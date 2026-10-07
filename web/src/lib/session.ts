/**
 * 会话令牌存取。
 *
 * 存 sessionStorage 而不是 localStorage：标签页关闭即失效，
 * 不在浏览器长期驻留明文令牌（web/AGENTS.md 的底线）。
 * httpOnly Cookie 形态需要服务端配合下发与 CSRF 防护，属后续演进项。
 */
const ACCESS_KEY = 'tokenmp_access_token';
const REFRESH_KEY = 'tokenmp_refresh_token';

/** saveSession 落下登录签发的一对令牌。 */
export function saveSession(accessToken: string, refreshToken: string): void {
  sessionStorage.setItem(ACCESS_KEY, accessToken);
  sessionStorage.setItem(REFRESH_KEY, refreshToken);
}

/** saveAccess 只更新访问令牌（刷新流程）。 */
export function saveAccess(accessToken: string): void {
  sessionStorage.setItem(ACCESS_KEY, accessToken);
}

/** accessToken 读当前访问令牌。 */
export function accessToken(): string | null {
  return sessionStorage.getItem(ACCESS_KEY);
}

/** refreshToken 读当前刷新令牌。 */
export function refreshToken(): string | null {
  return sessionStorage.getItem(REFRESH_KEY);
}

/** clearSession 清空会话，用于登出与刷新失败。 */
export function clearSession(): void {
  sessionStorage.removeItem(ACCESS_KEY);
  sessionStorage.removeItem(REFRESH_KEY);
}
