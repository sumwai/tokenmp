import { describe, expect, it } from 'vitest';

import { DEFAULT_PAGE_SIZE, keysQueryString, MAX_PAGE_SIZE, parseKeysQuery } from './keysUrl';

/** 密钥列表的 URL 状态：缺省、上限与非法值回落。 */

describe('parseKeysQuery', () => {
  it('空 query 取契约缺省', () => {
    expect(parseKeysQuery('')).toEqual({ page: 1, size: DEFAULT_PAGE_SIZE, enabled: undefined });
  });

  it('读出分页与筛选', () => {
    expect(parseKeysQuery('?page=3&size=50&enabled=false')).toEqual({
      page: 3,
      size: 50,
      enabled: false,
    });
    expect(parseKeysQuery('?enabled=true').enabled).toBe(true);
  });

  it('超出上限的每页条数截到上限', () => {
    expect(parseKeysQuery('?size=1000').size).toBe(MAX_PAGE_SIZE);
  });

  it('非法值回落缺省而不是透传给服务端', () => {
    expect(parseKeysQuery('?page=0&size=-5')).toEqual({
      page: 1,
      size: DEFAULT_PAGE_SIZE,
      enabled: undefined,
    });
    expect(parseKeysQuery('?page=abc&size=2.5&enabled=1')).toEqual({
      page: 1,
      size: DEFAULT_PAGE_SIZE,
      enabled: undefined,
    });
  });
});

describe('keysQueryString', () => {
  it('条件全量序列化，不过滤时省略 enabled', () => {
    expect(keysQueryString({ page: 2, size: 20 })).toBe('?page=2&size=20');
    expect(keysQueryString({ page: 1, size: 100, enabled: true })).toBe(
      '?page=1&size=100&enabled=true',
    );
  });

  it('序列化结果能被解析回同一条件（刷新与分享保持状态）', () => {
    const query = { page: 4, size: 50, enabled: false };
    expect(parseKeysQuery(keysQueryString(query))).toEqual(query);
  });
});
