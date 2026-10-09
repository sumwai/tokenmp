import { requestPage, type PageMeta } from './client';
import { parseUnsignedInt } from './decimal';
import type { components, paths } from './generated/schema';

/**
 * 管理面只读清单端点（`/api/v1/admin/*`）的类型与调用。
 *
 * 字段与 docs/openapi-web.yaml 的 Admin* 一致，类型取自契约生成物（web/AGENTS.md：
 * 响应类型不得手写）。清单里的行与 `tokenmp admin <组> list --json` 同字段，
 * 页面因此与 CLI 看到同一份事实。
 */

/** AdminChannel 是一条上游渠道。 */
export type AdminChannel = components['schemas']['AdminChannel'];
/** AdminCredential 是一行上游凭据（只有脱敏前缀）。 */
export type AdminCredential = components['schemas']['AdminCredential'];
/** AdminModelMap 是一条渠道模型映射。 */
export type AdminModelMap = components['schemas']['AdminModelMap'];
/** AdminAccount 是一个账户。 */
export type AdminAccount = components['schemas']['AdminAccount'];
/** AdminPricing 是一个定价版本。 */
export type AdminPricing = components['schemas']['AdminPricing'];
/** AdminQuota 是一条窗口限额与当前用量。 */
export type AdminQuota = components['schemas']['AdminQuota'];
/** AdminAdjustment 是一条人工调账记录。 */
export type AdminAdjustment = components['schemas']['AdminAdjustment'];
/** AdminUsageItem 是一条跨账户的用量流水。 */
export type AdminUsageItem = components['schemas']['AdminUsageItem'];
/** AdminSettlementItem 是一个商家的结算对账单。 */
export type AdminSettlementItem = components['schemas']['AdminSettlementItem'];

/**
 * AdminPaths 与契约里的清单路径一一对应。
 *
 * `satisfies` 让契约改名时在这里编译失败，而不是等到运行时 404。
 */
export const AdminPaths = {
  channels: '/api/v1/admin/channels',
  credentials: '/api/v1/admin/credentials',
  modelmaps: '/api/v1/admin/modelmaps',
  accounts: '/api/v1/admin/accounts',
  pricing: '/api/v1/admin/pricing',
  quotas: '/api/v1/admin/quotas',
  adjustments: '/api/v1/admin/adjustments',
  usage: '/api/v1/admin/usage',
  settlements: '/api/v1/admin/settlements',
} as const satisfies Record<string, keyof paths>;

/** AdminList 是一次清单请求的结果：当页条目与信封里的分页信息。 */
export interface AdminList<T> {
  items: T[];
  meta: PageMeta;
}

/** AdminListQuery 是清单的 URL 状态：分页加资源专属的过滤条件。 */
export interface AdminListQuery {
  page: number;
  size: number;
  /** 过滤条件，键与契约里的查询参数同名；空串视为未过滤。 */
  filters: Record<string, string>;
}

/**
 * 每页条数缺省值与上限，与契约 Page / Size 参数的 default 与 maximum 一致。
 *
 * 取值在页面这一层落地：服务端对缺省与超限各有兜底，前端只是让 URL 与请求一致。
 */
export const ADMIN_PAGE_SIZE = 20;
export const ADMIN_MAX_PAGE_SIZE = 100;

/** 过滤条件的键名，与契约里的查询参数同名。 */
export const FilterMerchantID = 'merchant_id';
export const FilterAccountID = 'account_id';
export const FilterScope = 'scope';
export const FilterScopeID = 'scope_id';
export const FilterModel = 'model';
export const FilterSince = 'since';
export const FilterFrom = 'from';
export const FilterTo = 'to';

/**
 * parseAdminQuery 从 URL query 解析清单状态。
 *
 * URL 是页面状态的唯一出处（web/AGENTS.md 的表格规范）：分页与过滤都从它读，
 * 非法取值回落到默认值而不是报错 —— 分享出来的地址不该因为一个手改的页码打不开。
 */
export function parseAdminQuery(raw: URLSearchParams): AdminListQuery {
  const size = positiveInt(raw.get('size'), ADMIN_PAGE_SIZE);
  const filters: Record<string, string> = {};
  for (const [key, value] of raw.entries()) {
    if (key === 'page' || key === 'size') continue;
    if (value.trim() !== '') filters[key] = value.trim();
  }
  return {
    page: positiveInt(raw.get('page'), 1),
    size: Math.min(size, ADMIN_MAX_PAGE_SIZE),
    filters,
  };
}

/** positiveInt 读一个正整数；缺失、非法或零以下取兜底值。 */
function positiveInt(raw: string | null, fallback: number): number {
  const value = raw === null ? null : parseUnsignedInt(raw);
  if (value === null || value < 1) {
    return fallback;
  }
  return value;
}

/** adminQuerySearch 把清单状态写回 URL。 */
export function adminQuerySearch(query: AdminListQuery): URLSearchParams {
  const params = new URLSearchParams();
  params.set('page', String(query.page));
  params.set('size', String(query.size));
  for (const [key, value] of Object.entries(query.filters)) {
    if (value.trim() !== '') {
      params.set(key, value);
    }
  }
  return params;
}

/** adminQueryKey 是取数的 key：URL 变了就重新取数。 */
export function adminQueryKey(query: AdminListQuery): string {
  return adminQuerySearch(query).toString();
}

/** adminQueryString 组装请求地址：分页固定带，过滤只带本资源声明且非空的键。 */
function adminQueryString(query: AdminListQuery, filterKeys: string[]): string {
  const params = new URLSearchParams();
  params.set('page', String(query.page));
  params.set('size', String(query.size));
  for (const key of filterKeys) {
    const value = query.filters[key];
    if (value !== undefined && value.trim() !== '') {
      params.set(key, value);
    }
  }
  return `?${params.toString()}`;
}

/** list 取一次清单：分页信息取自信封，条目取 data.items。 */
async function list<T>(
  path: keyof typeof AdminPaths,
  query: AdminListQuery,
  filterKeys: string[],
): Promise<AdminList<T>> {
  const { data, meta } = await requestPage<{ items: T[] }>(
    `${AdminPaths[path]}${adminQueryString(query, filterKeys)}`,
    { method: 'GET' },
  );
  return { items: data?.items ?? [], meta };
}

/** listAdminChannels 列出全部渠道。 */
export async function listAdminChannels(query: AdminListQuery): Promise<AdminList<AdminChannel>> {
  return list<AdminChannel>('channels', query, []);
}

/** listAdminCredentials 列出全部上游凭据。 */
export async function listAdminCredentials(
  query: AdminListQuery,
): Promise<AdminList<AdminCredential>> {
  return list<AdminCredential>('credentials', query, []);
}

/** listAdminModelMaps 列出全部渠道模型映射。 */
export async function listAdminModelMaps(query: AdminListQuery): Promise<AdminList<AdminModelMap>> {
  return list<AdminModelMap>('modelmaps', query, []);
}

/** listAdminAccounts 列出全部账户。 */
export async function listAdminAccounts(query: AdminListQuery): Promise<AdminList<AdminAccount>> {
  return list<AdminAccount>('accounts', query, []);
}

/** listAdminPricing 列出定价版本，支持按商家与模型过滤。 */
export async function listAdminPricing(query: AdminListQuery): Promise<AdminList<AdminPricing>> {
  return list<AdminPricing>('pricing', query, [FilterMerchantID, FilterModel]);
}

/** listAdminQuotas 列出窗口限额，支持按范围与账户过滤。 */
export async function listAdminQuotas(query: AdminListQuery): Promise<AdminList<AdminQuota>> {
  return list<AdminQuota>('quotas', query, [FilterScope, FilterScopeID, FilterAccountID]);
}

/** listAdminAdjustments 列出调账记录，支持按账户过滤。 */
export async function listAdminAdjustments(
  query: AdminListQuery,
): Promise<AdminList<AdminAdjustment>> {
  return list<AdminAdjustment>('adjustments', query, [FilterAccountID]);
}

/** listAdminUsage 列出全平台用量流水，支持按账户与起始时刻过滤。 */
export async function listAdminUsage(query: AdminListQuery): Promise<AdminList<AdminUsageItem>> {
  return list<AdminUsageItem>('usage', query, [FilterAccountID, FilterSince]);
}

/** listAdminSettlements 列出全平台结算对账单，支持按商家与账期过滤。 */
export async function listAdminSettlements(
  query: AdminListQuery,
): Promise<AdminList<AdminSettlementItem>> {
  return list<AdminSettlementItem>('settlements', query, [FilterMerchantID, FilterFrom, FilterTo]);
}
