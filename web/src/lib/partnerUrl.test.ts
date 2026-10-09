import { describe, expect, it } from 'vitest';

import {
  accountsQueryString,
  DEFAULT_PAGE_SIZE,
  MAX_PAGE_SIZE,
  parseAccountsQuery,
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
