/**
 * 页面信封与业务码，与 docs/openapi-web.yaml 一致。
 *
 * 六字段固定出现；程序按 code 分支，message 只面向用户。
 */

/** 页面通信的统一信封。 */
export interface Envelope<T> {
  code: number;
  data: T | null;
  message: string;
  page: number | null;
  size: number | null;
  total: number | null;
}

/** 业务码；取值与契约的业务码表一致。 */
export const Code = {
  OK: 200,
  BadRequest: 400,
  Unauthorized: 401,
  Forbidden: 403,
  NotFound: 404,
  Conflict: 409,
  ChallengeExpired: 410,
  TooManyRequests: 429,
  Internal: 500,
} as const;

/** ApiError 携带业务码，页面按 code 分支而不是解析文案。 */
export class ApiError extends Error {
  readonly code: number;

  /**
   * Retry-After 秒数；服务端没给出该头时为 null。
   *
   * 限流（429）的等待时间由它推算，页面据此提示，不做自动重试。
   */
  readonly retryAfter: number | null;

  constructor(code: number, message: string, retryAfter: number | null = null) {
    super(message);
    this.name = 'ApiError';
    this.code = code;
    this.retryAfter = retryAfter;
  }
}
