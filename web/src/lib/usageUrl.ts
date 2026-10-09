/**
 * 用量聚合页的 URL 状态：区间、分组维度与过滤全部进 query。
 *
 * URL 是页面状态的唯一出处（web/AGENTS.md 的表格规范）：刷新、分享与后退都据此复原。
 * 非法取值一律回落契约默认 —— URL 是用户可手改的输入，原样透传只会把 400 变成页面常态。
 */

/** 分组维度，与契约 get_user_usage_stats 的 group_by 枚举一致。 */
export type UsageGroupBy = 'day' | 'model' | 'api_key';

/** 分组维度的取值顺序，与契约枚举一致。 */
export const USAGE_GROUPS: UsageGroupBy[] = ['day', 'model', 'api_key'];

/** 缺省分组维度，与契约「缺省 day」一致。 */
export const DEFAULT_GROUP_BY: UsageGroupBy = 'day';

/** UsageQuery 是聚合页进 URL 的那部分状态，键名与契约参数名一致。 */
export interface UsageQuery {
  /** 起始时刻（含），RFC3339；空串表示不限。 */
  since: string;
  /** 结束时刻（含），RFC3339；空串表示不限。 */
  until: string;
  groupBy: UsageGroupBy;
  /** 模型名精确匹配；空串表示不过滤。 */
  model: string;
  /** 密钥 id；空串表示不过滤。 */
  apiKeyID: string;
}

/** 缺省状态：不限区间、按天分组、不过滤。 */
export const EMPTY_USAGE_QUERY: UsageQuery = {
  since: '',
  until: '',
  groupBy: DEFAULT_GROUP_BY,
  model: '',
  apiKeyID: '',
};

/** parseUsageQuery 从 URL query 读出聚合条件；缺失或非法取值回落默认。 */
export function parseUsageQuery(search: URLSearchParams): UsageQuery {
  return {
    since: parseMoment(search.get('since')),
    until: parseMoment(search.get('until')),
    groupBy: parseGroupBy(search.get('group_by')),
    model: trimmed(search.get('model')),
    apiKeyID: parseKeyID(search.get('api_key_id')),
  };
}

/** usageQueryString 把聚合条件序列化为 query；等于默认值的项不写出，保持链接简短。 */
export function usageQueryString(query: UsageQuery): URLSearchParams {
  const params = new URLSearchParams();
  if (query.since !== '') params.set('since', query.since);
  if (query.until !== '') params.set('until', query.until);
  if (query.groupBy !== DEFAULT_GROUP_BY) params.set('group_by', query.groupBy);
  if (query.model !== '') params.set('model', query.model);
  if (query.apiKeyID !== '') params.set('api_key_id', query.apiKeyID);
  return params;
}

/** usagePath 是聚合页路径（含条件），用于深链与「清除筛选」。 */
export function usagePath(query: UsageQuery): string {
  const search = usageQueryString(query).toString();
  return search === '' ? '/usage' : `/usage?${search}`;
}

/** hasUsageFilters 判断是否带了区间或模型、密钥过滤（分组维度不算过滤）。 */
export function hasUsageFilters(query: UsageQuery): boolean {
  return query.since !== '' || query.until !== '' || query.model !== '' || query.apiKeyID !== '';
}

/** parseGroupBy 解析分组维度；非法或缺失回落按天。 */
export function parseGroupBy(raw: string | null): UsageGroupBy {
  const value = trimmed(raw);
  return (USAGE_GROUPS as string[]).includes(value) ? (value as UsageGroupBy) : DEFAULT_GROUP_BY;
}

/** trimmed 去首尾空白；null 视为空串。 */
function trimmed(raw: string | null): string {
  return (raw ?? '').trim();
}

/** parseKeyID 解析密钥 id 过滤；非法取值回落「不过滤」，而不是发出必然 400 的请求。 */
function parseKeyID(raw: string | null): string {
  const value = trimmed(raw);
  return /^[1-9][0-9]*$/.test(value) ? value : '';
}

/**
 * parseMoment 解析 RFC3339 时刻；非法取值回落「不限」，避免把脏参数发往服务端。
 *
 * 导出给商家域的分账对账单页复用：账期同样是「URL 里手改得来的时刻」，回落口径必须与
 * 用量页逐字相同 —— 两处各写一份正则，迟早一处放宽一处收紧。
 */
export function parseMoment(raw: string | null): string {
  const value = trimmed(raw);
  if (value === '') {
    return '';
  }
  const rfc3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;
  if (!rfc3339.test(value) || Number.isNaN(new Date(value).getTime())) {
    return '';
  }
  return value;
}
