/**
 * 本文件由 docs/openapi-web.yaml 生成，勿手改。
 * 契约变更后执行 `npm run gen`（核对用 `npm run gen:check`）。
 */

/** 业务码：程序按它分支，不解析文案。 */
export const Code = {
  OK: 200,
  BadRequest: 400,
  Unauthorized: 401,
  Forbidden: 403,
  NotFound: 404,
  Conflict: 409,
  ChallengeExpired: 410,
  TooManyRequests: 429,
  InternalError: 500,
} as const;

/** 业务码取值集合。 */
export type BusinessCode = (typeof Code)[keyof typeof Code];
