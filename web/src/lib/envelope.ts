import { Code, type BusinessCode } from './generated/codes';
import type { components } from './generated/schema';

/**
 * 页面信封与业务码。
 *
 * 字段与业务码都取自契约生成物（./generated），本文件只把它们组成页面用的形状：
 * 信封六字段固定出现，data 收窄为调用点声明的载荷类型；程序按 code 分支，
 * message 只面向用户。
 */

/** 页面通信的统一信封。 */
export type Envelope<T> = Omit<components['schemas']['Envelope'], 'data'> & { data: T | null };

export { Code, type BusinessCode };

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
