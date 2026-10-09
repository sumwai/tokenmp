/**
 * 密钥列表的 URL 状态：筛选与分页全部进 query，刷新与分享保持（web/AGENTS.md 表格）。
 *
 * 非法值一律回落契约默认：URL 是用户可手改的输入，原样透传只会把 400
 * 变成页面的常态。上限与默认值与 docs/openapi-web.yaml 的 Page / Size 参数一致。
 */

/** 每页条数缺省值，与契约 Size 的 default 一致。 */
export const DEFAULT_PAGE_SIZE = 20;

/** 每页条数上限，与契约 Size 的 maximum 一致。 */
export const MAX_PAGE_SIZE = 100;

/** KeysQuery 是密钥列表的服务端查询条件，也就是进 URL 的那部分状态。 */
export interface KeysQuery {
  page: number;
  size: number;
  /** 启用状态过滤；undefined 表示不过滤。 */
  enabled?: boolean;
}

/** parseKeysQuery 从 URL query 读出查询条件；缺省与非法值取契约默认。 */
export function parseKeysQuery(search: string): KeysQuery {
  const params = new URLSearchParams(search);
  return {
    page: parsePage(params.get('page')),
    size: parseSize(params.get('size')),
    enabled: parseEnabled(params.get('enabled')),
  };
}

/** keysQueryString 把查询条件序列化为规范化的 query 串（含前导问号）。 */
export function keysQueryString(query: KeysQuery): string {
  const params = new URLSearchParams();
  params.set('page', String(query.page));
  params.set('size', String(query.size));
  if (query.enabled !== undefined) {
    params.set('enabled', String(query.enabled));
  }
  return `?${params.toString()}`;
}

/** parsePage 读页码：正整数，其余取 1。 */
function parsePage(raw: string | null): number {
  const value = toPositiveInt(raw);
  return value ?? 1;
}

/** parseSize 读每页条数：正整数且截到上限，其余取缺省。 */
function parseSize(raw: string | null): number {
  const value = toPositiveInt(raw);
  if (value === undefined) {
    return DEFAULT_PAGE_SIZE;
  }
  return Math.min(value, MAX_PAGE_SIZE);
}

/** parseEnabled 读启用状态过滤：只认布尔字面量，其余视为不过滤。 */
function parseEnabled(raw: string | null): boolean | undefined {
  if (raw === 'true') return true;
  if (raw === 'false') return false;
  return undefined;
}

/** toPositiveInt 解析正整数；非数字、非整数与零以下都返回 undefined。 */
function toPositiveInt(raw: string | null): number | undefined {
  if (raw === null || !/^\d+$/.test(raw.trim())) {
    return undefined;
  }
  const value = Number.parseInt(raw.trim(), 10);
  return value >= 1 ? value : undefined;
}
