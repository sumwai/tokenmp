import { describe, expect, it } from 'vitest';

import {
  DEFAULT_PAGE_SIZE,
  EMPTY_LIST_QUERY,
  diffPayloadShapes,
  formatBytes,
  formatCount,
  formatDuration,
  formatMoment,
  hasFilters,
  listQuerySearch,
  parseListQuery,
  payloadNotice,
  requestDetailPath,
  requestsPath,
  summarizeShapeValue,
  type RequestListQuery,
} from './requests';

/** 列表状态的 URL 往返、展示口径与报文差异计算；都是纯函数，不经网络与 DOM。 */

/** query 构造一个列表状态，未给字段取默认值。 */
function query(overrides: Partial<RequestListQuery> = {}): RequestListQuery {
  return { ...EMPTY_LIST_QUERY, ...overrides };
}

describe('parseListQuery', () => {
  it('缺省时全部回落默认值', () => {
    expect(parseListQuery(new URLSearchParams())).toEqual(EMPTY_LIST_QUERY);
  });

  it('解析合法取值', () => {
    const parsed = parseListQuery(
      new URLSearchParams({
        page: '3',
        size: '50',
        since: '2026-10-01T00:00:00Z',
        until: '2026-10-02T00:00:00+08:00',
        model: 'gpt-4o-mini',
        api_key_id: '42',
        status: 'failed',
        request_id: 'req_abc',
      }),
    );
    expect(parsed).toEqual({
      page: 3,
      size: 50,
      since: '2026-10-01T00:00:00Z',
      until: '2026-10-02T00:00:00+08:00',
      model: 'gpt-4o-mini',
      apiKeyID: '42',
      status: 'failed',
      requestID: 'req_abc',
    });
  });

  it('非法取值回落默认而不是发出必然 400 的请求', () => {
    const parsed = parseListQuery(
      new URLSearchParams({
        page: '0',
        size: '999',
        status: 'unknown',
        api_key_id: '-1',
        since: 'yesterday',
      }),
    );
    expect(parsed.page).toBe(1);
    expect(parsed.size).toBe(100);
    expect(parsed.status).toBe('');
    expect(parsed.apiKeyID).toBe('');
    expect(parsed.since).toBe('');
  });

  it('去除首尾空白', () => {
    const parsed = parseListQuery(new URLSearchParams({ model: ' gpt-4o ' }));
    expect(parsed.model).toBe('gpt-4o');
  });
});

describe('listQuerySearch', () => {
  it('默认状态不写出任何参数', () => {
    expect(listQuerySearch(EMPTY_LIST_QUERY).toString()).toBe('');
  });

  it('与 parseListQuery 往返一致', () => {
    const original = query({
      page: 2,
      size: 100,
      since: '2026-10-01T00:00:00Z',
      model: 'claude-sonnet-4',
      apiKeyID: '7',
      status: 'success',
      requestID: 'req_1',
    });
    expect(parseListQuery(listQuerySearch(original))).toEqual(original);
  });

  it('页面路径与端点查询参数使用同一份状态', () => {
    expect(requestsPath(EMPTY_LIST_QUERY)).toBe('/requests');
    expect(requestsPath(query({ status: 'failed', page: 2 }))).toBe(
      '/requests?page=2&status=failed',
    );
  });

  it('详情路径对标识做转义，深链可分享', () => {
    expect(requestDetailPath('req_1')).toBe('/requests/req_1');
    expect(requestDetailPath('a/b')).toBe('/requests/a%2Fb');
  });

  it('分页不算筛选条件', () => {
    expect(hasFilters(query({ page: 5 }))).toBe(false);
    expect(hasFilters(query({ model: 'gpt-4o' }))).toBe(true);
  });
});

describe('展示口径', () => {
  it('时刻按本地时间显示，无法解析时原样返回', () => {
    expect(formatMoment('2026-10-08T00:30:05Z')).toMatch(/^2026-10-08 \d{2}:\d{2}:\d{2}$/);
    expect(formatMoment('不是时间')).toBe('不是时间');
  });

  it('耗时按量级换单位', () => {
    expect(formatDuration(0)).toBe('0 ms');
    expect(formatDuration(320)).toBe('320 ms');
    expect(formatDuration(1234)).toBe('1.2 s');
    expect(formatDuration(-1)).toBe('—');
  });

  it('计数千分位，字节数换单位', () => {
    expect(formatCount(0)).toBe('0');
    expect(formatCount(1234567)).toBe('1,234,567');
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(2048)).toBe('2.0 KiB');
    expect(formatBytes(3 * 1024 * 1024)).toBe('3.0 MiB');
  });

  it('报文缺失态不写成错误', () => {
    expect(payloadNotice(false)).toContain('本次没有可取的报文');
    expect(payloadNotice(false)).toContain('7 天');
    expect(payloadNotice(true)).toContain('脱敏报文');
  });
});

describe('summarizeShapeValue', () => {
  it('类型标记显示为标记与长度', () => {
    expect(summarizeShapeValue({ __redacted: 'string', len: 12 })).toBe('string（12 字节）');
    expect(summarizeShapeValue({ __redacted: 'object' })).toBe('object');
  });

  it('数组与对象显示形状，标量原样', () => {
    expect(summarizeShapeValue([1, 2, 3])).toBe('[3 项]');
    expect(summarizeShapeValue({ a: 1, b: 2 })).toBe('{2 键}');
    expect(summarizeShapeValue(null)).toBe('null');
    expect(summarizeShapeValue('gpt-4o')).toBe('"gpt-4o"');
  });
});

describe('diffPayloadShapes', () => {
  it('结构相同则没有差异', () => {
    const shape = { model: 'gpt-4o', messages: [{ role: 'user' }] };
    expect(diffPayloadShapes(shape, { ...shape })).toEqual([]);
  });

  it('标量改写记为 changed 并给出前后摘要', () => {
    const entries = diffPayloadShapes({ model: 'gpt-4o' }, { model: 'gpt-4o-2024' });
    expect(entries).toEqual([
      { path: 'model', kind: 'changed', from: '"gpt-4o"', to: '"gpt-4o-2024"' },
    ]);
  });

  it('新增、缺失与嵌套路径都带完整路径', () => {
    const entries = diffPayloadShapes(
      { temperature: 1, messages: [{ content: { __redacted: 'string', len: 5 } }] },
      {
        temperature: 1,
        max_tokens: 1024,
        messages: [{ content: { __redacted: 'string', len: 40 } }],
      },
    );
    expect(entries).toEqual([
      { path: 'max_tokens', kind: 'added', from: '', to: '1024' },
      {
        path: 'messages[0].content',
        kind: 'changed',
        from: 'string（5 字节）',
        to: 'string（40 字节）',
      },
    ]);
  });

  it('数组元素增减记为 added / removed', () => {
    const entries = diffPayloadShapes({ messages: [1, 2] }, { messages: [1, 2, 3] });
    expect(entries).toEqual([{ path: 'messages[2]', kind: 'added', from: '', to: '3' }]);
  });

  it('根取值不同时用占位路径', () => {
    expect(diffPayloadShapes('a', 'b')).toEqual([
      { path: '(根)', kind: 'changed', from: '"a"', to: '"b"' },
    ]);
  });
});

describe('EMPTY_LIST_QUERY', () => {
  it('默认每页条数与契约缺省一致', () => {
    expect(DEFAULT_PAGE_SIZE).toBe(20);
  });
});
