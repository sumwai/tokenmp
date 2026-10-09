import type { Payload } from './client';
import { api } from './generated/api';

/**
 * 客户端指纹与密码类字段的公钥加密，与 docs/openapi-web.yaml 的
 * challenge 流程对齐：RSA-OAEP（SHA-256）、SPKI DER base64 公钥。
 */

/** 指纹是公钥的索引而不是凭据，可持久化在 localStorage。 */
const FINGERPRINT_KEY = 'tokenmp_fingerprint';

/** challenge 端点的响应数据，取自契约生成物。 */
export type ChallengeData = Payload<'/api/v1/auth/challenge', 'get'>;

/** fingerprint 读取或生成本地指纹。 */
export function fingerprint(): string {
  let value = localStorage.getItem(FINGERPRINT_KEY);
  if (!value) {
    value = crypto.randomUUID();
    localStorage.setItem(FINGERPRINT_KEY, value);
  }
  return value;
}

/** fetchChallenge 取一枚一次性公钥。 */
export async function fetchChallenge(): Promise<ChallengeData> {
  const data = await api.getAuthChallenge({
    params: { query: { fingerprint: fingerprint() } },
    skipRefresh: true,
  });
  if (!data) {
    throw new Error('challenge 无响应数据');
  }
  return data;
}

/** encryptSecret 用一次性公钥加密明文，返回 base64 密文。 */
export async function encryptSecret(plaintext: string, spkiBase64: string): Promise<string> {
  const der = Uint8Array.from(atob(spkiBase64), (c) => c.charCodeAt(0));
  const key = await crypto.subtle.importKey(
    'spki',
    der,
    { name: 'RSA-OAEP', hash: 'SHA-256' },
    false,
    ['encrypt'],
  );
  const cipher = await crypto.subtle.encrypt(
    { name: 'RSA-OAEP' },
    key,
    new TextEncoder().encode(plaintext),
  );
  return bufferToBase64(cipher);
}

/** ArrayBuffer → base64（大数组按块转换，避免展开参数栈溢出）。 */
function bufferToBase64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let binary = '';
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
}
