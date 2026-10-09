import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError, Code } from './envelope';
import {
  createChannel,
  createCredential,
  listChannels,
  listCredentials,
  loadSettlement,
  loadUsageStats,
  PartnerChannelsPath,
  PartnerCredentialsPath,
  PartnerSettlementPath,
  PartnerUsageStatsPath,
  setChannelEnabled,
  setCredentialEnabled,
} from './partner';
import { EMPTY_USAGE_QUERY } from './usageUrl';

/** 商家域端点的调用形状：路径、方法、请求体与信封解包。 */

function envelopeOf(body: unknown, extra: Record<string, unknown> = {}) {
  return {
    code: 200,
    data: body,
    message: 'ok',
    page: null,
    size: null,
    total: null,
    ...extra,
  };
}

/** stubFetch 记录调用参数并返回给定信封。 */
function stubFetch(envelope: unknown) {
  const fetchMock = vi.fn().mockResolvedValue({ json: async () => envelope });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('listChannels', () => {
  it('分页与筛选进 query，分页信息取自信封', async () => {
    const fetchMock = stubFetch(
      envelopeOf(
        {
          items: [
            {
              id: 3,
              name: 'up-chat',
              vendor: 'vendor-a',
              type: 'openai_chat',
              cred_group: 'grp',
              base_url: 'https://up.example',
              priority: 100,
              weight: 100,
              enabled: true,
            },
          ],
        },
        { page: 1, size: 20, total: 1 },
      ),
    );

    const result = await listChannels({ page: 1, size: 20, enabled: true });

    expect(fetchMock.mock.calls[0]?.[0]).toBe(`${PartnerChannelsPath}?page=1&size=20&enabled=true`);
    expect(result.items).toHaveLength(1);
    expect(result.meta).toEqual({ page: 1, size: 20, total: 1 });
  });

  it('data 为 null 时返回空列表而不是抛出', async () => {
    stubFetch(envelopeOf(null));
    await expect(listChannels({ page: 1, size: 20 })).resolves.toEqual({
      items: [],
      meta: { page: null, size: null, total: null },
    });
  });

  it('业务码非 200 抛 ApiError', async () => {
    stubFetch({
      code: Code.Forbidden,
      data: null,
      message: '需要商家身份',
      page: null,
      size: null,
      total: null,
    });
    await expect(listChannels({ page: 1, size: 20 })).rejects.toBeInstanceOf(ApiError);
  });
});

describe('createChannel', () => {
  it('提交登记字段并回传新建的渠道', async () => {
    const fetchMock = stubFetch(
      envelopeOf({
        id: 4,
        name: 'up-chat',
        vendor: '',
        type: 'openai_chat',
        cred_group: 'grp',
        base_url: 'https://up.example',
        priority: 100,
        weight: 100,
        enabled: true,
      }),
    );

    const created = await createChannel({
      name: 'up-chat',
      type: 'openai_chat',
      cred_group: 'grp',
      base_url: 'https://up.example',
    });

    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe(PartnerChannelsPath);
    expect(init.method).toBe('POST');
    expect(JSON.parse(String(init.body))).toEqual({
      name: 'up-chat',
      type: 'openai_chat',
      cred_group: 'grp',
      base_url: 'https://up.example',
    });
    expect(created.id).toBe(4);
  });

  it('同名渠道的 409 抛 ApiError', async () => {
    stubFetch({
      code: Code.Conflict,
      data: null,
      message: '渠道登记失败：同名记录已存在',
      page: null,
      size: null,
      total: null,
    });
    await expect(
      createChannel({ name: 'n', type: 'openai_chat', cred_group: 'g', base_url: 'https://x' }),
    ).rejects.toBeInstanceOf(ApiError);
  });
});

describe('setChannelEnabled', () => {
  it('启停各自 POST 到对应路径', async () => {
    const fetchMock = stubFetch(envelopeOf(null));
    await setChannelEnabled(7, false);
    await setChannelEnabled(7, true);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`${PartnerChannelsPath}/7/disable`);
    expect(fetchMock.mock.calls[1]?.[0]).toBe(`${PartnerChannelsPath}/7/enable`);
  });

  it('非本商家的渠道回 404 时抛 ApiError', async () => {
    stubFetch({
      code: Code.NotFound,
      data: null,
      message: '渠道不存在',
      page: null,
      size: null,
      total: null,
    });
    await expect(setChannelEnabled(8, false)).rejects.toBeInstanceOf(ApiError);
  });
});

describe('credentials', () => {
  it('列表只带前缀，创建回传一次性明文', async () => {
    const fetchMock = stubFetch(
      envelopeOf({ items: [{ id: 9, cred_group: 'grp', name: 'primary', prefix: 'sk-1…', enabled: true }] }),
    );
    const listed = await listCredentials({ page: 1, size: 20 });
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`${PartnerCredentialsPath}?page=1&size=20`);
    expect(listed.items[0]?.prefix).toBe('sk-1…');

    const createMock = stubFetch(
      envelopeOf({ id: 10, cred_group: 'grp', name: 'primary', prefix: 'sk-1…', enabled: true, secret: 'sk-1-full' }),
    );
    const created = await createCredential({ cred_group: 'grp', name: 'primary', api_key: 'sk-1-full' });
    expect(createMock.mock.calls[0]?.[0]).toBe(PartnerCredentialsPath);
    expect(created.secret).toBe('sk-1-full');
  });

  it('停用凭据 POST 到停用路径', async () => {
    const fetchMock = stubFetch(envelopeOf(null));
    await setCredentialEnabled(9, false);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`${PartnerCredentialsPath}/9/disable`);
  });
});

describe('loadUsageStats', () => {
  it('聚合维度与区间进 query，合计取自信封', async () => {
    const fetchMock = stubFetch(
      envelopeOf(
        {
          items: [
            {
              key: '2026-10-09',
              calls: 3,
              usage: {
                input_tokens: 10,
                output_tokens: 4,
                cache_read_tokens: 0,
                cache_write_tokens: 0,
                cache_write_5m_tokens: 0,
                cache_write_1h_tokens: 0,
                reasoning_tokens: 0,
                server_tool_uses: 0,
              },
              charged_amount: '1.50000000',
            },
          ],
        },
        { page: 1, size: 1, total: 1 },
      ),
    );

    const result = await loadUsageStats({
      ...EMPTY_USAGE_QUERY,
      groupBy: 'model',
      since: '2026-10-01T00:00:00Z',
      model: 'up-glm-5',
    });

    // 参数顺序由契约声明的参数顺序决定（since / until / group_by / model / api_key_id）。
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      `${PartnerUsageStatsPath}?since=2026-10-01T00%3A00%3A00Z&group_by=model&model=up-glm-5`,
    );
    expect(result[0]?.charged_amount).toBe('1.50000000');
  });

  it('业务码非 200 抛 ApiError', async () => {
    stubFetch({
      code: Code.BadRequest,
      data: null,
      message: 'group_by 取值必须是 day / model / api_key',
      page: null,
      size: null,
      total: null,
    });
    await expect(loadUsageStats(EMPTY_USAGE_QUERY)).rejects.toBeInstanceOf(ApiError);
  });
});

describe('loadSettlement', () => {
  it('账期进 query，对账单取自信封', async () => {
    const fetchMock = stubFetch(
      envelopeOf({
        period: 'month',
        from: '2026-09-01T00:00:00Z',
        to: '2026-10-01T00:00:00Z',
        commission_rate: '0.1000',
        trades: 3,
        gross_sales: '100.00000000',
        commission: '10.00000000',
        upstream_cost: '30.00000000',
        payout: '60.00000000',
      }),
    );

    const bill = await loadSettlement({
      from: '2026-09-01T00:00:00Z',
      to: '2026-10-01T00:00:00Z',
    });

    // 参数顺序由契约声明的参数顺序决定（from / to）。
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      `${PartnerSettlementPath}?from=2026-09-01T00%3A00%3A00Z&to=2026-10-01T00%3A00%3A00Z`,
    );
    expect(bill.payout).toBe('60.00000000');
    expect(bill.commission_rate).toBe('0.1000');
  });

  it('空账期不下发参数，由服务端按商家账期取上一期', async () => {
    const fetchMock = stubFetch(
      envelopeOf({
        period: 'month',
        from: '2026-09-01T00:00:00Z',
        to: '2026-10-01T00:00:00Z',
        commission_rate: '0.0000',
        trades: 0,
        gross_sales: '0.00000000',
        commission: '0.00000000',
        upstream_cost: '0.00000000',
        payout: '0.00000000',
      }),
    );

    await loadSettlement({ from: '', to: '' });

    expect(fetchMock.mock.calls[0]?.[0]).toBe(PartnerSettlementPath);
  });

  it('响应缺少 data 时抛错：不把「没查出来」显示成空账期', async () => {
    stubFetch({ code: 200, data: null, message: 'ok', page: null, size: null, total: null });
    await expect(loadSettlement({ from: '', to: '' })).rejects.toThrow(/缺少 data/);
  });

  it('业务码非 200 抛 ApiError', async () => {
    stubFetch({
      code: Code.BadRequest,
      data: null,
      message: 'from 与 to 要么都给，要么都不给',
      page: null,
      size: null,
      total: null,
    });
    await expect(
      loadSettlement({ from: '2026-09-01T00:00:00Z', to: '' }),
    ).rejects.toBeInstanceOf(ApiError);
  });
});
