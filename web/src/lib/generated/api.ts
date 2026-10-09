/**
 * 本文件由 docs/openapi-web.yaml 生成，勿手改。
 * 契约变更后执行 `npm run gen`（核对用 `npm run gen:check`）。
 */

import { apiRequest, type ApiInit } from '../client';

/**
 * 契约生成的薄请求客户端：每个 operationId 一个函数。
 *
 * 路径、参数、请求体与响应类型均来自契约；信封解包、访问令牌注入与一次性刷新
 * 由 ../client.ts 承担 —— 那是页面通信的运行时约定，不是契约内容。
 */
export const api = {
  /** 修改密码 */
  changePassword: (init: ApiInit<"/api/v1/auth/password", "put">) =>
    apiRequest("/api/v1/auth/password", "put", init),

  /** 创建密钥 */
  createUserKey: (init: ApiInit<"/api/v1/user/keys", "post">) =>
    apiRequest("/api/v1/user/keys", "post", init),

  /** 注销账号 */
  eraseAccount: (init: ApiInit<"/api/v1/auth/erase", "post">) =>
    apiRequest("/api/v1/auth/erase", "post", init),

  /** 用第三方授权码换取会话 */
  exchangeAuthOauth: (init: ApiInit<"/api/v1/auth/oauth/{provider}/exchange", "post">) =>
    apiRequest("/api/v1/auth/oauth/{provider}/exchange", "post", init),

  /** 获取一次性密码加密公钥 */
  getAuthChallenge: (init: ApiInit<"/api/v1/auth/challenge", "get">) =>
    apiRequest("/api/v1/auth/challenge", "get", init),

  /** 获取第三方授权跳转地址 */
  getAuthOauthUrl: (init: ApiInit<"/api/v1/auth/oauth/{provider}", "get">) =>
    apiRequest("/api/v1/auth/oauth/{provider}", "get", init),

  /** 查询当前会话身份 */
  getAuthSession: (init?: ApiInit<"/api/v1/auth/session", "get">) =>
    apiRequest("/api/v1/auth/session", "get", init),

  /** 账户概览 */
  getUserAccount: (init?: ApiInit<"/api/v1/user/account", "get">) =>
    apiRequest("/api/v1/user/account", "get", init),

  /** 控制台清单 */
  getUserConsole: (init?: ApiInit<"/api/v1/user/console", "get">) =>
    apiRequest("/api/v1/user/console", "get", init),

  /** 请求详情 */
  getUserRequest: (init: ApiInit<"/api/v1/user/requests/{request_id}", "get">) =>
    apiRequest("/api/v1/user/requests/{request_id}", "get", init),

  /** 请求记录聚合 */
  getUserRequestStats: (init?: ApiInit<"/api/v1/user/requests/stats", "get">) =>
    apiRequest("/api/v1/user/requests/stats", "get", init),

  /** 用量聚合 */
  getUserUsageStats: (init?: ApiInit<"/api/v1/user/usage/stats", "get">) =>
    apiRequest("/api/v1/user/usage/stats", "get", init),

  /** 列出可用登录方式 */
  listAuthProviders: (init?: ApiInit<"/api/v1/auth/providers", "get">) =>
    apiRequest("/api/v1/auth/providers", "get", init),

  /** 密钥列表 */
  listUserKeys: (init?: ApiInit<"/api/v1/user/keys", "get">) =>
    apiRequest("/api/v1/user/keys", "get", init),

  /** 可用模型 */
  listUserModels: (init?: ApiInit<"/api/v1/user/models", "get">) =>
    apiRequest("/api/v1/user/models", "get", init),

  /** 请求记录 */
  listUserRequests: (init?: ApiInit<"/api/v1/user/requests", "get">) =>
    apiRequest("/api/v1/user/requests", "get", init),

  /** 用量流水 */
  listUserUsage: (init?: ApiInit<"/api/v1/user/usage", "get">) =>
    apiRequest("/api/v1/user/usage", "get", init),

  /** 刷新会话令牌 */
  refreshSession: (init: ApiInit<"/api/v1/auth/refresh", "post">) =>
    apiRequest("/api/v1/auth/refresh", "post", init),

  /** 重置密码 */
  resetPassword: (init: ApiInit<"/api/v1/auth/reset", "post">) =>
    apiRequest("/api/v1/auth/reset", "post", init),

  /** 吊销密钥 */
  revokeUserKey: (init: ApiInit<"/api/v1/user/keys/{id}/revoke", "post">) =>
    apiRequest("/api/v1/user/keys/{id}/revoke", "post", init),

  /** 发送一次性验证码 */
  sendAuthOtp: (init: ApiInit<"/api/v1/auth/otp", "post">) =>
    apiRequest("/api/v1/auth/otp", "post", init),

  /** 登录 */
  signin: (init: ApiInit<"/api/v1/auth/signin", "post">) =>
    apiRequest("/api/v1/auth/signin", "post", init),

  /** 登出 */
  signout: (init?: ApiInit<"/api/v1/auth/signout", "post">) =>
    apiRequest("/api/v1/auth/signout", "post", init),

  /** 注册 */
  signup: (init: ApiInit<"/api/v1/auth/signup", "post">) =>
    apiRequest("/api/v1/auth/signup", "post", init),
};
