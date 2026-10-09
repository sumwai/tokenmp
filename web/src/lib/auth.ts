import type { Payload } from './client';
import { ApiError, Code } from './envelope';
import { api } from './generated/api';
import type { components } from './generated/schema';
import { encryptSecret, fetchChallenge, fingerprint } from './crypto';
import { saveOauthProvider } from './session';

/**
 * 认证端点的调用封装。
 *
 * 请求与响应类型一律取自契约生成物（./generated），本文件只承担流程：
 * 「取公钥 → 加密 → 提交 → 410 重试一次」，以及把结果收窄成页面要的形状。
 */

/** SessionUser 是会话返回的身份。 */
export type SessionUser = components['schemas']['SessionUser'];

/** SessionTokens 是登录与注册的签发结果。 */
export type SessionTokens = components['schemas']['SessionTokens'];

/** AuthProvider 是一个可用的第三方登录方式。 */
export type AuthProvider = components['schemas']['AuthProvider'];

/** OAuthAuthorizeData 是第三方授权跳转地址，取自契约里该端点的成功响应。 */
export type OAuthAuthorizeData = Payload<'/api/v1/auth/oauth/{provider}', 'get'>;

/**
 * withSecret 承担「取公钥 → 加密 → 提交 → 410 重试一次」的通用流程。
 *
 * 一次性私钥取出即销毁，密钥过期（code=410）时重新取公钥重试一轮；
 * 其它错误原样上抛。plain 是明文密码，只在本函数栈内存在。
 */
async function withSecret<T>(
  plain: string,
  call: (cipher: string, fp: string) => Promise<T>,
): Promise<T> {
  let lastError: unknown;
  for (let attempt = 0; attempt < 2; attempt++) {
    const challenge = await fetchChallenge();
    const cipher = await encryptSecret(plain, challenge.public_key);
    try {
      const data = await call(cipher, fingerprint());
      if (!data) {
        throw new ApiError(Code.InternalError, '服务端错误');
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
  return withSecret(password, (cipher, fp) =>
    api.signin({
      body: { username: identifier, password: cipher, fingerprint: fp },
      skipRefresh: true,
    }),
  );
}

/** signup 注册。 */
export async function signup(
  email: string,
  username: string,
  password: string,
): Promise<SessionTokens> {
  return withSecret(password, (cipher, fp) =>
    api.signup({
      body: { email, username, password: cipher, fingerprint: fp },
      skipRefresh: true,
    }),
  );
}

/** resetPassword 用 OTP 重置密码。 */
export async function resetPassword(
  email: string,
  otp: string,
  password: string,
): Promise<void> {
  await withSecret(password, (cipher, fp) =>
    api.resetPassword({
      body: { email, otp, password: cipher, fingerprint: fp },
      skipRefresh: true,
    }),
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
      await api.changePassword({
        body: {
          current_password: currentCipher,
          current_fingerprint: fp,
          new_password: newCipher,
          new_fingerprint: fp,
        },
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
  const data = await api.getAuthSession();
  if (!data) {
    throw new ApiError(Code.Unauthorized, '登录状态已失效');
  }
  return data;
}

/** signout 登出（幂等）。 */
export async function signout(): Promise<void> {
  await api.signout();
}

/** sendOtp 发送一次性验证码（purpose: reset | erase）。 */
export async function sendOtp(email: string, purpose: 'reset' | 'erase'): Promise<void> {
  await api.sendAuthOtp({
    body: { email, purpose },
    skipRefresh: true,
  });
}

/** listProviders 列出已配置的第三方登录方式。 */
export async function listProviders(): Promise<AuthProvider[]> {
  const data = await api.listAuthProviders({ skipRefresh: true });
  return data?.items ?? [];
}

/** oauthAuthorize 取第三方授权跳转地址；发起时记录提供方供回调页使用。 */
export async function oauthAuthorize(provider: string): Promise<OAuthAuthorizeData> {
  const data = await api.getAuthOauthUrl({
    params: { path: { provider } },
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
  const data = await api.exchangeAuthOauth({
    params: { path: { provider } },
    body: { code, state },
    skipRefresh: true,
  });
  if (!data) {
    throw new ApiError(Code.InternalError, '服务端错误');
  }
  return data;
}
