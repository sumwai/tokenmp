import { request } from './client';
import { parseUnsignedInt } from './decimal';
import { ApiError, Code } from './envelope';

/**
 * `/api/v1/user/account` 的类型与取数。
 *
 * 字段与 docs/openapi-web.yaml 的 AccountSummary 对齐：契约由 #112 定稿、端点由 #129
 * 落地，这里的类型是页面侧的消费面。账户作用域由会话推导，请求不携带账户 id。
 */

/** AccountRef 是账户本身对外可见的部分。 */
export interface AccountRef {
  id: number;
  code: string;
}

/** AccountBucket 是一条可用包存量；同单位的包各自成条。 */
export interface AccountBucket {
  unit: string;
  remaining: string;
  fallback: string;
  expires_at: string | null;
}

/** AccountQuota 是一条窗口限额及其当前窗口的已用量。 */
export interface AccountQuota {
  scope: string;
  metric: string;
  window_kind: string;
  period: string;
  used: string;
  limit: string;
  action: string;
  resets_at: string | null;
}

/** AccountRecentUsage 是一条流水摘要。 */
export interface AccountRecentUsage {
  model: string;
  created_at: string;
  charged_amount: string;
}

/** AccountSummary 是账户概览的响应数据。 */
export interface AccountSummary {
  account: AccountRef;
  buckets: AccountBucket[];
  quotas: AccountQuota[];
  recent: AccountRecentUsage[];
}

/** DEFAULT_RECENT 是契约里 recent 的缺省条数。 */
export const DEFAULT_RECENT = 10;

/** MAX_RECENT 是契约里 recent 的上限；更大的取值截到上限，与服务端解析同口径。 */
export const MAX_RECENT = 100;

/** QUOTA_SCALE 是限额数值的展示小数位：指标只有 token 数与请求次数，都是整数口径。 */
export const QUOTA_SCALE = 0;

/**
 * RECENT_SCALE 是流水应扣量的展示小数位。
 *
 * 契约里 charged_amount 不带单位（单位由定价配置决定），补一个 currency 口径的小数位
 * 会把 token 计费的行显示错，因此只加千分位、不补小数位。
 */
export const RECENT_SCALE = 0;

/**
 * normalizeRecent 把 URL 里的 recent 收敛到契约取值：缺省取默认条数，非法值（空、
 * 负数、非整数）回落默认条数，超上限截到上限。0 是合法取值，表示不取流水。
 */
export function normalizeRecent(raw: string | null): number {
  if (raw === null) {
    return DEFAULT_RECENT;
  }
  const value = parseUnsignedInt(raw);
  if (value === null) {
    return DEFAULT_RECENT;
  }
  return value > MAX_RECENT ? MAX_RECENT : value;
}

/** fetchAccount 取当前账户的摘要；recent 由 normalizeRecent 收敛后传入。 */
export async function fetchAccount(recent: number): Promise<AccountSummary> {
  const data = await request<AccountSummary>(`/api/v1/user/account?recent=${recent}`, {
    method: 'GET',
  });
  if (!data) {
    throw new ApiError(Code.Internal, '服务端错误');
  }
  return data;
}
