import type { MaybeOptionalInit } from 'openapi-fetch';

import { parseUnsignedInt } from './decimal';
import { ApiError, Code, type Envelope } from './envelope';
import type { components, paths } from './generated/schema';
import { accessToken, clearSession, refreshToken, saveAccess } from './session';

/**
 * 请求层：同源 /api/v1/*、信封解包、访问令牌注入与一次性刷新。
 *
 * 类型全部来自契约生成物（./generated/schema）：路径、参数、请求体与响应载荷都由
 * 契约决定，本文件只承担契约表达不了的运行时语义。
 */

/** 刷新端点自身不重放：避免刷新失败时递归。 */
const REFRESH_PATH = '/api/v1/auth/refresh';

/** PageMeta 是列表端点的分页信息，取自信封而非业务数据。 */
export interface PageMeta {
  page: number | null;
  size: number | null;
  total: number | null;
}

/** request 发起一次页面请求（路径形态），返回解包后的 data；业务码非 200 抛 ApiError。 */
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
  const { method, skipRefresh, ...rest } = init;
  const verb = method ?? 'GET';
  const sent = await send<T>(path, verb, rest);

  // 会话过期：先尝试用刷新令牌换发一次，成功则重放原请求，失败清会话回登录。
  if (sent.env.code === Code.Unauthorized && !skipRefresh && path !== REFRESH_PATH) {
    if (await refreshOnce()) {
      return send<T>(path, verb, rest);
    }
    clearSession();
  }
  return sent;
}

type PathKey = keyof paths;
type Method = 'get' | 'post' | 'put' | 'delete';

/** 契约声明了该方法的路径才有取值：未声明的方法在生成类型里是 never。 */
type MethodKey<P extends PathKey> = {
  [M in Method]: undefined extends paths[P][M] ? never : M;
}[Method];

type OperationOf<P extends PathKey, M extends MethodKey<P>> = paths[P][M];

/** 成功响应：契约里写作 allOf: [Envelope, { data: … }]。 */
type OkBody<P extends PathKey, M extends MethodKey<P>> = OperationOf<P, M> extends {
  responses: { 200: { content: { 'application/json': infer S } } };
}
  ? S
  : never;

/** 成功响应里 data 的类型；契约未声明 data 的端点（如登出）得到 unknown。 */
export type Payload<P extends PathKey, M extends MethodKey<P>> = OkBody<P, M> extends {
  data?: infer D;
}
  ? Exclude<D, undefined>
  : never;

/** 契约的参数对象：路径参数替换路径占位符，查询参数追加为编码后的键值对。 */
interface Params {
  query?: Record<string, unknown>;
  path?: Record<string, unknown>;
}

/** 发送选项：请求体已是 fetch 可用的形态（对象请求体由契约形态的入口先序列化）。 */
type SendInit = Omit<RequestInit, 'method'>;

/** 请求选项：契约声明的参数与请求体，加上本层特有的「跳过刷新」。 */
export type ApiInit<P extends PathKey, M extends MethodKey<P>> = MaybeOptionalInit<paths[P], M> & {
  /** 免登录端点（取公钥、登录、注册、OTP、第三方登录、刷新）不参与 401 重放。 */
  skipRefresh?: boolean;
};

/** apiRequest 发起一次页面请求，返回信封解包后的 data；业务码非 200 抛 ApiError。 */
export async function apiRequest<P extends PathKey, M extends MethodKey<P>>(
  path: P,
  method: M,
  init?: ApiInit<P, M>,
): Promise<Payload<P, M>> {
  // 契约类型里还带着 openapi-fetch 的序列化选项：本层只消费参数与跳过刷新，其余转交 fetch。
  const { params, skipRefresh, ...rest } = (init ?? {}) as Omit<ApiInit<P, M>, 'params'> & {
    params?: Params;
  };
  const url = buildURL(path, params);
  // 契约形态的请求体是对象：这里序列化一次，send 只负责原样交给 fetch。
  // 请求体是否存在由契约的 operation 类型决定，取值处按 unknown 读。
  const rawBody = (rest as { body?: unknown }).body;
  const body = rawBody === undefined ? undefined : JSON.stringify(rawBody);
  const sent = await send<Payload<P, M>>(url, method, { ...rest, body });

  // 会话过期：先尝试用刷新令牌换发一次，成功则重放原请求，失败清会话回登录。
  if (sent.env.code === Code.Unauthorized && !skipRefresh && path !== REFRESH_PATH) {
    if (await refreshOnce()) {
      return unwrap(await send<Payload<P, M>>(url, method, { ...rest, body }));
    }
    clearSession();
  }
  return unwrap(sent);
}

/** Sent 是一次请求的结果：信封，外加响应头里可用的重试提示。 */
interface Sent<T> {
  env: Envelope<T>;
  retryAfter: number | null;
}

/** buildURL 组装请求地址：路径参数替换占位符，查询参数追加为编码后的键值对。 */
function buildURL(path: string, params?: Params): string {
  let url = path;
  for (const [key, value] of Object.entries(params?.path ?? {})) {
    url = url.replace(`{${key}}`, encodeURIComponent(String(value)));
  }
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params?.query ?? {})) {
    if (value !== undefined && value !== null) {
      query.set(key, String(value));
    }
  }
  const encoded = query.toString();
  return encoded === '' ? url : `${url}?${encoded}`;
}

/** send 执行 fetch 并解析信封；非 JSON 响应（如反代错误页）按 500 处理。 */
async function send<T>(url: string, method: string, init: SendInit): Promise<Sent<T>> {
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
    res = await fetch(url, {
      ...init,
      method: method.toUpperCase(),
      headers,
      body: init.body ?? undefined,
    });
  } catch {
    throw new ApiError(Code.InternalError, '网络异常，请稍后重试');
  }
  const retryAfter = readRetryAfter(res);
  try {
    return { env: (await res.json()) as Envelope<T>, retryAfter };
  } catch {
    throw new ApiError(Code.InternalError, '服务端错误');
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
  const token = refreshToken();
  if (!token) {
    return false;
  }
  const body: components['schemas']['RefreshRequest'] = { refresh_token: token };
  try {
    const res = await fetch(REFRESH_PATH, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify(body),
      // 刷新失败不能再次触发刷新：这里不走 apiRequest 的重放分支。
    });
    const env = (await res.json()) as Envelope<Payload<'/api/v1/auth/refresh', 'post'>>;
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
