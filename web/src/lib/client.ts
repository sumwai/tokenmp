import { ApiError, Code, type Envelope } from './envelope';
import { accessToken, clearSession, refreshToken, saveAccess } from './session';

/** 请求层：同源 /api/v1/*、信封解包、访问令牌注入与一次性刷新。 */

const REFRESH_PATH = '/api/v1/auth/refresh';

/** request 发起一次页面请求，返回解包后的 data；业务码非 200 抛 ApiError。 */
export async function request<T>(
  path: string,
  init: RequestInit & { skipRefresh?: boolean } = {},
): Promise<T> {
  const env = await send<T>(path, init);

  // 会话过期：先尝试用刷新令牌换发一次，成功则重放原请求，失败清会话回登录。
  // 刷新端点自身不重放，避免递归。
  if (env.code === Code.Unauthorized && !init.skipRefresh && path !== REFRESH_PATH) {
    if (await refreshOnce()) {
      return unwrap(await send<T>(path, init));
    }
    clearSession();
  }
  return unwrap(env);
}

/** send 执行 fetch 并解析信封；非 JSON 响应（如反代错误页）按 500 处理。 */
async function send<T>(path: string, init: RequestInit): Promise<Envelope<T>> {
  const headers = new Headers(init.headers);
  headers.set('Accept', 'application/json');
  if (init.body !== undefined && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }
  const token = accessToken();
  if (token) {
    headers.set('Authorization', `Bearer ${token}`);
  }

  let res: Response;
  try {
    res = await fetch(path, { ...init, headers });
  } catch {
    throw new ApiError(Code.Internal, '网络异常，请稍后重试');
  }
  try {
    return (await res.json()) as Envelope<T>;
  } catch {
    throw new ApiError(Code.Internal, '服务端错误');
  }
}

/** unwrap 按业务码返回 data 或抛错。 */
function unwrap<T>(env: Envelope<T>): T {
  if (env.code !== Code.OK) {
    throw new ApiError(env.code, env.message);
  }
  return env.data as T;
}

/** refreshOnce 用刷新令牌换发访问令牌；返回是否成功。 */
async function refreshOnce(): Promise<boolean> {
  const rt = refreshToken();
  if (!rt) {
    return false;
  }
  try {
    const res = await fetch(REFRESH_PATH, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify({ refresh_token: rt }),
      // 刷新失败不能再次触发刷新，跳过 request 的刷新分支。
    });
    const env = (await res.json()) as Envelope<{ access_token: string }>;
    if (env.code === Code.OK && env.data) {
      saveAccess(env.data.access_token);
      return true;
    }
  } catch {
    // 网络或解析失败一律视为刷新失败，由调用方清会话。
  }
  clearSession();
  return false;
}
