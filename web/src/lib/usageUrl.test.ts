import { describe, expect, it } from 'vitest';

import {
  EMPTY_USAGE_QUERY,
  hasUsageFilters,
  parseUsageQuery,
  usagePath,
  usageQueryString,
  type UsageQuery,
} from './usageUrl';

/** 用量聚合页的 URL 状态：缺省、非法回落与序列化往返。 */

const parse = (search: string) => parseUsageQuery(new URLSearchParams(search));

describe('parseUsageQuery', () => {
  it('缺省：不限区间、按天分组、不过滤', () => {
    expect(parse('')).toEqual(EMPTY_USAGE_QUERY);
  });

  it('读出区间、分组维度与过滤条件', () => {
    expect(
      parse('since=2026-01-01T00:00:00Z&until=2026-02-01T00:00:00%2B08:00&group_by=api_key&model=gpt-4o&api_key_id=7'),
    ).toEqual({
      since: '2026-01-01T00:00:00Z',
      until: '2026-02-01T00:00:00+08:00',
      groupBy: 'api_key',
      model: 'gpt-4o',
      apiKeyID: '7',
    });
  });

  it('非法取值回落契约默认，不透传给服务端', () => {
    const cases: [string, Partial<UsageQuery>][] = [
      ['since=yesterday', { since: '' }],
      ['until=2026-13-01T00:00:00Z', { until: '' }],
      ['group_by=week', { groupBy: 'day' }],
      ['api_key_id=0', { apiKeyID: '' }],
      ['api_key_id=-1', { apiKeyID: '' }],
      ['api_key_id=1.5', { apiKeyID: '' }],
    ];
    for (const [search, expected] of cases) {
      expect(parse(search)).toEqual({ ...EMPTY_USAGE_QUERY, ...expected });
    }
  });

  it('过滤取值去首尾空白', () => {
    expect(parse('model=%20gpt-4o%20').model).toBe('gpt-4o');
  });
});

describe('usageQueryString', () => {
  it('等于默认值的项不写出，缺省状态得到空 query', () => {
    expect(usageQueryString(EMPTY_USAGE_QUERY).toString()).toBe('');
  });

  it('序列化后解析回来与入参一致（往返）', () => {
    const query: UsageQuery = {
      since: '2026-01-01T00:00:00Z',
      until: '',
      groupBy: 'model',
      model: 'gpt-4o',
      apiKeyID: '7',
    };
    expect(parseUsageQuery(usageQueryString(query))).toEqual(query);
  });

  it('按天是缺省维度，不写进 query', () => {
    expect(usageQueryString({ ...EMPTY_USAGE_QUERY, groupBy: 'day' }).toString()).toBe('');
  });
});

describe('usagePath', () => {
  it('缺省状态指向 /usage', () => {
    expect(usagePath(EMPTY_USAGE_QUERY)).toBe('/usage');
  });

  it('带条件时把 query 拼上，深链可分享', () => {
    const path = usagePath({ ...EMPTY_USAGE_QUERY, groupBy: 'api_key', apiKeyID: '7' });
    expect(path.startsWith('/usage?')).toBe(true);
    expect(path).toContain('group_by=api_key');
    expect(path).toContain('api_key_id=7');
  });
});

describe('hasUsageFilters', () => {
  it('分组维度不是过滤条件', () => {
    expect(hasUsageFilters({ ...EMPTY_USAGE_QUERY, groupBy: 'model' })).toBe(false);
  });

  it('区间与模型、密钥过滤都算条件', () => {
    expect(hasUsageFilters({ ...EMPTY_USAGE_QUERY, since: '2026-01-01T00:00:00Z' })).toBe(true);
    expect(hasUsageFilters({ ...EMPTY_USAGE_QUERY, until: '2026-01-01T00:00:00Z' })).toBe(true);
    expect(hasUsageFilters({ ...EMPTY_USAGE_QUERY, model: 'gpt-4o' })).toBe(true);
    expect(hasUsageFilters({ ...EMPTY_USAGE_QUERY, apiKeyID: '7' })).toBe(true);
  });
});
