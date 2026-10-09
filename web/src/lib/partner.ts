import { requestPage, type PageMeta } from './client';
import { api } from './generated/api';
import type { components } from './generated/schema';
import { accountsQueryString, type AccountsQuery } from './partnerUrl';
import { usageStatsQuery, type UsageStatsItem } from './usage';
import type { UsageQuery } from './usageUrl';

/**
 * 商家域端点（`/api/v1/partner/*`）的类型与调用。
 *
 * 类型全部来自契约生成物（./generated/schema）：响应类型不手写，契约变更后
 * `npm run gen` 重新生成即可。作用域由会话推导出的商家给定，调用方不传商家参数。
 *
 * 名下调用量的聚合复用用量页的参数映射（./usage 的 usageStatsQuery）：两条入口在同一
 * 区间、同一维度上发出的请求因此逐字相同，口径不会各算一套。
 */

/** 商家域集合与聚合端点的路径，与契约一致。 */
export const PartnerChannelsPath = '/api/v1/partner/channels';
export const PartnerCredentialsPath = '/api/v1/partner/credentials';
export const PartnerUsageStatsPath = '/api/v1/partner/usage/stats';

/** PartnerChannel 是一条上游渠道；不含商家标识与渠道级扩展配置。 */
export type PartnerChannel = components['schemas']['PartnerChannel'];

/** PartnerCredential 是一份上游凭据；只有前缀，明文从不出现。 */
export type PartnerCredential = components['schemas']['PartnerCredential'];

/** CreatedPartnerCredential 是凭据创建响应：在视图上追加一次性明文。 */
export type CreatedPartnerCredential = components['schemas']['CreatedPartnerCredential'];

/** CreatePartnerChannelInput 是登记渠道的请求体。 */
export type CreatePartnerChannelInput = components['schemas']['CreatePartnerChannelRequest'];

/** CreatePartnerCredentialInput 是登记凭据的请求体。 */
export type CreatePartnerCredentialInput = components['schemas']['CreatePartnerCredentialRequest'];

export type { UsageStatsItem };

/** listChannels 列出本商家的上游渠道；分页信息取自信封。 */
export async function listChannels(
  query: AccountsQuery,
): Promise<{ items: PartnerChannel[]; meta: PageMeta }> {
  const { data, meta } = await requestPage<components['schemas']['PageOfPartnerChannel']>(
    `${PartnerChannelsPath}${accountsQueryString(query)}`,
    { method: 'GET' },
  );
  return { items: data?.items ?? [], meta };
}

/** createChannel 登记一条上游渠道；归属由服务端按会话推导。 */
export async function createChannel(input: CreatePartnerChannelInput): Promise<PartnerChannel> {
  return api.createPartnerChannel({ body: input });
}

/** setChannelEnabled 启用或停用一条属于本商家的渠道。 */
export async function setChannelEnabled(id: number, enabled: boolean): Promise<void> {
  if (enabled) {
    await api.enablePartnerChannel({ params: { path: { id } } });
    return;
  }
  await api.disablePartnerChannel({ params: { path: { id } } });
}

/** listCredentials 列出本商家的上游凭据；只返回前缀。 */
export async function listCredentials(
  query: AccountsQuery,
): Promise<{ items: PartnerCredential[]; meta: PageMeta }> {
  const { data, meta } = await requestPage<components['schemas']['PageOfPartnerCredential']>(
    `${PartnerCredentialsPath}${accountsQueryString(query)}`,
    { method: 'GET' },
  );
  return { items: data?.items ?? [], meta };
}

/** createCredential 登记一份上游凭据，返回含一次性明文。 */
export async function createCredential(
  input: CreatePartnerCredentialInput,
): Promise<CreatedPartnerCredential> {
  return api.createPartnerCredential({ body: input });
}

/** setCredentialEnabled 启用或停用一份属于本商家的凭据。 */
export async function setCredentialEnabled(id: number, enabled: boolean): Promise<void> {
  if (enabled) {
    await api.enablePartnerCredential({ params: { path: { id } } });
    return;
  }
  await api.disablePartnerCredential({ params: { path: { id } } });
}

/** loadUsageStats 取本商家名下渠道的用量聚合；端点不分页，一次返回全部维度分组。 */
export async function loadUsageStats(query: UsageQuery): Promise<UsageStatsItem[]> {
  const data = await api.getPartnerUsageStats({ params: { query: usageStatsQuery(query) } });
  return data?.items ?? [];
}
