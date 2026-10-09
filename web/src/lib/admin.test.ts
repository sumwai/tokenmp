import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  ADMIN_MAX_PAGE_SIZE,
  ADMIN_PAGE_SIZE,
  AdminPaths,
  FilterAccountID,
  FilterFrom,
  FilterMerchantID,
  FilterSince,
  FilterTo,
  adminQueryKey,
  adminQuerySearch,
  listAdminChannels,
  listAdminCredentials,
  listAdminPricing,
  listAdminQuotas,
  listAdminSettlements,
  listAdminUsage,
  parseAdminQuery,
} from './admin';
import { ApiError, Code } from './envelope';

/**
 * 管理面清单的 URL 状态与取数。
 *
 * URL 是页面状态的唯一出处：解析要宽容（手改的地址不该打不开），序列化要规范
 * （同一个状态得到同一个地址）。取数走请求层，这里只核对地址装配、信封解包与
 * 业务码分支。
 */

const ok = (data: unknown, page: number | null = null, total: number | null = null) => ({
  code: 200,
  data,
  message: 'ok',
  page,
  size: page === null ? null : ADMIN_PAGE_SIZE,
  total,
});

const forbidden = { code: 403, data: null, message: '无管理面权限', page: null, size: null, total: null };

/** stubFetch 用桩替换 fetch，返回该次响应的 JSON。 */
function stubFetch(payload: unknown) {
  const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => ({
    json: async () => payload,
  }));
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('parseAdminQuery', () => {
  it('缺省取契约默认', () => {
    expect(parseAdminQuery(new URLSearchParams())).toEqual({
      page: 1,
      size: ADMIN_PAGE_SIZE,
      filters: {},
    });
  });

  it('非法页码与零取默认值', () => {
    for (const raw of ['0', '-3', 'abc', '']) {
      expect(parseAdminQuery(new URLSearchParams(`page=${raw}`)).page).toBe(1);
    }
  });

  it('每页条数截到上限', () => {
    expect(parseAdminQuery(new URLSearchParams('size=500')).size).toBe(ADMIN_MAX_PAGE_SIZE);
  });

  it('除分页外的键都作为过滤条件，空值丢弃', () => {
    const query = parseAdminQuery(new URLSearchParams('page=2&size=50&account_id=7&model=&scope=account'));
    expect(query.filters).toEqual({ account_id: '7', scope: 'account' });
  });
});

describe('adminQuerySearch 与 adminQueryKey', () => {
  it('序列化后能被解析回同一状态', () => {
    const query = { page: 3, size: 50, filters: { [FilterAccountID]: '7', [FilterMerchantID]: '' } };
    const params = adminQuerySearch(query);
    expect(parseAdminQuery(params)).toEqual({ page: 3, size: 50, filters: { [FilterAccountID]: '7' } });
  });

  it('同一个状态得到同一个 key，过滤条件变化时 key 变化', () => {
    const base = { page: 1, size: ADMIN_PAGE_SIZE, filters: { [FilterAccountID]: '7' } };
    expect(adminQueryKey(base)).toBe(adminQueryKey({ ...base }));
    expect(adminQueryKey(base)).not.toBe(adminQueryKey({ ...base, page: 2 }));
  });
});

describe('清单取数', () => {
  it('地址带分页，条目与分页信息取自信封', async () => {
    const fetchMock = stubFetch(ok({ items: [{ id: 1 }] }, 2, 41));
    const { items, meta } = await listAdminChannels({ page: 2, size: ADMIN_PAGE_SIZE, filters: {} });

    expect(fetchMock.mock.calls[0][0]).toBe(`${AdminPaths.channels}?page=2&size=${ADMIN_PAGE_SIZE}`);
    expect(items).toEqual([{ id: 1 }]);
    expect(meta).toEqual({ page: 2, size: ADMIN_PAGE_SIZE, total: 41 });
  });

  it('data 为 null 时条目为空数组，页面不会拿到 undefined', async () => {
    stubFetch(ok(null));
    await expect(listAdminCredentials({ page: 1, size: ADMIN_PAGE_SIZE, filters: {} })).resolves.toEqual({
      items: [],
      meta: { page: null, size: null, total: null },
    });
  });

  it('只带本资源声明的过滤键：不相关的键不进地址', async () => {
    const fetchMock = stubFetch(ok({ items: [] }));
    await listAdminChannels({ page: 1, size: ADMIN_PAGE_SIZE, filters: { [FilterAccountID]: '7' } });
    expect(fetchMock.mock.calls[0][0]).toBe(`${AdminPaths.channels}?page=1&size=${ADMIN_PAGE_SIZE}`);
  });

  it('本资源声明的过滤键按契约的名字进地址', async () => {
    const fetchMock = stubFetch(ok({ items: [] }));
    await listAdminPricing({
      page: 1,
      size: ADMIN_PAGE_SIZE,
      filters: { [FilterMerchantID]: '9', model: 'gpt-4o-mini' },
    });
    expect(fetchMock.mock.calls[0][0]).toBe(
      `${AdminPaths.pricing}?page=1&size=${ADMIN_PAGE_SIZE}&merchant_id=9&model=gpt-4o-mini`,
    );
  });

  it('限额清单带范围与账户过滤', async () => {
    const fetchMock = stubFetch(ok({ items: [] }));
    await listAdminQuotas({
      page: 1,
      size: ADMIN_PAGE_SIZE,
      filters: { scope: 'api_key', scope_id: '12', [FilterAccountID]: '5' },
    });
    expect(fetchMock.mock.calls[0][0]).toBe(
      `${AdminPaths.quotas}?page=1&size=${ADMIN_PAGE_SIZE}&scope=api_key&scope_id=12&account_id=5`,
    );
  });

  it('流水清单带起始时刻过滤，时刻原样传递不重排', async () => {
    const fetchMock = stubFetch(ok({ items: [] }));
    await listAdminUsage({
      page: 1,
      size: ADMIN_PAGE_SIZE,
      filters: { [FilterSince]: '2026-10-09T00:00:00Z' },
    });
    expect(fetchMock.mock.calls[0][0]).toBe(
      `${AdminPaths.usage}?page=1&size=${ADMIN_PAGE_SIZE}&since=2026-10-09T00%3A00%3A00Z`,
    );
  });

  it('业务码非 200 时抛 ApiError：越权不被当成空清单', async () => {
    stubFetch(forbidden);
    await expect(
      listAdminChannels({ page: 1, size: ADMIN_PAGE_SIZE, filters: {} }),
    ).rejects.toSatisfy((err: unknown) => err instanceof ApiError && err.code === Code.Forbidden);
  });
});

describe('结算清单取数', () => {
  it('商家与账期过滤按契约的名字进地址，条目取自信封', async () => {
    const fetchMock = stubFetch(
      ok(
        {
          items: [
            {
              merchant_id: 2,
              period: 'month',
              from: '2026-09-01T00:00:00Z',
              to: '2026-10-01T00:00:00Z',
              commission_rate: '0.1000',
              trades: 3,
              gross_sales: '100.00000000',
              commission: '10.00000000',
              upstream_cost: '30.00000000',
              payout: '60.00000000',
            },
          ],
        },
        1,
        1,
      ),
    );

    const { items, meta } = await listAdminSettlements({
      page: 1,
      size: ADMIN_PAGE_SIZE,
      filters: {
        [FilterMerchantID]: '2',
        [FilterFrom]: '2026-09-01T00:00:00Z',
        [FilterTo]: '2026-10-01T00:00:00Z',
        account_id: '7',
      },
    });

    // 账期原样传递不重排，未声明的过滤键（account_id）不进地址。
    expect(fetchMock.mock.calls[0][0]).toBe(
      `${AdminPaths.settlements}?page=1&size=${ADMIN_PAGE_SIZE}` +
        '&merchant_id=2&from=2026-09-01T00%3A00%3A00Z&to=2026-10-01T00%3A00%3A00Z',
    );
    expect(items[0]?.payout).toBe('60.00000000');
    expect(meta).toEqual({ page: 1, size: ADMIN_PAGE_SIZE, total: 1 });
  });

  it('业务码非 200 时抛 ApiError', async () => {
    stubFetch(forbidden);
    await expect(
      listAdminSettlements({ page: 1, size: ADMIN_PAGE_SIZE, filters: {} }),
    ).rejects.toSatisfy((err: unknown) => err instanceof ApiError && err.code === Code.Forbidden);
  });
});
