import { request, requestPage, type PageMeta } from './client';
import { keysQueryString, type KeysQuery } from './keysUrl';

/**
 * 用户密钥端点（`/api/v1/user/keys`）的类型与调用。
 *
 * 字段与 docs/openapi-web.yaml 的 ApiKey / CreateApiKeyRequest / CreatedApiKey 一致。
 * 契约要求响应类型由生成物提供（web/AGENTS.md）；生成管道（#135）尚未落地，
 * 因此类型先集中声明在本文件这一处，管道落地后改为消费生成物，调用处不动。
 */

/** 密钥集合与吊销端点的路径，与契约一致。 */
export const KeysPath = '/api/v1/user/keys';

/** ApiKey 是一把密钥的可见部分；哈希与明文从不出现。 */
export interface ApiKey {
  id: number;
  name: string;
  /** 明文前缀，仅供展示与检索。 */
  key_prefix: string;
  /** 吊销后为 false。 */
  enabled: boolean;
  expires_at: string | null;
  last_used_at: string | null;
}

/** CreatedApiKey 是创建响应：在密钥可见部分上追加一次性明文。 */
export interface CreatedApiKey extends ApiKey {
  /** 明文，只在此响应出现，服务端不落库、不再返回。 */
  secret: string;
}

/** CreateApiKeyInput 是创建请求体；expires_at 为 null 表示不过期。 */
export interface CreateApiKeyInput {
  name: string;
  expires_at: string | null;
}

/** listKeys 列出当前账户的密钥；分页信息取自信封。 */
export async function listKeys(
  query: KeysQuery,
): Promise<{ items: ApiKey[]; meta: PageMeta }> {
  const { data, meta } = await requestPage<{ items: ApiKey[] }>(
    `${KeysPath}${keysQueryString(query)}`,
    { method: 'GET' },
  );
  return { items: data?.items ?? [], meta };
}

/** createKey 签发一把密钥，返回含一次性明文。 */
export async function createKey(input: CreateApiKeyInput): Promise<CreatedApiKey> {
  return request<CreatedApiKey>(KeysPath, {
    method: 'POST',
    body: JSON.stringify(input),
  });
}

/** revokeKey 吊销一把密钥；已吊销的密钥再次吊销同样返回成功。 */
export async function revokeKey(id: number): Promise<void> {
  await request<null>(`${KeysPath}/${id}/revoke`, { method: 'POST' });
}
