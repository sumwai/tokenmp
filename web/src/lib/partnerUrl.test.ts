import { describe, expect, it } from 'vitest';

import {
  accountsQueryString,
  DEFAULT_PAGE_SIZE,
  EMPTY_SETTLEMENT_WINDOW,
  MAX_PAGE_SIZE,
  parseAccountsQuery,
  parseSettlementWindow,
  settlementWindowSearch,
} from './partnerUrl';

/** 上游账号列表的 URL 状态：缺省、非法值与序列化。 */

describe('parseAccountsQuery', () => {
  it('缺省取契约默认', () => {
    expect(parseAccountsQuery('')).toEqual({ page: 1, size: DEFAULT_PAGE_SIZE, enabled: undefined });
  });

  it('非法值回落默认，超上限的 size 截到上限', () => {
    expect(parseAccountsQuery('?page=0&size=999&enabled=maybe')).toEqual({
      page: 1,
      size: MAX_PAGE_SIZE,
      enabled: undefined,
    });
  });

  it('合法值原样读出', () => {
    expect(parseAccountsQuery('?page=3&size=50&enabled=false')).toEqual({
      page: 3,
      size: 50,
      enabled: false,
    });
  });
});

describe('accountsQueryString', () => {
  it('规范化输出，不过滤时不带 enabled', () => {
    expect(accountsQueryString({ page: 1, size: 20 })).toBe('?page=1&size=20');
    expect(accountsQueryString({ page: 2, size: 50, enabled: true })).toBe(
      '?page=2&size=50&enabled=true',
    );
  });
});

describe('parseSettlementWindow', () => {
  it('两侧都给时原样读出', () => {
    expect(
      parseSettlementWindow(
        new URLSearchParams('from=2026-09-01T00:00:00Z&to=2026-10-01T00:00:00Z'),
      ),
    ).toEqual({ from: '2026-09-01T00:00:00Z', to: '2026-10-01T00:00:00Z' });
  });

  it('缺一侧或非法时刻都回落成空账期，不把 400 变成页面常态', () => {
    expect(parseSettlementWindow(new URLSearchParams(''))).toEqual(EMPTY_SETTLEMENT_WINDOW);
    expect(parseSettlementWindow(new URLSearchParams('from=2026-09-01T00:00:00Z'))).toEqual(
      EMPTY_SETTLEMENT_WINDOW,
    );
    expect(parseSettlementWindow(new URLSearchParams('from=昨天&to=2026-10-01T00:00:00Z'))).toEqual(
      EMPTY_SETTLEMENT_WINDOW,
    );
    expect(parseSettlementWindow(new URLSearchParams('from=2026/09/01&to=2026/10/01'))).toEqual(
      EMPTY_SETTLEMENT_WINDOW,
    );
  });
});

describe('settlementWindowSearch', () => {
  it('空账期不写出，两侧都有时带前导问号外的规范串', () => {
    expect(settlementWindowSearch(EMPTY_SETTLEMENT_WINDOW).toString()).toBe('');
    expect(
      settlementWindowSearch({ from: '2026-09-01T00:00:00Z', to: '2026-10-01T00:00:00Z' }).toString(),
    ).toBe('from=2026-09-01T00%3A00%3A00Z&to=2026-10-01T00%3A00%3A00Z');
  });
});
