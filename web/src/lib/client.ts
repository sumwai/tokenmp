import { parseUnsignedInt } from './decimal';
import { ApiError, Code, type Envelope } from './envelope';
import { accessToken, clearSession, refreshToken, saveAccess } from './session';

/** 请求层：同源 /api/v1/*、信封解包、访问令牌注入与一次性刷新。 */

const REFRESH_PATH = '/api/v1/auth/refresh';

/** PageMeta 是列表端点的分页信息，取自信封而非业务数据。 */
export interface PageMeta {
  page: number | null;
  size: number | null;
  total: number | null;
}

/** request 发起一次页面请求，返回解包后的 data；业务码非 200 抛 ApiError。 */
export async function request<T>(
  path: string,
  init: RequestInit & { skipRefresh?: boolean } = {},
): Promise<T> {
  return unwrap(await sendWithRefresh<T>(path, init));
}

/**
 * requestEnvelope 返回完整信封。
 *
 * 列表端点要按契约的信封字段分页（page / size / total），只取 data 会把分页信息丢掉，
 * 页面就得自行推算 —— 那正是 web/AGENTS.md 禁止的「自造分页形态」。
 */
export async function requestEnvelope<T>(
  path: string,
  init: RequestInit & { skipRefresh?: boolean } = {},
): Promise<Envelope<T>> {
  return (await sendWithRefresh<T>(path, init)).env;
}

/**
 * requestPage 取列表端点：除业务数据外还要信封的 page / size / total。
 *
 * 分页由服务端决定，页面不能用 items.length 推断总条数。
 */
export async function requestPage<T>(
  path: string,
  init: RequestInit & { skipRefresh?: boolean } = {},
): Promise<{ data: T | null; meta: PageMeta }> {
  const sent = await sendWithRefresh<T>(path, init);
  return {
    // 复用 unwrap：业务码非 200 时同样抛 ApiError，列表页才能进错误态。
    data: unwrap(sent),
    meta: { page: sent.env.page, size: sent.env.size, total: sent.env.total },
  };
}

/** sendWithRefresh 发一次请求，并在会话过期时用刷新令牌换发一次后重放原请求。 */
async function sendWithRefresh<T>(
  path: string,
  init: RequestInit & { skipRefresh?: boolean },
): Promise<Sent<T>> {
  const sent = await send<T>(path, init);

  // 会话过期：先尝试用刷新令牌换发一次，成功则重放原请求，失败清会话回登录。
  // 刷新端点自身不重放，避免递归。
  if (sent.env.code === Code.Unauthorized && !init.skipRefresh && path !== REFRESH_PATH) {
    if (await refreshOnce()) {
      return send<T>(path, init);
    }
    clearSession();
  }
  return sent;
}

/** Sent 是一次请求的结果：信封，外加响应头里可用的重试提示。 */
interface Sent<T> {
  env: Envelope<T>;
  retryAfter: number | null;
}

/** send 执行 fetch 并解析信封；非 JSON 响应（如反代错误页）按 500 处理。 */
async function send<T>(path: string, init: RequestInit): Promise<Sent<T>> {
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
  const retryAfter = readRetryAfter(res);
  try {
    return { env: (await res.json()) as Envelope<T>, retryAfter };
  } catch {
    throw new ApiError(Code.Internal, '服务端错误');
  }
}

/**
 * readRetryAfter 取响应头里的 Retry-After 秒数。
 *
 * 只认秒数形态：HTTP-date 形态的取值不落成等待时长，交回页面按「稍后重试」提示，
 * 避免把一个解析不了的头显示成一个具体秒数。
 */
function readRetryAfter(res: Response): number | null {
  const raw = res.headers?.get('Retry-After');
  if (raw === undefined || raw === null) {
    return null;
  }
  return parseUnsignedInt(raw);
}

/** unwrap 按业务码返回 data 或抛错。 */
function unwrap<T>(sent: Sent<T>): T {
  if (sent.env.code !== Code.OK) {
    throw new ApiError(sent.env.code, sent.env.message, sent.retryAfter);
  }
  return sent.env.data as T;
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
