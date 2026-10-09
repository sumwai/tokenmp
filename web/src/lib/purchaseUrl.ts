/**
 * 订单列表的 URL 状态：分页进 query，刷新与分享保持（web/AGENTS.md 表格）。
 *
 * 非法值一律回落契约默认：URL 是用户可手改的输入，原样透传只会把 400 变成页面常态。
 * 上限与默认值与 docs/openapi-web.yaml 的 Page / Size 参数一致。
 */

/** 每页条数缺省值，与契约 Size 的 default 一致。 */
export const DEFAULT_PAGE_SIZE = 20;

/** 每页条数上限，与契约 Size 的 maximum 一致。 */
export const MAX_PAGE_SIZE = 100;

/** OrdersQuery 是订单列表的服务端查询条件，也就是进 URL 的那部分状态。 */
export interface OrdersQuery {
  page: number;
  size: number;
}

/** parseOrdersQuery 从 URL query 读出查询条件；缺省与非法值取契约默认。 */
export function parseOrdersQuery(search: string): OrdersQuery {
  const params = new URLSearchParams(search);
  const page = toPositiveInt(params.get('page')) ?? 1;
  const size = toPositiveInt(params.get('size'));
  return { page, size: size === undefined ? DEFAULT_PAGE_SIZE : Math.min(size, MAX_PAGE_SIZE) };
}

/** ordersQueryString 把查询条件序列化为规范化的 query 串（含前导问号）。 */
export function ordersQueryString(query: OrdersQuery): string {
  const params = new URLSearchParams();
  params.set('page', String(query.page));
  params.set('size', String(query.size));
  return `?${params.toString()}`;
}

/** toPositiveInt 解析正整数；非数字、非整数与零以下都返回 undefined。 */
function toPositiveInt(raw: string | null): number | undefined {
  if (raw === null || !/^\d+$/.test(raw.trim())) {
    return undefined;
  }
  const value = Number.parseInt(raw.trim(), 10);
  return value >= 1 ? value : undefined;
}
