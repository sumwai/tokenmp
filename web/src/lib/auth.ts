import { request } from './client';
import { ApiError, Code } from './envelope';
import { encryptSecret, fetchChallenge, fingerprint } from './crypto';
import { saveOauthProvider } from './session';

/** 认证端点的请求与响应类型，与 docs/openapi-web.yaml 对齐。 */

/** SessionUser 是会话返回的身份。 */
export interface SessionUser {
  id: number;
  username: string;
  role: string;
  identities: string[];
}

/** SessionTokens 是登录与注册的签发结果。 */
export interface SessionTokens {
  access_token: string;
  refresh_token: string;
  expires_in: number;
  user: SessionUser;
}

/** AuthProvider 是一个可用的第三方登录方式。 */
export interface AuthProvider {
  id: string;
  name: string;
}

/** OAuthAuthorizeData 是第三方授权跳转地址。 */
export interface OAuthAuthorizeData {
  authorize_url: string;
  state: string;
}

/**
 * withSecret 承担「取公钥 → 加密 → 提交 → 410 重试一次」的通用流程。
 *
 * 一次性私钥取出即销毁，密钥过期（code=410）时重新取公钥重试一轮；
 * 其它错误原样上抛。plain 是明文密码，只在本函数栈内存在。
 */
async function withSecret<T>(
  path: string,
  plain: string,
  build: (cipher: string, fp: string) => unknown,
  method = 'POST',
): Promise<T> {
  let lastError: unknown;
  for (let attempt = 0; attempt < 2; attempt++) {
    const challenge = await fetchChallenge();
    const cipher = await encryptSecret(plain, challenge.public_key);
    try {
      const data = await request<T>(path, {
        method,
        body: JSON.stringify(build(cipher, fingerprint())),
        skipRefresh: true,
      });
      if (!data) {
        throw new ApiError(Code.Internal, '服务端错误');
      }
      return data;
    } catch (error) {
      lastError = error;
      if (!(error instanceof ApiError) || error.code !== Code.ChallengeExpired) {
        throw error;
      }
    }
  }
  throw lastError;
}

/** signin 登录。 */
export async function signin(identifier: string, password: string): Promise<SessionTokens> {
  const data = await withSecret<SessionTokens>(
    '/api/v1/auth/signin',
    password,
    (cipher, fp) => ({ username: identifier, password: cipher, fingerprint: fp }),
  );
  return data;
}

/** signup 注册。 */
export async function signup(
  email: string,
  username: string,
  password: string,
): Promise<SessionTokens> {
  return withSecret<SessionTokens>(
    '/api/v1/auth/signup',
    password,
    (cipher, fp) => ({ email, username, password: cipher, fingerprint: fp }),
  );
}

/** resetPassword 用 OTP 重置密码。 */
export async function resetPassword(
  email: string,
  otp: string,
  password: string,
): Promise<void> {
  await withSecret<null>(
    '/api/v1/auth/reset',
    password,
    (cipher, fp) => ({ email, otp, password: cipher, fingerprint: fp }),
  );
}

/** changePassword 修改密码：新旧密码各取一枚一次性公钥，任一失效则同轮重取。 */
export async function changePassword(
  currentPassword: string,
  newPassword: string,
): Promise<void> {
  const fp = fingerprint();
  for (let attempt = 0; attempt < 2; attempt++) {
    const currentChallenge = await fetchChallenge();
    const currentCipher = await encryptSecret(currentPassword, currentChallenge.public_key);
    const newChallenge = await fetchChallenge();
    const newCipher = await encryptSecret(newPassword, newChallenge.public_key);
    try {
      await request<null>('/api/v1/auth/password', {
        method: 'PUT',
        body: JSON.stringify({
          current_password: currentCipher,
          current_fingerprint: fp,
          new_password: newCipher,
          new_fingerprint: fp,
        }),
        skipRefresh: true,
      });
      return;
    } catch (error) {
      if (!(error instanceof ApiError) || error.code !== Code.ChallengeExpired) {
        throw error;
      }
      // 密钥过期：两把一起重取，保持同轮，不在新旧之间留下时差。
    }
  }
  throw new ApiError(Code.ChallengeExpired, '加密公钥已失效，请重新获取');
}

/** session 查询当前身份。 */
export async function session(): Promise<SessionUser> {
  const data = await request<SessionUser>('/api/v1/auth/session', { method: 'GET' });
  if (!data) {
    throw new ApiError(Code.Unauthorized, '登录状态已失效');
  }
  return data;
}

/** signout 登出（幂等）。 */
export async function signout(): Promise<void> {
  await request<null>('/api/v1/auth/signout', { method: 'POST' });
}

/** sendOtp 发送一次性验证码（purpose: reset | erase）。 */
export async function sendOtp(email: string, purpose: 'reset' | 'erase'): Promise<void> {
  await request<null>('/api/v1/auth/otp', {
    method: 'POST',
    body: JSON.stringify({ email, purpose }),
    skipRefresh: true,
  });
}

/** listProviders 列出已配置的第三方登录方式。 */
export async function listProviders(): Promise<AuthProvider[]> {
  const data = await request<{ items: AuthProvider[] }>('/api/v1/auth/providers', {
    method: 'GET',
    skipRefresh: true,
  });
  return data?.items ?? [];
}

/** oauthAuthorize 取第三方授权跳转地址；发起时记录提供方供回调页使用。 */
export async function oauthAuthorize(provider: string): Promise<OAuthAuthorizeData> {
  const data = await request<OAuthAuthorizeData>(`/api/v1/auth/oauth/${provider}`, {
    method: 'GET',
    skipRefresh: true,
  });
  if (!data) {
    throw new ApiError(Code.NotFound, '登录方式未启用');
  }
  saveOauthProvider(provider);
  return data;
}

/** oauthExchange 用授权码换取会话（回调页调用）。 */
export async function oauthExchange(
  provider: string,
  code: string,
  state: string,
): Promise<SessionTokens> {
  const data = await request<SessionTokens>(`/api/v1/auth/oauth/${provider}/exchange`, {
    method: 'POST',
    body: JSON.stringify({ code, state }),
    skipRefresh: true,
  });
  if (!data) {
    throw new ApiError(Code.Internal, '服务端错误');
  }
  return data;
}
