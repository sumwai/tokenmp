/**
 * 本文件由 docs/openapi-web.yaml 生成，勿手改。
 * 契约变更后执行 `npm run gen`（核对用 `npm run gen:check`）。
 */
export interface paths {
    "/api/v1/auth/challenge": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 获取一次性密码加密公钥
         * @description 注册、登录与改密前调用。以 `fingerprint` 为索引生成一对一次性密钥，
         *     同一指纹重复获取会覆盖旧密钥；私钥取出即失效。同一来源受频率限制，
         *     超限返回 `code=429`。
         */
        get: operations["getAuthChallenge"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/signin": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 登录
         * @description 校验账号与密码并签发会话。密码必须是经 `challenge` 公钥加密后的 base64 密文，
         *     且 `fingerprint` 与取公钥时一致。账号不存在、密码错误、账号停用一律返回
         *     `code=401` 且文案一致，不区分失败原因；`code=410` 表示密钥已失效，
         *     重新取公钥后重试一次。
         */
        post: operations["signin"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/signup": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 注册
         * @description 创建账号并在同一事务内开户，随后签发会话。密码经 `challenge` 公钥加密；
         *     邮箱已存在返回 `code=409`。是否开放注册由服务端配置决定，
         *     关闭时返回 `code=403`。
         */
        post: operations["signup"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/refresh": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 刷新会话令牌
         * @description 用刷新令牌换发新的访问令牌；刷新令牌一次性轮换，旧值同时作废。
         */
        post: operations["refreshSession"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/session": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 查询当前会话身份
         * @description 返回当前登录身份、角色与已绑定的第三方登录方式，前端据此渲染导航清单与首页段落；
         *     角色与权限的真相在服务端，前端不硬编码角色取值。
         */
        get: operations["getAuthSession"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/signout": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 登出
         * @description 吊销当前会话令牌；重复调用同样返回成功。
         */
        post: operations["signout"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/otp": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 发送一次性验证码
         * @description 向账号绑定邮箱发送一次性验证码（OTP），`purpose` 区分用途：
         *     `reset` 用于重置密码，`erase` 用于注销账号。无论邮箱是否存在都返回成功，
         *     避免通过响应差异枚举账号；同一来源受频率限制。
         */
        post: operations["sendAuthOtp"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/erase": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 注销账号
         * @description 注销当前登录账号。提交经 `purpose=erase` 获取的 OTP 验证身份；
         *     成功后该账号立即不可登录，全部会话与刷新令牌失效，重复调用返回 `code=401`。
         *     账号标识与关联数据的保留策略由部署方配置决定。
         */
        post: operations["eraseAccount"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/reset": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 重置密码
         * @description 校验邮箱与 OTP 后设置新密码，新密码经 `challenge` 公钥加密。
         *     成功后该账号的全部会话与刷新令牌失效，需重新登录。
         */
        post: operations["resetPassword"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/password": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * 修改密码
         * @description 已登录下调用，校验当前密码后设置新密码。当前密码与新密码各经一次
         *     `challenge` 取公钥加密，分别携带对应的 `fingerprint`。
         *     成功后除当前会话外的全部会话失效。
         */
        put: operations["changePassword"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/providers": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 列出可用登录方式
         * @description 返回启用中的第三方登录提供方，前端据此渲染登录页的第三方入口。
         */
        get: operations["listAuthProviders"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/oauth/{provider}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 获取第三方授权跳转地址
         * @description 返回提供方的授权页地址（含服务端签发的 `state`），客户端整页跳转；
         *     `state` 在换取会话时原样带回并由服务端校验。提供方未启用返回 `code=404`。
         */
        get: operations["getAuthOauthUrl"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/oauth/{provider}/exchange": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 用第三方授权码换取会话
         * @description 回调落在前端路由后，客户端把提供方返回的 `code` 与 `state` 提交到本端点；
         *     服务端校验 `state`、向提供方换 token、按邮箱匹配或创建账号并在同一事务内开户，
         *     随后签发会话。提供方未启用返回 `code=404`，`state` 不匹配返回 `code=400`。
         */
        post: operations["exchangeAuthOauth"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/user/console": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 控制台清单
         * @description 返回当前登录主体可用的控制台清单：底部导航项、首页段落与能力集合。
         *     角色与权限的真相在服务端：清单按角色生成，只包含当前主体确实持有的条目
         *     （条目所需的能力不在 `capabilities` 里时该条目不出现），前端按清单渲染，
         *     不硬编码角色取值。
         *
         *     `capabilities` 的取值：`console` 控制台首页、`keys` 密钥自助管理、
         *     `requests` 请求记录、`usage` 用量与应扣量、`account` 账户概览、
         *     `ops` 管理面。取值只增不改语义，未识别的取值前端忽略。
         */
        get: operations["getUserConsole"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/user/account": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 账户概览
         * @description 返回当前登录主体所属账户的摘要：账户标识、可用包存量、生效中的窗口限额与最近流水。
         *     与数据面的账户自助查询（`docs/openapi.yaml` 的 `/v1/me/account`）口径一致，
         *     差别只在鉴权与作用域推导；判定复用同一份实现，展示与判定不会漂移。
         *     响应不含密钥、上游凭据或商家内部标识。
         */
        get: operations["getUserAccount"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/user/keys": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 密钥列表
         * @description 列出当前账户下的 API 密钥，按主键倒序。只返回密钥前缀，哈希与明文从不返回。
         */
        get: operations["listUserKeys"];
        put?: never;
        /**
         * 创建密钥
         * @description 为当前账户签发一把 API 密钥。明文只在本次响应出现一次，之后任何端点都不再返回；
         *     列表只给出前缀。密钥归属固定为当前账户，不接受归属参数。
         */
        post: operations["createUserKey"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/user/keys/{id}/revoke": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 吊销密钥
         * @description 吊销一把属于当前账户的密钥；已吊销的密钥再次吊销同样返回成功。
         *     不属于当前账户的密钥返回 `code=404`，不以 403 区分存在性。
         */
        post: operations["revokeUserKey"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/user/usage": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 用量流水
         * @description 列出当前账户的用量流水，按写入时刻倒序。流水是 append-only 的计费事实，
         *     一次请求一行；被重试过的失败尝试不产生流水。
         */
        get: operations["listUserUsage"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/user/usage/stats": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 用量聚合
         * @description 按维度把区间内的用量与应扣量合计起来，用于回答趋势与「花在哪了」。
         *
         *     分组维度 `day` 取日期（`YYYY-MM-DD`，按写入时刻所在的自然日）、`model` 取客户端
         *     请求的模型名、`api_key` 取密钥 id 的十进制文本。合计与流水明细同源：同一组过滤
         *     参数下，明细逐行相加应等于本端点的合计。
         *
         *     它只回答合计，不返回单条流水；看某次调用的明细走用量流水端点。
         */
        get: operations["getUserUsageStats"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/user/models": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 可用模型
         * @description 列出当前账户可调用的模型及其支持的线协议。模型可用性由渠道与模型映射决定，
         *     响应只给出客户端可用的模型名，不含上游模型名与渠道标识。
         */
        get: operations["listUserModels"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/user/requests": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 请求记录
         * @description 列出当前账户的请求记录，按请求时刻倒序。粒度是客户端发起的一次请求：
         *     重试过的失败尝试不单独成行，因此条数与实际调用次数一致。
         *
         *     记录只覆盖摘要层与归属层字段。渠道标识、上游标识、商家标识与上游错误详情
         *     不对用户暴露；这些字段属于商家面与管理面的统计口径。
         */
        get: operations["listUserRequests"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/user/requests/{request_id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 请求详情
         * @description 按请求标识返回单条记录的完整视图：摘要与归属层字段、尝试时间线，以及脱敏后的
         *     请求结构与错误响应结构。
         *
         *     报文按「落盘即脱敏」处理：原文从不落盘，只有键名、嵌套结构与参数真值被保留，
         *     用户内容替换为带长度的类型标记。因此报文可解析、可对比，但不含用户数据。
         *
         *     成功请求不保留报文，`request_shape` 与 `error_response_shape` 为 `null`；
         *     报文只保留 7 天，超期后同样为 `null`。这两点由 `payload_available` 显式区分：
         *     为 `false` 表示本次没有可取的报文，而不是接口异常。
         */
        get: operations["getUserRequest"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/user/requests/stats": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 请求记录聚合
         * @description 按维度聚合当前账户的请求计数，用于回答时间跨度上的趋势与错误率。
         *
         *     明细超过保留期后进入归档，本端点的数据源是随明细同步维护的按天聚合缓存，
         *     因此超期区间同样可查，不触发回源归档；区间内明细尚在热表时，两者口径一致。
         *
         *     它只回答计数，不返回单条记录；按 `request_id` 定位具体请求走详情端点。
         */
        get: operations["getUserRequestStats"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/channels": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 渠道清单
         * @description 列出全部上游渠道，与 `tokenmp admin channel list --json` 同一份行数据。
         *     渠道的 `config` 不回显：探针请求头里可能有鉴权 token。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        get: operations["listAdminChannels"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/credentials": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 上游凭据清单
         * @description 列出全部上游凭据，与 `tokenmp admin credential list --json` 同一份行数据。
         *     只给脱敏前缀与形态，明文与 secret JSON 任何端点都不返回。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        get: operations["listAdminCredentials"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/modelmaps": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 渠道模型映射清单
         * @description 列出全部渠道模型映射，与 `tokenmp admin model-map list --json` 同一份行数据。
         *     路径段用单词（`modelmaps`），与认证面路径的命名约定一致。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        get: operations["listAdminModelMaps"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/accounts": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 账户清单
         * @description 列出全部账户，与 `tokenmp admin account list --json` 同一份行数据。
         *     含归属主体与默认商家的标识：管理面要据此把账户与运营对象对应起来。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        get: operations["listAdminAccounts"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/pricing": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 定价版本清单
         * @description 列出定价版本，与 `tokenmp admin price list --json` 同一份行数据；按 id 升序。
         *     未下架的行 `retired_at` 为 `null`，据此区分生效与已退役版本（与 CLI 的
         *     `active` / `retired` 列同一判定）。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        get: operations["listAdminPricing"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/quotas": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 窗口限额清单
         * @description 列出窗口限额定义，附当前窗口的已用量与剩余额度，与
         *     `tokenmp admin quota list --json` 同一份行数据。已用量与剩余额度复用判定链上的
         *     同一份窗口计算；无法判定的行两者为 `null`，展示层给占位符。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        get: operations["listAdminQuotas"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/adjustments": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 调账清单
         * @description 列出人工调账记录，与 `tokenmp admin adjust list --json` 同一份行数据；按 id 升序。
         *     调整数量是十进制字符串，正数为补扣、负数为退费。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        get: operations["listAdminAdjustments"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/usage": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 全平台用量流水
         * @description 列出用量流水，与 `tokenmp admin usage list --json` 同一份行数据；按 id 升序。
         *     与用户面流水的差别只在作用域：这里跨账户，用于对账与排障，因此多出
         *     `merchant_id` 与结算字段。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        get: operations["listAdminUsage"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export type webhooks = Record<string, never>;
export interface components {
    schemas: {
        /**
         * @description 业务结果码，取值见 info.description 的业务码表；程序按它分支，不解析 message。
         *     取值与常量名同时是前端业务码常量的生成来源。
         * @enum {integer}
         */
        BusinessCode: 200 | 400 | 401 | 403 | 404 | 409 | 410 | 429 | 500;
        /** @description 页面通信的统一信封，六个字段固定出现。 */
        Envelope: {
            code: components["schemas"]["BusinessCode"];
            /** @description 业务数据，结构由各端点声明；无数据时省略或为 null。 */
            data: unknown;
            /** @description 面向用户的文案；程序不得按它分支。 */
            message: string;
            /** @description 当前页码，从 1 起；非列表端点为 null。 */
            page: number | null;
            /** @description 每页条数；非列表端点为 null。 */
            size: number | null;
            /** @description 总条数；非列表端点为 null。 */
            total: number | null;
        };
        /** @description 经一次性公钥 RSA-OAEP（SHA-256）加密后的 base64 密文。 */
        EncryptedSecret: string;
        /**
         * @description 客户端本地生成并持久化的指纹，同时充当一次性密钥的标识：
         *     取公钥时提交它作为索引，提交密文时携带同一值供服务端定位私钥。
         */
        Fingerprint: string;
        ChallengeData: {
            /** @constant */
            algorithm: "RSA-OAEP";
            /** @constant */
            hash: "SHA-256";
            /** @description SPKI DER 格式的 base64 公钥。 */
            public_key: string;
            /** @description 密钥有效期，单位秒。 */
            expires_in: number;
        };
        SigninRequest: {
            /** @description 账号标识（邮箱或手机号）。 */
            username: string;
            password: components["schemas"]["EncryptedSecret"];
            fingerprint: components["schemas"]["Fingerprint"];
        };
        SignupRequest: {
            /** Format: email */
            email: string;
            username: string;
            password: components["schemas"]["EncryptedSecret"];
            fingerprint: components["schemas"]["Fingerprint"];
        };
        RefreshRequest: {
            refresh_token: string;
        };
        SessionTokens: {
            /** @description 访问令牌，随 Authorization 头提交。 */
            access_token: string;
            /** @description 刷新令牌，一次性轮换。 */
            refresh_token: string;
            /** @description 访问令牌有效期，单位秒。 */
            expires_in: number;
            user: components["schemas"]["SessionUser"];
        };
        AccessTokenData: {
            access_token: string;
            /** @description 访问令牌有效期，单位秒。 */
            expires_in: number;
        };
        SessionUser: {
            /** Format: int64 */
            id: number;
            username: string;
            /**
             * @description 主体持有的身份集合，叠加而非互斥：`member` 是所有主体的基线（注册默认），
             *     `partner` 是申请制的商家身份，`admin` 是平台管理员且同时持有前两者。
             *     顺序固定为基线在前。取值由服务端下发，前端按 `capabilities` 渲染导航与
             *     权限，不按身份取值分支，也不硬编码。
             */
            roles: ("member" | "partner" | "admin")[];
            /**
             * @description 主体持有的能力标识，为各身份能力的并集，与 `ConsoleData.capabilities`
             *     同取值域；同样只增不改语义，未识别的取值一律忽略。
             */
            capabilities: string[];
            /** @description 已绑定的登录身份，`password` 表示已设密码，其余为第三方提供方标识。 */
            identities: string[];
        };
        AuthProvider: {
            /** @description 提供方标识，用于 `oauth` 与 `exchange` 端点的 `provider` 参数。 */
            id: string;
            /** @description 展示名。 */
            name: string;
        };
        SendOtpRequest: {
            /** Format: email */
            email: string;
            /**
             * @description 验证码用途：`reset` 重置密码，`erase` 注销账号；两者不可混用。
             * @enum {string}
             */
            purpose: "reset" | "erase";
        };
        EraseRequest: {
            /** @description 经 `purpose=erase` 获取的一次性验证码。 */
            otp: string;
        };
        ResetRequest: {
            /** Format: email */
            email: string;
            /** @description 经 `purpose=reset` 获取的一次性验证码。 */
            otp: string;
            password: components["schemas"]["EncryptedSecret"];
            fingerprint: components["schemas"]["Fingerprint"];
        };
        ChangePasswordRequest: {
            current_password: components["schemas"]["EncryptedSecret"];
            current_fingerprint: components["schemas"]["Fingerprint"];
            new_password: components["schemas"]["EncryptedSecret"];
            new_fingerprint: components["schemas"]["Fingerprint"];
        };
        /** @description 控制台清单：当前主体的导航项、首页段落与能力集合。 */
        ConsoleData: {
            /**
             * @description 当前主体持有的身份集合，与 `SessionUser.roles` 同取值域。它只用于展示与
             *     排障：前端按 `capabilities` 与清单条目渲染，不按身份取值分支。
             */
            roles: string[];
            /**
             * @description 当前主体持有的能力标识，为各身份能力的并集；取值只增不改语义，
             *     未识别的取值一律忽略。
             */
            capabilities: string[];
            /** @description 底部导航项，按顺序渲染；数量不超过 5。 */
            navigation: components["schemas"]["ConsoleEntry"][];
            /** @description 首页段落，按顺序渲染；条目被过滤光的段落不出现。 */
            sections: components["schemas"]["ConsoleSection"][];
        };
        /** @description 一条控制台条目，即一个页面入口。 */
        ConsoleEntry: {
            title: string;
            /** @description 一句话说明；无补充说明时为空串。 */
            description: string;
            /** @description Lucide 图标名（kebab-case）；未登记的名字不渲染图标。 */
            icon: string;
            /** @description 站内相对路径，用于导航与登录回跳。 */
            path: string;
            /** @description 该条目所需的能力标识；不在 `capabilities` 里时不渲染本项。 */
            capability: string;
        };
        /** @description 首页的一个段落。 */
        ConsoleSection: {
            title: string;
            description: string;
            entries: components["schemas"]["ConsoleEntry"][];
        };
        /** @description 账户摘要。字段只增不改语义；与数据面自助查询同口径。 */
        AccountSummary: {
            account: components["schemas"]["AccountRef"];
            /**
             * @description 可用包存量：未过期且剩余大于 0 的账本行。同单位的包相邻排列，
             *     不合并成每单位一条合计，因为各包的 fallback 与到期时间不同。
             */
            buckets: components["schemas"]["AccountBucket"][];
            /** @description 生效中的窗口限额行，账户维度在前。 */
            quotas: components["schemas"]["AccountQuota"][];
            /** @description 最近流水摘要，按写入时刻倒序；条数由 `recent` 参数决定。 */
            recent: components["schemas"]["AccountRecentUsage"][];
        };
        AccountRef: {
            /** Format: int64 */
            id: number;
            /** @description 账户业务编码。 */
            code: string;
        };
        /** @description 一条可用包存量。 */
        AccountBucket: {
            /** @description 结算单位 `currency` / `token` / `credit`。 */
            unit: string;
            /** @description 剩余存量，十进制字符串。 */
            remaining: string;
            /** @description 包扣尽后的处置 `charge_balance` / `reject`。 */
            fallback: string;
            /**
             * Format: date-time
             * @description 到期时刻；`null` 表示不过期。
             */
            expires_at: string | null;
        };
        /** @description 一条窗口限额及其当前窗口的已用量。 */
        AccountQuota: {
            /** @description 限额维度 `account` 或 `api_key`。 */
            scope: string;
            /** @description 计量指标。 */
            metric: string;
            /** @description 窗口类型 `rolling` / `calendar`。 */
            window_kind: string;
            /** @description 窗口周期 `5h` / `day` / `week` / `month` / `total`。 */
            period: string;
            /** @description 当前窗口已用量，十进制字符串。 */
            used: string;
            /** @description 限额，十进制字符串。 */
            limit: string;
            /** @description 超限处置 `reject` / `throttle`。 */
            action: string;
            /**
             * Format: date-time
             * @description 当前窗口结束时刻；`null` 表示不会自然重置（total）。
             */
            resets_at: string | null;
        };
        /** @description 一条流水摘要。 */
        AccountRecentUsage: {
            model: string;
            /** Format: date-time */
            created_at: string;
            /** @description 本次应扣量，十进制字符串。 */
            charged_amount: string;
        };
        /** @description 一把 API 密钥的可见部分；哈希与明文从不出现。 */
        ApiKey: {
            /** Format: int64 */
            id: number;
            name: string;
            /** @description 明文前缀，仅供展示与检索。 */
            key_prefix: string;
            enabled: boolean;
            /** Format: date-time */
            expires_at: string | null;
            /** Format: date-time */
            last_used_at: string | null;
        };
        PageOfApiKey: {
            items: components["schemas"]["ApiKey"][];
        };
        CreateApiKeyRequest: {
            name: string;
            /**
             * Format: date-time
             * @description 过期时刻；`null` 或省略表示不过期。
             */
            expires_at?: string | null;
        };
        /** @description 创建响应；`secret` 只在此处出现一次。 */
        CreatedApiKey: {
            /** Format: int64 */
            id: number;
            name: string;
            key_prefix: string;
            enabled: boolean;
            /** Format: date-time */
            expires_at: string | null;
            /** Format: date-time */
            last_used_at: string | null;
            /** @description 密钥明文，仅本次响应返回，不落库、不再查询。 */
            secret: string;
        };
        /** @description 一次调用的 token 用量分量。子项包含于主计数：缓存读写属于输入，推理属于输出。 */
        UsageTokens: {
            /** @description 输入 token 总数，含缓存读写。 */
            input_tokens: number;
            /** @description 输出 token 总数，含推理。 */
            output_tokens: number;
            cache_read_tokens: number;
            /** @description 不分档的缓存写计数；与两个分档字段互斥使用。 */
            cache_write_tokens: number;
            cache_write_5m_tokens: number;
            cache_write_1h_tokens: number;
            reasoning_tokens: number;
            /** @description 服务端工具执行次数，按次计费，不是 token 计数。 */
            server_tool_uses: number;
        };
        /** @description 一条用量流水。 */
        UsageItem: {
            /** Format: int64 */
            id: number;
            /** @description 客户端请求的模型名。 */
            model: string;
            /** @description 实际履约的模型名。 */
            upstream_model: string;
            /** @description 客户端使用的线协议。 */
            protocol: string;
            cross_protocol: boolean;
            usage: components["schemas"]["UsageTokens"];
            /** @description 本次应扣量，十进制字符串。 */
            charged_amount: string;
            /** Format: date-time */
            created_at: string;
        };
        PageOfUsageItem: {
            items: components["schemas"]["UsageItem"][];
        };
        /** @description 一个分组维度的用量合计。 */
        UsageStatsItem: {
            /**
             * @description 分组值：`day` 为日期（`YYYY-MM-DD`），`model` 为客户端请求的模型名，
             *     `api_key` 为密钥 id 的十进制文本。
             */
            key: string;
            /** @description 区间内成功履约的请求次数，与流水明细的行数一致。 */
            calls: number;
            usage: components["schemas"]["UsageTokens"];
            /** @description 应扣量合计：逐行「基础价 × 倍率」相加，十进制字符串。 */
            charged_amount: string;
        };
        PageOfUsageStatsItem: {
            items: components["schemas"]["UsageStatsItem"][];
        };
        /** @description 一个当前账户可调用的模型。 */
        ModelInfo: {
            /** @description 客户端可用的模型名，可直接用于请求体。 */
            name: string;
            /** @description 支持调用的线协议标识。 */
            protocols: string[];
        };
        PageOfModelInfo: {
            items: components["schemas"]["ModelInfo"][];
        };
        /** @description 一条请求记录的摘要与归属层字段。 */
        RequestItem: {
            request_id: string;
            /** Format: date-time */
            created_at: string;
            /**
             * @description 请求终态。
             * @enum {string}
             */
            status: "success" | "failed" | "cancelled";
            /** @description 返回给客户端的 HTTP 状态码。 */
            http_status: number;
            /** @description 上游返回的状态码；未取得时为 `null`。 */
            upstream_status: number | null;
            /** @description 失败分类；成功时为 `null`。 */
            failure_class: string | null;
            /** @description 网关错误码；成功时为 `null`。 */
            error_code: string | null;
            /** @description 上游调用耗时，不含向客户端写出的耗时。 */
            duration_ms: number;
            /** @description 客户端请求的模型名。 */
            model: string;
            /** @description 实际发往上游的模型名；与 `model` 不同表示网关改写过。 */
            upstream_model: string;
            /** @description 客户端使用的线协议。 */
            protocol: string;
            /** @description 本次请求发往上游的线协议。 */
            upstream_protocol: string;
            /** @description 两侧协议是否不同，即本次是否发生跨协议重建。 */
            cross_protocol: boolean;
            /**
             * Format: int64
             * @description 签发本次调用的密钥 id，可按它过滤。
             */
            api_key_id: number;
            /** @description 取得的 token 用量；未取得时为 `null`。 */
            usage: components["schemas"]["UsageTokens"] | null;
            /**
             * @description 是否存在可取的脱敏报文。仅失败请求保留报文，且只保留 7 天，
             *     其余情况为 `false`。
             */
            payload_available: boolean;
            /** @description 本次是否为流式请求。 */
            stream?: boolean;
            /** @description 已写给客户端的响应体字节数。 */
            written_bytes?: number;
        };
        PageOfRequestItem: {
            items: components["schemas"]["RequestItem"][];
        };
        /** @description 一个分组维度的请求计数。 */
        RequestStatsItem: {
            /** @description 分组值：`day` 为日期（`YYYY-MM-DD`），`model` 为模型名，`status` 为终态。 */
            key: string;
            total: number;
            success: number;
            failed: number;
            cancelled: number;
        };
        RequestStatsPage: {
            items: components["schemas"]["RequestStatsItem"][];
        };
        /** @description 一次上游尝试。重试、渠道回退各占一条，按尝试序号排列。 */
        RequestAttempt: {
            /** @description 尝试序号，从 1 起。 */
            attempt: number;
            /** @enum {string} */
            outcome: "ok" | "failed" | "cancelled" | "skipped";
            upstream_status: number | null;
            failure_class: string | null;
            error_code: string | null;
            cross_protocol: boolean;
            duration_ms: number;
        };
        /**
         * @description 脱敏后的报文结构。键名与嵌套层次原样保留，值按规则处理：请求参数白名单保留
         *     真值；用户内容替换为 `{"__redacted": "<类型>", "len": <长度>}`；数组保留
         *     元素形状，元素数超过采样上限时补 `__total` 给出实际数量。
         *
         *     原文从不落盘，本结构可解析、可对比，用于判定请求形状与参数是否符合预期，
         *     但不含用户数据。
         */
        RedactedPayload: {
            [key: string]: unknown;
        };
        /** @description 一条请求记录的完整视图。 */
        RequestDetail: {
            request: components["schemas"]["RequestItem"];
            /** @description 尝试时间线，按尝试序号排列。 */
            attempts: components["schemas"]["RequestAttempt"][];
            /** @description 脱敏后的客户端请求结构；无报文时为 `null`。 */
            request_shape: components["schemas"]["RedactedPayload"] | null;
            /**
             * @description 脱敏后实际发往上游的请求结构。与 `request_shape` 的差异即网关改写的部分：
             *     模型名替换、协议重建、报文改写。无报文时为 `null`。
             */
            upstream_request_shape: components["schemas"]["RedactedPayload"] | null;
            /** @description 脱敏后的错误响应结构；成功请求或超期后为 `null`。 */
            error_response_shape: components["schemas"]["RedactedPayload"] | null;
            /** @description 网关对报文做过的改写标注，按出现顺序排列；未改写时为空数组。 */
            rewritten_parts: string[];
            /**
             * @description 客户端地址。取自连接层，存在反向代理时为代理地址；客户端可影响其取值，
             *     只作排障线索，不作为安全判定依据。
             */
            client_ip: string;
            /**
             * @description 客户端自报的 User-Agent，按上限截断；未自报时为空串。可伪造，
             *     只作排障线索。
             */
            user_agent: string;
        };
        /**
         * @description 一条上游渠道，字段与 `tokenmp admin channel list --json` 的行一致。
         *     `config` 不在其中：探针请求头里可能有鉴权 token，管理面不回显配置原文。
         */
        AdminChannel: {
            /** Format: int64 */
            id: number;
            /**
             * Format: int64
             * @description 归属商家 id。
             */
            merchant_id: number;
            name: string;
            /** @description 厂商标识，如 `openai` / `anthropic` / `gemini`。 */
            vendor: string;
            /** @description 渠道的线协议类型，取值见数据面的渠道类型白名单。 */
            type: string;
            /** @description 取用上游凭据的分组名。 */
            cred_group: string;
            /** @description 上游基地址。 */
            base_url: string;
            /** @description 选路优先级，越小越优先。 */
            priority: number;
            /** @description 同优先级内的加权轮询权重。 */
            weight: number;
            enabled: boolean;
        };
        PageOfAdminChannel: {
            items: components["schemas"]["AdminChannel"][];
        };
        /**
         * @description 一行上游凭据，字段与 `tokenmp admin credential list --json` 的行一致。
         *     明文与 secret JSON 从不出现，只有脱敏前缀。
         */
        AdminCredential: {
            /** Format: int64 */
            id: number;
            /** Format: int64 */
            merchant_id: number;
            /** @description 凭据分组；渠道按它取用凭据。 */
            cred_group: string;
            /** @description 凭据名，用于同组内区分轮换。 */
            name: string;
            /** @description 凭据形态：`api`、`oauth`，解析失败时为 `unknown`。 */
            kind: string;
            /** @description 可展示的明文前缀。 */
            prefix: string;
            /** @description oauth 凭据的过期时刻（RFC3339）；api 凭据没有该字段。 */
            expires?: string;
            /** @description oauth 凭据的访问令牌是否已过期；api 凭据恒为 false。 */
            expired: boolean;
            enabled: boolean;
        };
        PageOfAdminCredential: {
            items: components["schemas"]["AdminCredential"][];
        };
        /** @description 一条渠道模型映射，字段与 `tokenmp admin model-map list --json` 的行一致。 */
        AdminModelMap: {
            /** Format: int64 */
            id: number;
            /** Format: int64 */
            channel_id: number;
            /** @description 客户端请求的模型名。 */
            model: string;
            /** @description 转发给该渠道时替换成的模型名。 */
            upstream_model: string;
            /** @description 该映射的价格倍率，十进制字符串。 */
            price_multiplier: string;
            /** @description 转发时叠加到请求体上的字段；未配置时该字段不出现。 */
            request_overrides?: {
                [key: string]: unknown;
            };
            enabled: boolean;
        };
        PageOfAdminModelMap: {
            items: components["schemas"]["AdminModelMap"][];
        };
        /** @description 一个账户，字段与 `tokenmp admin account list --json` 的行一致。 */
        AdminAccount: {
            /** Format: int64 */
            id: number;
            code: string;
            name: string;
            /** @description 归属登录主体的 id；平台账户没有归属时为 `null`。 */
            owner_user_id: number | null;
            /** @description 默认商家 id；未指定时为 `null`。 */
            default_merchant_id: number | null;
            /** @description 账户级价格倍率，十进制字符串。 */
            price_multiplier: string;
            /** @description 账户状态：`active` 可用，`disabled` 已停用。 */
            status: string;
        };
        PageOfAdminAccount: {
            items: components["schemas"]["AdminAccount"][];
        };
        /**
         * @description 一个定价版本，字段与 `tokenmp admin price list --json` 的行一致。
         *     改价产生新版本行并把旧版本 `retired_at` 置位，不覆盖旧行。
         */
        AdminPricing: {
            /** Format: int64 */
            id: number;
            /**
             * Format: int64
             * @description 定价归属的商家 id。
             */
            merchant_id: number;
            /** @description 客户端请求的模型名。 */
            model: string;
            /** @description 同一 (商家, 模型) 下自增的版本号。 */
            version: number;
            /**
             * Format: date-time
             * @description 版本生效时刻。
             */
            effective_at: string;
            /**
             * Format: date-time
             * @description 版本退役时刻；`null` 表示当前生效。
             */
            retired_at: string | null;
        };
        PageOfAdminPricing: {
            items: components["schemas"]["AdminPricing"][];
        };
        /**
         * @description 一条窗口限额定义加当前窗口的已用量，字段与 `tokenmp admin quota list --json`
         *     的行一致。已用量与剩余额度由判定链上的同一份窗口计算得出。
         */
        AdminQuota: {
            /** Format: int64 */
            id: number;
            /** @description 限额作用范围：`account` / `api_key` / `channel` / `plan`。 */
            scope: string;
            /**
             * Format: int64
             * @description 范围实体的 id。
             */
            scope_id: number;
            /** @description 计量指标，取值见数据面的计费指标白名单。 */
            metric: string;
            /** @description 窗口类型。 */
            window_kind: string;
            /** @description 窗口周期。 */
            period: string;
            /** @description 窗口内允许的额度上限，十进制字符串。 */
            limit_amount: string;
            /** @description 超限时的处置动作。 */
            action: string;
            /** @description 当前窗口已用量，十进制字符串；无法判定时为 `null`。 */
            used: string | null;
            /** @description 当前窗口剩余额度，十进制字符串；无法判定时为 `null`。 */
            remaining: string | null;
        };
        PageOfAdminQuota: {
            items: components["schemas"]["AdminQuota"][];
        };
        /** @description 一条人工调账记录，字段与 `tokenmp admin adjust list --json` 的行一致。 */
        AdminAdjustment: {
            /** Format: int64 */
            id: number;
            /** Format: int64 */
            account_id: number;
            /** @description 调整数量，十进制字符串；正数为补扣，负数为退费。 */
            delta_amount: string;
            reason: string;
            /** @description 操作者标识。 */
            operator: string;
            /** Format: date-time */
            created_at: string;
        };
        PageOfAdminAdjustment: {
            items: components["schemas"]["AdminAdjustment"][];
        };
        /**
         * @description 一条跨账户的用量流水，字段与 `tokenmp admin usage list --json` 的行一致。
         *     流水是 append-only 的计费事实，一次请求一行。
         */
        AdminUsageItem: {
            /** Format: int64 */
            id: number;
            /**
             * Format: int64
             * @description 结算归属的商家 id。
             */
            merchant_id: number;
            /** Format: int64 */
            account_id: number;
            /**
             * Format: int64
             * @description 实际履约的渠道 id。
             */
            channel_id: number;
            model: string;
            /** @description 本次调用的用量分量，键为计费指标、值为十进制字符串。 */
            usage: {
                [key: string]: string;
            };
            /** @description 结算前的应扣量，十进制字符串。 */
            gross_amount: string;
            /** @description 本次生效的总倍率，十进制字符串。 */
            multiplier: string;
            /** @description 结算明细；未结算时该字段不出现。 */
            settlement?: {
                [key: string]: unknown;
            };
            /** Format: date-time */
            created_at: string;
        };
        PageOfAdminUsageItem: {
            items: components["schemas"]["AdminUsageItem"][];
        };
    };
    responses: {
        /** @description 参数缺失或非法，`code=400`。 */
        BadRequest: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Envelope"];
            };
        };
        /** @description 未登录、会话失效或凭据错误，`code=401`。 */
        Unauthorized: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Envelope"];
            };
        };
        /** @description 一次性加密公钥已失效，`code=410`；重新获取公钥后重试一次。 */
        ChallengeExpired: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Envelope"];
            };
        };
        /** @description 触发频率限制，`code=429`。 */
        TooManyRequests: {
            headers: {
                "Retry-After": components["headers"]["RetryAfter"];
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Envelope"];
            };
        };
        /** @description 已登录但无权访问该资源，`code=403`。 */
        Forbidden: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Envelope"];
            };
        };
        /** @description 资源不存在或不属于当前账户，`code=404`；两者不作区分，避免探测他人资源。 */
        NotFound: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Envelope"];
            };
        };
        /** @description 状态冲突，`code=409`。 */
        Conflict: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Envelope"];
            };
        };
        /** @description 服务端错误，`code=500`。 */
        InternalError: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Envelope"];
            };
        };
    };
    parameters: {
        /** @description 页码，从 1 起；缺省 1。 */
        Page: number;
        /** @description 每页条数；缺省 20，上限 100。 */
        Size: number;
        /** @description 起始时刻（含），RFC3339；缺省不限。 */
        Since: string;
        /** @description 结束时刻（含），RFC3339；缺省不限。 */
        Until: string;
        /** @description 按模型名精确匹配；缺省不过滤。 */
        ModelFilter: string;
        /** @description 按签发本次调用的密钥 id 过滤；缺省不过滤。 */
        ApiKeyFilter: number;
        /** @description 按商家 id 过滤；缺省不过滤。 */
        AdminMerchantFilter: number;
        /** @description 按账户 id 过滤；缺省不过滤。管理面独有：用户面的作用域一律由会话推导。 */
        AdminAccountFilter: number;
        /**
         * @description 按限额 / 规则的作用范围过滤，取值 `account` / `api_key` / `channel` / `plan`；
         *     与 `scope_id` 必须成对给出，缺省列出全部。
         */
        AdminScopeFilter: "account" | "api_key" | "channel" | "plan";
        /** @description 范围实体的 id，与 `scope` 成对使用。 */
        AdminScopeIDFilter: number;
    };
    requestBodies: never;
    headers: {
        /**
         * @description 触发频率限制（`code=429`）时出现，单位秒：按来源的滑动窗口推算出的最短等待时长。
         *     页面按它显示等待时间，不做自动重试（web/AGENTS.md 的三态）。
         */
        RetryAfter: number;
    };
    pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
    getAuthChallenge: {
        parameters: {
            query: {
                /** @description 客户端本地持久化的指纹，作为一次性密钥的索引。 */
                fingerprint: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 公钥获取成功。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["ChallengeData"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            429: components["responses"]["TooManyRequests"];
        };
    };
    signin: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SigninRequest"];
            };
        };
        responses: {
            /** @description 登录成功，签发会话令牌。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["SessionTokens"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            410: components["responses"]["ChallengeExpired"];
            429: components["responses"]["TooManyRequests"];
        };
    };
    signup: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SignupRequest"];
            };
        };
        responses: {
            /** @description 注册成功，签发会话令牌。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["SessionTokens"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            /** @description 注册入口已关闭，`code=403`。 */
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
            /** @description 邮箱或用户名已被使用，`code=409`，两者不作区分。 */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
            410: components["responses"]["ChallengeExpired"];
            429: components["responses"]["TooManyRequests"];
        };
    };
    refreshSession: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["RefreshRequest"];
            };
        };
        responses: {
            /** @description 换发成功。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["AccessTokenData"];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
        };
    };
    getAuthSession: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 会话有效。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["SessionUser"];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
        };
    };
    signout: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 登出成功。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
            401: components["responses"]["Unauthorized"];
        };
    };
    sendAuthOtp: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SendOtpRequest"];
            };
        };
        responses: {
            /** @description 已受理（邮箱不存在时不发信但同样返回成功）。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
            429: components["responses"]["TooManyRequests"];
        };
    };
    eraseAccount: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["EraseRequest"];
            };
        };
        responses: {
            /** @description 注销成功。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
        };
    };
    resetPassword: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ResetRequest"];
            };
        };
        responses: {
            /** @description 重置成功。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
            400: components["responses"]["BadRequest"];
            410: components["responses"]["ChallengeExpired"];
        };
    };
    changePassword: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ChangePasswordRequest"];
            };
        };
        responses: {
            /** @description 修改成功。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            410: components["responses"]["ChallengeExpired"];
        };
    };
    listAuthProviders: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 登录方式列表。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: {
                            items: components["schemas"]["AuthProvider"][];
                        };
                    };
                };
            };
        };
    };
    getAuthOauthUrl: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 提供方标识，取值来自 `providers` 端点（如 `google`、`github`）。 */
                provider: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 授权地址。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: {
                            /** Format: uri */
                            authorize_url: string;
                            state: string;
                        };
                    };
                };
            };
            /** @description 提供方不存在或未启用，`code=404`。 */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
        };
    };
    exchangeAuthOauth: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                provider: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": {
                    /** @description 提供方回调带到前端的授权码。 */
                    code: string;
                    state: string;
                };
            };
        };
        responses: {
            /** @description 换取成功，签发会话令牌。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["SessionTokens"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            /** @description 提供方不存在或未启用，`code=404`。 */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
            429: components["responses"]["TooManyRequests"];
        };
    };
    getUserConsole: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 控制台清单。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["ConsoleData"];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
        };
    };
    getUserAccount: {
        parameters: {
            query?: {
                /** @description 最近流水条数；缺省 10，超过 100 截到 100，`0` 表示不返回流水。 */
                recent?: number;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 账户摘要。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["AccountSummary"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    listUserKeys: {
        parameters: {
            query?: {
                /** @description 页码，从 1 起；缺省 1。 */
                page?: components["parameters"]["Page"];
                /** @description 每页条数；缺省 20，上限 100。 */
                size?: components["parameters"]["Size"];
                /** @description 按启用状态过滤；缺省不过滤。 */
                enabled?: boolean;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 密钥列表。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfApiKey"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    createUserKey: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CreateApiKeyRequest"];
            };
        };
        responses: {
            /** @description 创建成功，响应携带一次性明文。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["CreatedApiKey"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    revokeUserKey: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 吊销成功。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
            401: components["responses"]["Unauthorized"];
            404: components["responses"]["NotFound"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    listUserUsage: {
        parameters: {
            query?: {
                /** @description 页码，从 1 起；缺省 1。 */
                page?: components["parameters"]["Page"];
                /** @description 每页条数；缺省 20，上限 100。 */
                size?: components["parameters"]["Size"];
                /** @description 起始时刻（含），RFC3339；缺省不限。 */
                since?: components["parameters"]["Since"];
                /** @description 结束时刻（含），RFC3339；缺省不限。 */
                until?: components["parameters"]["Until"];
                /** @description 按模型名精确匹配；缺省不过滤。 */
                model?: components["parameters"]["ModelFilter"];
                /** @description 按签发本次调用的密钥 id 过滤；缺省不过滤。 */
                api_key_id?: components["parameters"]["ApiKeyFilter"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 用量流水。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfUsageItem"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    getUserUsageStats: {
        parameters: {
            query?: {
                /** @description 起始时刻（含），RFC3339；缺省不限。 */
                since?: components["parameters"]["Since"];
                /** @description 结束时刻（含），RFC3339；缺省不限。 */
                until?: components["parameters"]["Until"];
                /** @description 分组维度；缺省 `day`。 */
                group_by?: "day" | "model" | "api_key";
                /** @description 按模型名精确匹配；缺省不过滤。 */
                model?: components["parameters"]["ModelFilter"];
                /** @description 按签发本次调用的密钥 id 过滤；缺省不过滤。 */
                api_key_id?: components["parameters"]["ApiKeyFilter"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 聚合合计，按 `key` 升序。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfUsageStatsItem"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    listUserModels: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 模型列表。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfModelInfo"];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
            500: components["responses"]["InternalError"];
        };
    };
    listUserRequests: {
        parameters: {
            query?: {
                /** @description 页码，从 1 起；缺省 1。 */
                page?: components["parameters"]["Page"];
                /** @description 每页条数；缺省 20，上限 100。 */
                size?: components["parameters"]["Size"];
                /** @description 起始时刻（含），RFC3339；缺省不限。 */
                since?: components["parameters"]["Since"];
                /** @description 结束时刻（含），RFC3339；缺省不限。 */
                until?: components["parameters"]["Until"];
                /** @description 按模型名精确匹配；缺省不过滤。 */
                model?: components["parameters"]["ModelFilter"];
                /** @description 按签发本次调用的密钥 id 过滤；缺省不过滤。 */
                api_key_id?: components["parameters"]["ApiKeyFilter"];
                /** @description 按终态过滤；缺省不过滤。 */
                status?: "success" | "failed" | "cancelled";
                /** @description 按请求标识精确匹配，用于定位单条记录。 */
                request_id?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 请求记录。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfRequestItem"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    getUserRequest: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 请求标识，取自列表端点。 */
                request_id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 请求详情。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["RequestDetail"];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
            404: components["responses"]["NotFound"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    getUserRequestStats: {
        parameters: {
            query?: {
                /** @description 起始时刻（含），RFC3339；缺省不限。 */
                since?: components["parameters"]["Since"];
                /** @description 结束时刻（含），RFC3339；缺省不限。 */
                until?: components["parameters"]["Until"];
                /** @description 分组维度；缺省 `day`。 */
                group_by?: "day" | "model" | "status";
                /** @description 按模型名精确匹配；缺省不过滤。 */
                model?: components["parameters"]["ModelFilter"];
                /** @description 按签发本次调用的密钥 id 过滤；缺省不过滤。 */
                api_key_id?: components["parameters"]["ApiKeyFilter"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 聚合计数，按 `key` 升序。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["RequestStatsPage"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    listAdminChannels: {
        parameters: {
            query?: {
                /** @description 页码，从 1 起；缺省 1。 */
                page?: components["parameters"]["Page"];
                /** @description 每页条数；缺省 20，上限 100。 */
                size?: components["parameters"]["Size"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 渠道清单。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfAdminChannel"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
        };
    };
    listAdminCredentials: {
        parameters: {
            query?: {
                /** @description 页码，从 1 起；缺省 1。 */
                page?: components["parameters"]["Page"];
                /** @description 每页条数；缺省 20，上限 100。 */
                size?: components["parameters"]["Size"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 凭据清单。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfAdminCredential"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
        };
    };
    listAdminModelMaps: {
        parameters: {
            query?: {
                /** @description 页码，从 1 起；缺省 1。 */
                page?: components["parameters"]["Page"];
                /** @description 每页条数；缺省 20，上限 100。 */
                size?: components["parameters"]["Size"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 模型映射清单。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfAdminModelMap"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
        };
    };
    listAdminAccounts: {
        parameters: {
            query?: {
                /** @description 页码，从 1 起；缺省 1。 */
                page?: components["parameters"]["Page"];
                /** @description 每页条数；缺省 20，上限 100。 */
                size?: components["parameters"]["Size"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 账户清单。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfAdminAccount"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
        };
    };
    listAdminPricing: {
        parameters: {
            query?: {
                /** @description 页码，从 1 起；缺省 1。 */
                page?: components["parameters"]["Page"];
                /** @description 每页条数；缺省 20，上限 100。 */
                size?: components["parameters"]["Size"];
                /** @description 按商家 id 过滤；缺省不过滤。 */
                merchant_id?: components["parameters"]["AdminMerchantFilter"];
                /** @description 按模型名精确匹配；缺省不过滤。 */
                model?: components["parameters"]["ModelFilter"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 定价版本清单。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfAdminPricing"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
        };
    };
    listAdminQuotas: {
        parameters: {
            query?: {
                /** @description 页码，从 1 起；缺省 1。 */
                page?: components["parameters"]["Page"];
                /** @description 每页条数；缺省 20，上限 100。 */
                size?: components["parameters"]["Size"];
                /**
                 * @description 按限额 / 规则的作用范围过滤，取值 `account` / `api_key` / `channel` / `plan`；
                 *     与 `scope_id` 必须成对给出，缺省列出全部。
                 */
                scope?: components["parameters"]["AdminScopeFilter"];
                /** @description 范围实体的 id，与 `scope` 成对使用。 */
                scope_id?: components["parameters"]["AdminScopeIDFilter"];
                /** @description 按账户 id 过滤；缺省不过滤。管理面独有：用户面的作用域一律由会话推导。 */
                account_id?: components["parameters"]["AdminAccountFilter"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 限额清单。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfAdminQuota"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
        };
    };
    listAdminAdjustments: {
        parameters: {
            query?: {
                /** @description 页码，从 1 起；缺省 1。 */
                page?: components["parameters"]["Page"];
                /** @description 每页条数；缺省 20，上限 100。 */
                size?: components["parameters"]["Size"];
                /** @description 按账户 id 过滤；缺省不过滤。管理面独有：用户面的作用域一律由会话推导。 */
                account_id?: components["parameters"]["AdminAccountFilter"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 调账清单。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfAdminAdjustment"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
        };
    };
    listAdminUsage: {
        parameters: {
            query?: {
                /** @description 页码，从 1 起；缺省 1。 */
                page?: components["parameters"]["Page"];
                /** @description 每页条数；缺省 20，上限 100。 */
                size?: components["parameters"]["Size"];
                /** @description 按账户 id 过滤；缺省不过滤。管理面独有：用户面的作用域一律由会话推导。 */
                account_id?: components["parameters"]["AdminAccountFilter"];
                /** @description 起始时刻（含），RFC3339；缺省不限。 */
                since?: components["parameters"]["Since"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 用量流水。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfAdminUsageItem"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
        };
    };
}
