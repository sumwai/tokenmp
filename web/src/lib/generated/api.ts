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

  /** 新建渠道 */
  createAdminChannel: (init: ApiInit<"/api/v1/admin/channels", "post">) =>
    apiRequest("/api/v1/admin/channels", "post", init),

  /** 写入上游凭据 */
  createAdminCredential: (init: ApiInit<"/api/v1/admin/credentials", "post">) =>
    apiRequest("/api/v1/admin/credentials", "post", init),

  /** 新建商家 */
  createAdminMerchant: (init: ApiInit<"/api/v1/admin/merchants", "post">) =>
    apiRequest("/api/v1/admin/merchants", "post", init),

  /** 登记上游渠道 */
  createPartnerChannel: (init: ApiInit<"/api/v1/partner/channels", "post">) =>
    apiRequest("/api/v1/partner/channels", "post", init),

  /** 登记上游凭据 */
  createPartnerCredential: (init: ApiInit<"/api/v1/partner/credentials", "post">) =>
    apiRequest("/api/v1/partner/credentials", "post", init),

  /** 创建密钥 */
  createUserKey: (init: ApiInit<"/api/v1/user/keys", "post">) =>
    apiRequest("/api/v1/user/keys", "post", init),

  /** 下单购买 */
  createUserOrder: (init: ApiInit<"/api/v1/user/orders", "post">) =>
    apiRequest("/api/v1/user/orders", "post", init),

  /** 停用渠道 */
  disableAdminChannel: (init: ApiInit<"/api/v1/admin/channels/{id}/disable", "post">) =>
    apiRequest("/api/v1/admin/channels/{id}/disable", "post", init),

  /** 停用上游凭据 */
  disableAdminCredential: (init: ApiInit<"/api/v1/admin/credentials/{id}/disable", "post">) =>
    apiRequest("/api/v1/admin/credentials/{id}/disable", "post", init),

  /** 停用商家 */
  disableAdminMerchant: (init: ApiInit<"/api/v1/admin/merchants/{id}/disable", "post">) =>
    apiRequest("/api/v1/admin/merchants/{id}/disable", "post", init),

  /** 停用渠道模型映射 */
  disableAdminModelMap: (init: ApiInit<"/api/v1/admin/modelmaps/{id}/disable", "post">) =>
    apiRequest("/api/v1/admin/modelmaps/{id}/disable", "post", init),

  /** 停用上游渠道 */
  disablePartnerChannel: (init: ApiInit<"/api/v1/partner/channels/{id}/disable", "post">) =>
    apiRequest("/api/v1/partner/channels/{id}/disable", "post", init),

  /** 停用上游凭据 */
  disablePartnerCredential: (init: ApiInit<"/api/v1/partner/credentials/{id}/disable", "post">) =>
    apiRequest("/api/v1/partner/credentials/{id}/disable", "post", init),

  /** 启用渠道 */
  enableAdminChannel: (init: ApiInit<"/api/v1/admin/channels/{id}/enable", "post">) =>
    apiRequest("/api/v1/admin/channels/{id}/enable", "post", init),

  /** 启用上游凭据 */
  enableAdminCredential: (init: ApiInit<"/api/v1/admin/credentials/{id}/enable", "post">) =>
    apiRequest("/api/v1/admin/credentials/{id}/enable", "post", init),

  /** 启用上游渠道 */
  enablePartnerChannel: (init: ApiInit<"/api/v1/partner/channels/{id}/enable", "post">) =>
    apiRequest("/api/v1/partner/channels/{id}/enable", "post", init),

  /** 启用上游凭据 */
  enablePartnerCredential: (init: ApiInit<"/api/v1/partner/credentials/{id}/enable", "post">) =>
    apiRequest("/api/v1/partner/credentials/{id}/enable", "post", init),

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

  /** 分账对账单 */
  getPartnerSettlement: (init?: ApiInit<"/api/v1/partner/settlement", "get">) =>
    apiRequest("/api/v1/partner/settlement", "get", init),

  /** 名下调用量与应扣量聚合 */
  getPartnerUsageStats: (init?: ApiInit<"/api/v1/partner/usage/stats", "get">) =>
    apiRequest("/api/v1/partner/usage/stats", "get", init),

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

  /** 账户清单 */
  listAdminAccounts: (init?: ApiInit<"/api/v1/admin/accounts", "get">) =>
    apiRequest("/api/v1/admin/accounts", "get", init),

  /** 调账清单 */
  listAdminAdjustments: (init?: ApiInit<"/api/v1/admin/adjustments", "get">) =>
    apiRequest("/api/v1/admin/adjustments", "get", init),

  /** 渠道清单 */
  listAdminChannels: (init?: ApiInit<"/api/v1/admin/channels", "get">) =>
    apiRequest("/api/v1/admin/channels", "get", init),

  /** 上游凭据清单 */
  listAdminCredentials: (init?: ApiInit<"/api/v1/admin/credentials", "get">) =>
    apiRequest("/api/v1/admin/credentials", "get", init),

  /** 商家清单 */
  listAdminMerchants: (init?: ApiInit<"/api/v1/admin/merchants", "get">) =>
    apiRequest("/api/v1/admin/merchants", "get", init),

  /** 渠道模型映射清单 */
  listAdminModelMaps: (init?: ApiInit<"/api/v1/admin/modelmaps", "get">) =>
    apiRequest("/api/v1/admin/modelmaps", "get", init),

  /** 定价版本清单 */
  listAdminPricing: (init?: ApiInit<"/api/v1/admin/pricing", "get">) =>
    apiRequest("/api/v1/admin/pricing", "get", init),

  /** 窗口限额清单 */
  listAdminQuotas: (init?: ApiInit<"/api/v1/admin/quotas", "get">) =>
    apiRequest("/api/v1/admin/quotas", "get", init),

  /** 全平台结算对账单 */
  listAdminSettlements: (init?: ApiInit<"/api/v1/admin/settlements", "get">) =>
    apiRequest("/api/v1/admin/settlements", "get", init),

  /** 全平台用量流水 */
  listAdminUsage: (init?: ApiInit<"/api/v1/admin/usage", "get">) =>
    apiRequest("/api/v1/admin/usage", "get", init),

  /** 列出可用登录方式 */
  listAuthProviders: (init?: ApiInit<"/api/v1/auth/providers", "get">) =>
    apiRequest("/api/v1/auth/providers", "get", init),

  /** 上游渠道列表 */
  listPartnerChannels: (init?: ApiInit<"/api/v1/partner/channels", "get">) =>
    apiRequest("/api/v1/partner/channels", "get", init),

  /** 上游凭据列表 */
  listPartnerCredentials: (init?: ApiInit<"/api/v1/partner/credentials", "get">) =>
    apiRequest("/api/v1/partner/credentials", "get", init),

  /** 密钥列表 */
  listUserKeys: (init?: ApiInit<"/api/v1/user/keys", "get">) =>
    apiRequest("/api/v1/user/keys", "get", init),

  /** 可用模型 */
  listUserModels: (init?: ApiInit<"/api/v1/user/models", "get">) =>
    apiRequest("/api/v1/user/models", "get", init),

  /** 订单列表 */
  listUserOrders: (init?: ApiInit<"/api/v1/user/orders", "get">) =>
    apiRequest("/api/v1/user/orders", "get", init),

  /** 商品目录 */
  listUserProducts: (init?: ApiInit<"/api/v1/user/products", "get">) =>
    apiRequest("/api/v1/user/products", "get", init),

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

  /** 绑定商家归属主体 */
  setAdminMerchantOwner: (init: ApiInit<"/api/v1/admin/merchants/{id}/owner", "post">) =>
    apiRequest("/api/v1/admin/merchants/{id}/owner", "post", init),

  /** 写入渠道模型映射 */
  setAdminModelMap: (init: ApiInit<"/api/v1/admin/modelmaps", "put">) =>
    apiRequest("/api/v1/admin/modelmaps", "put", init),

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
