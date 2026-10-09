import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError, Code } from './envelope';
import { totalUsage, usageStats, type UsageStatsItem, type UsageTokens } from './usage';

/** 用量聚合的请求层与合计口径；fetch 用桩替换，不经网络。 */

const ok = (data: unknown) => ({
  code: 200,
  data,
  message: 'ok',
  page: 1,
  size: 1,
  total: 1,
});

/** stubFetch 用桩替换 fetch，handler 返回该次响应解析出的 JSON。 */
function stubFetch(handler: (url: string, init: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init: RequestInit) => ({
    json: async () => handler(url, init),
  }));
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

/** 一次查询里没有内容的 query 参数。 */
function readQuery(url: string): URLSearchParams {
  return new URLSearchParams(url.slice(url.indexOf('?') + 1));
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('usageStats', () => {
  it('取回分组合计；端点不分页，一次返回全部维度分组', async () => {
    const items: UsageStatsItem[] = [
      {
        key: '2026-01-01',
        calls: 2,
        usage: {
          input_tokens: 100,
          output_tokens: 50,
          cache_read_tokens: 0,
          cache_write_tokens: 0,
          cache_write_5m_tokens: 0,
          cache_write_1h_tokens: 0,
          reasoning_tokens: 0,
          server_tool_uses: 0,
        },
        charged_amount: '1.5',
      },
    ];
    stubFetch(() => ok({ items }));
    await expect(
      usageStats({ since: '', until: '', groupBy: 'day', model: '', apiKeyID: '' }),
    ).resolves.toEqual(items);
  });

  it('按契约装配地址：缺省维度照发，空条件不写进请求', async () => {
    const fetchMock = stubFetch(() => ok({ items: [] }));
    await usageStats({ since: '', until: '', groupBy: 'day', model: '', apiKeyID: '' });
    await usageStats({
      since: '2026-01-01T00:00:00Z',
      until: '',
      groupBy: 'api_key',
      model: 'gpt-4o',
      apiKeyID: '7',
    });

    const [wildcard, filtered] = fetchMock.mock.calls.map(([url]) => url);
    expect(wildcard.startsWith('/api/v1/user/usage/stats?')).toBe(true);
    expect([...readQuery(wildcard).keys()]).toEqual(['group_by']);
    expect(readQuery(wildcard).get('group_by')).toBe('day');

    expect(readQuery(filtered).get('group_by')).toBe('api_key');
    expect(readQuery(filtered).get('since')).toBe('2026-01-01T00:00:00Z');
    expect(readQuery(filtered).get('model')).toBe('gpt-4o');
    expect(readQuery(filtered).get('api_key_id')).toBe('7');
    expect(readQuery(filtered).has('until')).toBe(false);
  });

  it('业务码非 200 时抛 ApiError', async () => {
    stubFetch(() => ({ code: Code.TooManyRequests, data: null, message: '请求过于频繁', page: null, size: null, total: null }));
    await expect(
      usageStats({ since: '', until: '', groupBy: 'day', model: '', apiKeyID: '' }),
    ).rejects.toBeInstanceOf(ApiError);
  });
});

/** item 造一条分组合计；未点名的分量取零。 */
function item(key: string, calls: number, tokens: Partial<UsageTokens>, charged: string): UsageStatsItem {
  return {
    key,
    calls,
    usage: {
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      cache_write_5m_tokens: 0,
      cache_write_1h_tokens: 0,
      reasoning_tokens: 0,
      server_tool_uses: 0,
      ...tokens,
    },
    charged_amount: charged,
  };
}

describe('totalUsage', () => {
  it('合计等于各行逐行相加：次数、各分量与应扣量', () => {
    const total = totalUsage([
      item('2026-01-01', 2, { input_tokens: 100, output_tokens: 50, cache_read_tokens: 10, reasoning_tokens: 5 }, '1.5'),
      item('2026-01-02', 3, { input_tokens: 7, cache_write_5m_tokens: 4 }, '2.25'),
    ]);
    expect(total.calls).toBe(5);
    expect(total.tokens).toEqual({
      input_tokens: 107,
      output_tokens: 50,
      cache_read_tokens: 10,
      cache_write_tokens: 0,
      cache_write_5m_tokens: 4,
      cache_write_1h_tokens: 0,
      reasoning_tokens: 5,
      server_tool_uses: 0,
    });
    expect(total.chargedAmount).toBe('3.75');
  });

  it('应扣量合计不经浮点：超出双精度的取值逐位保真', () => {
    const total = totalUsage([
      item('a', 1, {}, '9007199254740993'),
      item('b', 1, {}, '0.1'),
      item('c', 1, {}, '0.2'),
    ]);
    // 经 Number 会把末位改掉；字符串相加给出精确值。
    expect(total.chargedAmount).toBe('9007199254740993.3');
  });

  it('空集合的合计是全零与零金额', () => {
    const total = totalUsage([]);
    expect(total.calls).toBe(0);
    expect(total.chargedAmount).toBe('0');
  });

  it('应扣量不是十进制形态时合计取不回，返回 null', () => {
    expect(totalUsage([item('a', 1, {}, 'n/a')]).chargedAmount).toBeNull();
  });
});
