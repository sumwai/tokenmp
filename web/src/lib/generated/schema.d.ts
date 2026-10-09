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
         *     `purchase` 充值 / 购买、`ops` 管理面。取值只增不改语义，未识别的取值前端忽略。
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
         *     账户无可用额度（`settlement.Fundable` 判定为否）时回 `402`，与数据面同一门闸；
         *     页面据此把 402 分支渲染成充值 / 购买引导。
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
    "/api/v1/user/products": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 商品目录
         * @description 列出可购买的商品档位：名称、结算单位、每份数量、售价、可用模型范围与有效天数。
         *     数量与售价都是十进制字符串；`model_scope` 为 `null` 表示不限模型。
         *     目录不分页，但分页三字段照填：档位是运营上架的有限集合，与模型目录同一形状。
         */
        get: operations["listUserProducts"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/user/orders": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 订单列表
         * @description 列出当前账户的订单，按购买时刻倒序。每笔订单记录买了哪个商品档位、几份、
         *     实付金额与派生的存量；金额、数量与折算率都是十进制字符串，不经浮点。
         */
        get: operations["listUserOrders"];
        put?: never;
        /**
         * 下单购买
         * @description 购买一个商品档位：写入购买事实并派生账本，存量、有效期与折算率按档位口径生成。
         *     归属由会话推导，不接受账户或商家参数。
         *
         *     幂等：请求必须携带客户端生成的 `idempotency_key`，服务端以
         *     `(账户, idempotency_key)` 唯一。同一键重复提交（双击、超时重试）只产生一笔订单，
         *     响应返回首次创建的那笔订单，不重复发放存量。键在账户内唯一，不同账户可用同一个键；
         *     同一键用于不同的商品或份数时同样返回首次订单，不按本次请求重新下单。
         */
        post: operations["createUserOrder"];
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
    "/api/v1/admin/merchants": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 商家清单
         * @description 列出全部商家，与 `tokenmp admin merchant list --json` 同一份行数据。
         *     渠道与凭据的写入都要给出归属商家，页面据此取候选；平台自营与入驻商家
         *     都是这里的行，`kind` 只是数据。请求方须持有管理面能力，越权返回 `code=403`。
         */
        get: operations["listAdminMerchants"];
        put?: never;
        /**
         * 新建商家
         * @description 新建一个商家，与 `tokenmp admin merchant create` 调用同一份业务实现。新建一律
         *     `active`，停用是独立动作。平台自营与入驻商家走同一条路径，`kind` 只是数据。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        post: operations["createAdminMerchant"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/merchants/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * 修改商家
         * @description 改一个商家的编码、名称与类型，字段与新建相同，整体替换。状态与归属不在本端点：
         *     前者走 `/enable` 与 `/disable`，后者走 `/owner`，三者的写入条件不同。
         *     编码是平台内唯一键，改成已占用的取值返回 `code=409`。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        put: operations["updateAdminMerchant"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/merchants/{id}/enable": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 启用商家
         * @description 按主键置启用位。动作幂等：重复调用返回成功，id 不存在返回 `code=404`。
         *     与 `/disable` 成对：停用是运营动作，误停之后必须能原地恢复。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        post: operations["enableAdminMerchant"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/merchants/{id}/disable": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 停用商家
         * @description 按主键置停用位，与 `tokenmp admin merchant disable` 同一动作。动作幂等：重复调用返回成功，
         *     id 不存在返回 `code=404`。行不因此被删除，名下渠道、凭据与历史流水仍指向它。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        post: operations["disableAdminMerchant"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/merchants/{id}/owner": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 绑定商家归属主体
         * @description 把商家绑定到一个登录主体，与 `tokenmp admin merchant set-owner` 同一动作。
         *     归属是商家域（`/api/v1/partner/*`）唯一的作用域来源：没有绑定的商家在页面上没有
         *     主人，绑定后该登录主体才能自助管理它的上游账号。归属一对一，重复绑定返 `code=409`；
         *     `user_id` 不存在返回 `code=400`（绑到一个还不存在的 id 会在那个 id 被后来注册的人
         *     拿到时把商家交给对方）。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        post: operations["setAdminMerchantOwner"];
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
        /**
         * 新建渠道
         * @description 新建一条上游渠道，与 `tokenmp admin channel create` 调用同一份业务实现。
         *     新建一律启用，启用位由后续动作改动。`priority` / `weight` 缺省取 100：0 会让渠道
         *     永远排在最后，因此不作为「未配置」的取值。`cred_group` 只是分组名，凭据要另行写入。
         *
         *     `config` 是渠道级扩展配置原文（静态请求头与探针声明），必须是 JSON 对象；
         *     非对象、`headers` 不是字符串到字符串的对象、或占用了网关自身请求头的头名，
         *     都在写入时返回 `code=400`（读取侧对这些情况是宽容的，写入口是唯一能显式拦下的地方）。
         *     归属 `merchant_id` 不存在同样返回 `code=400`。
         *
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        post: operations["createAdminChannel"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/channels/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * 修改渠道
         * @description 改一条渠道的全部配置字段，字段与新建相同，整体替换。启用位不在本端点：启停是独立的
         *     运营动作，改配置不应把一条已停用的渠道意外启用。
         *
         *     归属 `merchant_id` 不存在返回 `code=400`；同一商家下协议与渠道名重复返回 `code=409`
         *     （唯一键是 `merchant_id`、`type`、`name` 三列）。
         *
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        put: operations["updateAdminChannel"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/channels/{id}/enable": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 启用渠道
         * @description 按主键置启用位，与 `tokenmp admin channel enable` 同一动作。动作幂等：重复调用返回成功，
         *     id 不存在返回 `code=404`。停用是运营动作，误停之后靠本端点原地恢复。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        post: operations["enableAdminChannel"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/channels/{id}/disable": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 停用渠道
         * @description 按主键置停用位，与 `tokenmp admin channel disable` 同一动作。动作幂等：重复调用返回成功，
         *     id 不存在返回 `code=404`。行不因此被删除，流水与历史映射仍指向它。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        post: operations["disableAdminChannel"];
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
        /**
         * 写入上游凭据
         * @description 写入一行上游凭据，与 `tokenmp admin credential add` 调用同一份业务实现，
         *     secret 以 `{"api_key": …}` 承载。请求体里的 `api_key` 是明文，只在写入这一条
         *     路径出现；写入响应不回显明文，读取永远只有脱敏前缀。
         *
         *     订阅型上游的 OAuth 登录需要设备码轮询等交互过程，不是单次请求能完成的动作，
         *     不在本端点：它仍走 `tokenmp admin credential oauth-login`。归属 `merchant_id`
         *     不存在返回 `code=400`。
         *
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        post: operations["createAdminCredential"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/credentials/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * 修改上游凭据
         * @description 改一行上游凭据的归属、分组与名称。`api_key` 可以省略，表示保持现有明文不变；
         *     给出时在原行上覆盖，因此凭据 id 不变，历史流水与凭据的对应关系不断。
         *
         *     响应不回显明文，也不回行 id。归属 `merchant_id` 不存在返回 `code=400`。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        put: operations["updateAdminCredential"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/credentials/{id}/enable": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 启用上游凭据
         * @description 按主键置启用位，与 `tokenmp admin credential enable` 同一动作。动作幂等：重复调用返回成功，
         *     id 不存在返回 `code=404`。凭据轮换与误停后的恢复都靠它，不重写一行
         *     （重写会让凭据 id 与历史流水脱钩）。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        post: operations["enableAdminCredential"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/credentials/{id}/disable": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 停用上游凭据
         * @description 按主键置停用位，与 `tokenmp admin credential disable` 同一动作。动作幂等：重复调用返回成功，
         *     id 不存在返回 `code=404`。停用后的凭据不再被选路取用。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        post: operations["disableAdminCredential"];
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
        /**
         * 写入渠道模型映射
         * @description 写入或覆盖一条渠道模型映射，与 `tokenmp admin model-map set` 调用同一份业务实现。
         *     写入按 `(channel_id, model)` 幂等：同一组合重复写入只覆盖取值，不新增行。
         *     因此不返回行 id，写入后从清单取。改模型名本身或归属渠道走
         *     `/api/v1/admin/modelmaps/{id}`。
         *
         *     `price_multiplier` 缺省取 1；`request_overrides` 必须是 JSON 对象，
         *     转发时叠加到请求体上。`channel_id` 不存在返回 `code=400`。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        put: operations["setAdminModelMap"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/modelmaps/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * 修改模型映射
         * @description 改一条模型映射的归属渠道、模型名与转发取值。本端点按主键定位，因此模型名本身与
         *     归属渠道都可以改；与 `/api/v1/admin/modelmaps` 的分工是：那个按 `(channel_id, model)`
         *     这个自然键置位，用于「给这条渠道配上这个别名」，本端点用于改这一行。
         *
         *     启用位不在本端点：停用走 `/{id}/disable`，重新启用走覆盖写入。
         *     改成已存在的 `(channel_id, model)` 组合返回 `code=409`。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        put: operations["updateAdminModelMap"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/admin/modelmaps/{id}/disable": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 停用渠道模型映射
         * @description 按主键置停用位，与 `tokenmp admin model-map disable` 同一动作。动作幂等：重复调用返回成功，
         *     id 不存在返回 `code=404`。停用后该模型名不再路由到这条渠道。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        post: operations["disableAdminModelMap"];
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
    "/api/v1/admin/settlements": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 全平台结算对账单
         * @description 按商家逐行列出结算对账单，字段与 `tokenmp admin settlement list --json` 的行一致。
         *     `merchant_id` 限定单个商家；账期缺省时按各商家自己的账期取上一个完整自然周期 ——
         *     账期是商家自己的口径，同一批里各家的账期可以不同。
         *     请求方须持有管理面能力，越权返回 `code=403`。
         */
        get: operations["listAdminSettlements"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/partner/channels": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 上游渠道列表
         * @description 列出本商家登记的上游渠道，按主键升序。只返回渠道自身的字段，不含渠道级扩展配置
         *     （里面可能有上游请求头）。非本商家的渠道不出现。
         */
        get: operations["listPartnerChannels"];
        put?: never;
        /**
         * 登记上游渠道
         * @description 为本商家登记一条上游渠道，归属写死为会话推导出的商家，不接受归属参数。
         *     同名（同协议方言下同名）的渠道返回 `code=409`。新建一律启用，停用是独立动作。
         */
        post: operations["createPartnerChannel"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/partner/channels/{id}/enable": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 启用上游渠道
         * @description 启用一条属于本商家的渠道；已启用的渠道再次启用同样返回成功。
         *     不属于本商家的渠道返回 `code=404`，不以 403 区分存在性。
         */
        post: operations["enablePartnerChannel"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/partner/channels/{id}/disable": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 停用上游渠道
         * @description 停用一条属于本商家的渠道；已停用的渠道再次停用同样返回成功。停用不影响历史流水。
         *     不属于本商家的渠道返回 `code=404`，不以 403 区分存在性。
         */
        post: operations["disablePartnerChannel"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/partner/credentials": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 上游凭据列表
         * @description 列出本商家登记的上游凭据，按主键升序。只返回脱敏前缀，明文从不返回。
         *     非本商家的凭据不出现。
         */
        get: operations["listPartnerCredentials"];
        put?: never;
        /**
         * 登记上游凭据
         * @description 为本商家登记一份上游凭据，归属写死为会话推导出的商家。明文只在本次响应出现一次，
         *     之后任何端点都不再返回；列表只给出前缀。凭据按 `cred_group` 与渠道关联：
         *     同分组的渠道共享这一份凭据。
         */
        post: operations["createPartnerCredential"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/partner/credentials/{id}/enable": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 启用上游凭据
         * @description 启用一份属于本商家的凭据；已启用的凭据再次启用同样返回成功。
         *     不属于本商家的凭据返回 `code=404`，不以 403 区分存在性。
         */
        post: operations["enablePartnerCredential"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/partner/credentials/{id}/disable": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 停用上游凭据
         * @description 停用一份属于本商家的凭据；已停用的凭据再次停用同样返回成功。
         *     不属于本商家的凭据返回 `code=404`，不以 403 区分存在性。
         */
        post: operations["disablePartnerCredential"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/partner/usage/stats": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 名下调用量与应扣量聚合
         * @description 按维度把区间内本商家名下渠道的调用量与应扣量合计起来。口径与账户面的用量聚合一致：
         *     分组维度 `day` 取日期（`YYYY-MM-DD`，按写入时刻所在的自然日）、`model` 取客户端请求
         *     的模型名、`api_key` 取密钥 id 的十进制文本；合计与流水同源，只是作用域换成商家。
         *
         *     它只回答合计，不返回单条流水。
         */
        get: operations["getPartnerUsageStats"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/partner/settlement": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 分账对账单
         * @description 返回本商家一个账期的分账对账单：账期、成交笔数与四处金额（卖出总额、平台抽成、
         *     上游成本、商家收益）。作用域由会话推导，不接受商家参数；非本商家的数据不出现。
         *
         *     账期是左闭右开区间：`from` 与 `to` 一起给出时按该区间出账，都不给时按本商家的
         *     账期取上一个完整自然周期。金额是十进制字符串（金额 8 位小数、抽成率 4 位），
         *     口径见 `docs/compatibility.md` 的「分佣与结算口径」。
         */
        get: operations["getPartnerSettlement"];
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
        BusinessCode: 200 | 400 | 401 | 402 | 403 | 404 | 409 | 410 | 429 | 500;
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
        /** @description 一条上游渠道。不含商家标识与渠道级扩展配置。 */
        PartnerChannel: {
            /** Format: int64 */
            id: number;
            /** @description 渠道名，商家内用于区分同协议的多条渠道。 */
            name: string;
            /** @description 上游厂商标签，仅供展示与对账分组，不参与路由。 */
            vendor: string;
            /**
             * @description 协议方言。
             * @enum {string}
             */
            type: "openai_chat" | "openai_responses" | "anthropic_messages" | "gemini_generate";
            /** @description 凭据分组，与上游凭据的 `cred_group` 对应。 */
            cred_group: string;
            /** @description 上游根地址，端点段由协议决定。 */
            base_url: string;
            /** @description 路由排序，值大者优先。 */
            priority: number;
            /** @description 同优先级内的加权随机权重。 */
            weight: number;
            enabled: boolean;
        };
        PageOfPartnerChannel: {
            items: components["schemas"]["PartnerChannel"][];
        };
        /** @description 登记渠道的请求体；归属由会话推导，不接受商家参数。 */
        CreatePartnerChannelRequest: {
            name: string;
            vendor?: string;
            /** @enum {string} */
            type: "openai_chat" | "openai_responses" | "anthropic_messages" | "gemini_generate";
            cred_group: string;
            /** @description 上游根地址，只填到端点段之前。 */
            base_url: string;
            /**
             * @description 凭据注入形态；省略按协议现状。渠道级扩展配置（静态请求头等）属平台运维口径，
             *     不由商家自助填写。
             * @enum {string}
             */
            credential_style?: "authorization" | "x-api-key" | "x-goog-api-key" | "query";
            /** @description 路由排序，值大者优先；省略取 100。 */
            priority?: number;
            /** @description 同优先级内的加权随机权重；省略取 100。 */
            weight?: number;
        };
        /** @description 一份上游凭据。只有前缀，明文从不返回。 */
        PartnerCredential: {
            /** Format: int64 */
            id: number;
            cred_group: string;
            name: string;
            /** @description 脱敏前缀，足以区分凭据行、不足以还原明文。 */
            prefix: string;
            enabled: boolean;
        };
        /** @description 凭据的创建响应；`secret` 只在此处出现一次。 */
        CreatedPartnerCredential: {
            /** Format: int64 */
            id: number;
            cred_group: string;
            name: string;
            prefix: string;
            enabled: boolean;
            /** @description 凭据明文，仅本次响应返回；列表与后续读取只给前缀。 */
            secret: string;
        };
        PageOfPartnerCredential: {
            items: components["schemas"]["PartnerCredential"][];
        };
        /** @description 登记凭据的请求体；归属由会话推导，不接受商家参数。 */
        CreatePartnerCredentialRequest: {
            cred_group: string;
            name: string;
            /** @description 上游凭据明文，只在写入这一条路径出现。 */
            api_key: string;
        };
        /**
         * @description 一个商家一个账期的分账对账单，字段与 `settlement.BillView` 一致，只去掉商家标识 ——
         *     它就是会话本身。金额与抽成率是十进制字符串（金额 8 位小数、抽成率 4 位），
         *     口径见 `docs/compatibility.md` 的「分佣与结算口径」。
         */
        PartnerSettlement: {
            /**
             * @description 结算账期。
             * @enum {string}
             */
            period: "day" | "week" | "month";
            /**
             * Format: date-time
             * @description 账期起点（含）。
             */
            from: string;
            /**
             * Format: date-time
             * @description 账期终点（不含）。
             */
            to: string;
            /** @description 平台抽成率，十进制字符串，最多 4 位小数。 */
            commission_rate: string;
            /** @description 账期内的成交笔数。 */
            trades: number;
            /** @description 卖出总额（本商家名下账户的实付合计），十进制字符串。 */
            gross_sales: string;
            /** @description 平台抽成（卖出总额 × 抽成率），十进制字符串。 */
            commission: string;
            /** @description 上游成本（本商家名下渠道用量的牌价基础价合计），十进制字符串。 */
            upstream_cost: string;
            /** @description 商家收益（卖出总额 − 平台抽成 − 上游成本），十进制字符串。 */
            payout: string;
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
        /** @description 一个可购买的商品档位；数量与售价都是十进制字符串。 */
        Product: {
            /** Format: int64 */
            id: number;
            /** @description 档位名称，面向用户。 */
            name: string;
            /** @description 派生存量的结算单位 `currency` / `token` / `credit`。 */
            unit: string;
            /** @description 每份包含的数量，十进制字符串。 */
            qty: string;
            /** @description 售价，十进制字符串。 */
            price: string;
            /**
             * @description 该档位的可用模型范围；`null` 表示不限。空数组表示一个模型都不含，
             *     与 `null` 是两个不同的口径。
             */
            model_scope: string[] | null;
            /** @description 派生账本的有效天数；`0` 表示不过期。 */
            validity_days: number;
        };
        PageOfProduct: {
            items: components["schemas"]["Product"][];
        };
        /** @description 下单请求；账户归属由会话推导，不接受账户或商家参数。 */
        CreateOrderRequest: {
            /** Format: int64 */
            product_id: number;
            /** @description 购买份数，正数的十进制字符串。 */
            qty: string;
            /**
             * @description 客户端生成的幂等键。同一账户下同一键的重复提交只产生一笔订单，
             *     响应返回首次创建的那笔订单；键在账户内唯一，不同账户可以用同一个键。
             */
            idempotency_key: string;
        };
        /** @description 一笔订单；金额、数量与折算率都是十进制字符串，不经浮点。 */
        Order: {
            /** Format: int64 */
            id: number;
            /** Format: int64 */
            product_id: number;
            /** @description 购买时档位的名称。 */
            product_name: string;
            /** @description 派生存量的结算单位 `currency` / `token` / `credit`。 */
            unit: string;
            /** @description 购买份数，十进制字符串。 */
            qty: string;
            /** @description 实付金额，十进制字符串。 */
            price_paid: string;
            /** @description 本次购买派生的存量 = 每份数量 × 份数，十进制字符串。 */
            total: string;
            /** @description 购买时刻锁定的折算率 = 售价 / 每份数量，十进制字符串。 */
            unit_rate: string;
            /**
             * Format: date-time
             * @description 购买时刻。
             */
            purchased_at: string;
        };
        PageOfOrder: {
            items: components["schemas"]["Order"][];
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
        /** @description 一个商家，字段与 `tokenmp admin merchant list --json` 的行一致。 */
        AdminMerchant: {
            /** Format: int64 */
            id: number;
            /** @description 商家编码，平台内唯一。 */
            code: string;
            name: string;
            /**
             * @description 商家类型：`platform` 平台自营，`partner` 入驻商家。
             * @enum {string}
             */
            kind: "platform" | "partner";
            /** @description 商家状态：`active` / `disabled`。 */
            status: string;
            /**
             * Format: int64
             * @description 绑定的登录主体 id；未绑定时为 `null`（平台自营或尚未绑定）。
             */
            owner_user_id?: number | null;
            /** Format: date-time */
            created_at: string;
        };
        PageOfAdminMerchant: {
            items: components["schemas"]["AdminMerchant"][];
        };
        /** @description 新建商家的请求体。 */
        CreateAdminMerchantRequest: {
            /** @description 商家编码，平台内唯一；重复返 `code=409`。 */
            code: string;
            name: string;
            /** @enum {string} */
            kind: "platform" | "partner";
        };
        /** @description 绑定商家归属的请求体。 */
        SetAdminMerchantOwnerRequest: {
            /**
             * Format: int64
             * @description 登录主体 id；归属一对一，已被其它商家占用时返 `code=409`。
             */
            user_id: number;
        };
        /** @description 新建渠道的请求体。 */
        CreateAdminChannelRequest: {
            /**
             * Format: int64
             * @description 归属商家 id，取自商家清单。
             */
            merchant_id: number;
            name: string;
            /** @description 厂商标识，如 `openai` / `anthropic` / `gemini`；仅供展示与对账分组。 */
            vendor?: string;
            /**
             * @description 渠道的线协议类型。
             * @enum {string}
             */
            type: "openai_chat" | "openai_responses" | "anthropic_messages" | "gemini_generate";
            /** @description 凭据分组名；本端点只建立分组名，凭据另行写入。 */
            cred_group: string;
            /** @description 上游基地址，只填到端点段之前。 */
            base_url: string;
            /**
             * @description 凭据注入形态；省略时按协议现状注入。
             * @enum {string}
             */
            credential_style?: "authorization" | "x-api-key" | "x-goog-api-key" | "query";
            /**
             * @description 渠道级扩展配置原文：`headers` 是静态请求头（字符串到字符串的对象，不得占用
             *     网关自身的头名），其余键供探针声明。省略时无扩展配置。
             */
            config?: {
                [key: string]: unknown;
            };
            /** @description 选路优先级，值小者优先；省略取 100。 */
            priority?: number;
            /** @description 同优先级内的加权轮询权重；省略取 100。 */
            weight?: number;
        };
        /** @description 写入上游凭据的请求体。 */
        CreateAdminCredentialRequest: {
            /**
             * Format: int64
             * @description 归属商家 id。
             */
            merchant_id: number;
            /** @description 凭据分组，与渠道的 `cred_group` 对应。 */
            cred_group: string;
            name: string;
            /** @description 上游凭据明文，只在写入这一条路径出现；响应不回显。 */
            api_key: string;
        };
        /** @description 写入渠道模型映射的请求体；按 `(channel_id, model)` 幂等。 */
        SetAdminModelMapRequest: {
            /** Format: int64 */
            channel_id: number;
            /** @description 客户端请求的模型名。 */
            model: string;
            /** @description 转发给该渠道时替换成的模型名。 */
            upstream_model: string;
            /** @description 该映射的价格倍率，十进制字符串；省略取 1。 */
            price_multiplier?: string;
            /** @description 转发时叠加到请求体上的字段；省略时不叠加。 */
            request_overrides?: {
                [key: string]: unknown;
            };
        };
        /**
         * @description 修改商家的请求体。字段与新建相同，整体替换：`code`、`name`、`kind` 三个都要给。
         *     状态与归属不在请求体里，它们各自有端点。
         */
        UpdateAdminMerchantRequest: components["schemas"]["CreateAdminMerchantRequest"];
        /**
         * @description 修改渠道的请求体。字段与新建相同，整体替换：要保留的取值也要一并给出。
         *     启用位不在请求体里，它由 `/enable` 与 `/disable` 置位。
         */
        UpdateAdminChannelRequest: components["schemas"]["CreateAdminChannelRequest"];
        /**
         * @description 修改上游凭据的请求体。`api_key` 可以省略，表示保持现有明文不变；
         *     给出时在原行上覆盖。
         */
        UpdateAdminCredentialRequest: {
            /**
             * Format: int64
             * @description 归属商家 id。
             */
            merchant_id: number;
            /** @description 凭据分组，与渠道的 `cred_group` 对应。 */
            cred_group: string;
            name: string;
            /** @description 新的上游凭据明文；省略表示不改动现有明文。 */
            api_key?: string;
        };
        /** @description 写入动作的结果：受影响行的主键。 */
        AdminResourceID: {
            /** Format: int64 */
            id: number;
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
        /**
         * @description 一个商家的结算对账单，字段与 `tokenmp admin settlement list --json` 的行一致；
         *     比 PartnerSettlement 多 `merchant_id`：管理面是跨商家的清单，没有它分行无从归属。
         */
        AdminSettlementItem: {
            /**
             * Format: int64
             * @description 结算归属的商家 id。
             */
            merchant_id: number;
            /**
             * @description 结算账期。
             * @enum {string}
             */
            period: "day" | "week" | "month";
            /**
             * Format: date-time
             * @description 账期起点（含）。
             */
            from: string;
            /**
             * Format: date-time
             * @description 账期终点（不含）。
             */
            to: string;
            /** @description 平台抽成率，十进制字符串，最多 4 位小数。 */
            commission_rate: string;
            /** @description 账期内的成交笔数。 */
            trades: number;
            /** @description 卖出总额，十进制字符串。 */
            gross_sales: string;
            /** @description 平台抽成，十进制字符串。 */
            commission: string;
            /** @description 上游成本，十进制字符串。 */
            upstream_cost: string;
            /** @description 商家收益，十进制字符串。 */
            payout: string;
        };
        PageOfAdminSettlementItem: {
            items: components["schemas"]["AdminSettlementItem"][];
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
        /**
         * @description 账户无可用额度，`code=402`。与数据面同一判定（`settlement.Fundable`）：
         *     没有任何未过期且可扣的账本，也不存在可透支的货币账本。
         *     页面据此给出充值 / 购买引导（web/AGENTS.md 的三态）。
         */
        PaymentRequired: {
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
        /**
         * @description 账期起点（含），RFC3339。与 `to` 一起给出即按该区间出账；都不给时按商家的账期
         *     取上一个完整自然周期。只给一侧返回 `code=400`。
         */
        WindowFrom: string;
        /** @description 账期终点（不含），RFC3339。与 `from` 一起给出。 */
        WindowTo: string;
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
        /** @description 资源主键；不属于本商家的取值一律按不存在处理。 */
        ResourceID: number;
        /** @description 管理面对象主键。动作与修改都先确认这一行存在：id 不存在返回 `code=404`。 */
        AdminResourceID: number;
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
            402: components["responses"]["PaymentRequired"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    listUserProducts: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 商品目录。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfProduct"];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    listUserOrders: {
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
            /** @description 订单列表。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfOrder"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    createUserOrder: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CreateOrderRequest"];
            };
        };
        responses: {
            /** @description 订单；幂等命中时是首次创建的那笔。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["Order"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            404: components["responses"]["NotFound"];
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
    listAdminMerchants: {
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
            /** @description 商家清单。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfAdminMerchant"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
        };
    };
    createAdminMerchant: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CreateAdminMerchantRequest"];
            };
        };
        responses: {
            /** @description 新建成功，`data.id` 是新行主键。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["AdminResourceID"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            500: components["responses"]["InternalError"];
        };
    };
    updateAdminMerchant: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 管理面对象主键。动作与修改都先确认这一行存在：id 不存在返回 `code=404`。 */
                id: components["parameters"]["AdminResourceID"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["UpdateAdminMerchantRequest"];
            };
        };
        responses: {
            /** @description 已修改。 */
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
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            500: components["responses"]["InternalError"];
        };
    };
    enableAdminMerchant: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 管理面对象主键。动作与修改都先确认这一行存在：id 不存在返回 `code=404`。 */
                id: components["parameters"]["AdminResourceID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 已置位。 */
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
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            500: components["responses"]["InternalError"];
        };
    };
    disableAdminMerchant: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 管理面对象主键。动作与修改都先确认这一行存在：id 不存在返回 `code=404`。 */
                id: components["parameters"]["AdminResourceID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 已置位。 */
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
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            500: components["responses"]["InternalError"];
        };
    };
    setAdminMerchantOwner: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 管理面对象主键。动作与修改都先确认这一行存在：id 不存在返回 `code=404`。 */
                id: components["parameters"]["AdminResourceID"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SetAdminMerchantOwnerRequest"];
            };
        };
        responses: {
            /** @description 已绑定。 */
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
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
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
    createAdminChannel: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CreateAdminChannelRequest"];
            };
        };
        responses: {
            /** @description 新建成功，`data.id` 是新行主键。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["AdminResourceID"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            500: components["responses"]["InternalError"];
        };
    };
    updateAdminChannel: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 管理面对象主键。动作与修改都先确认这一行存在：id 不存在返回 `code=404`。 */
                id: components["parameters"]["AdminResourceID"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["UpdateAdminChannelRequest"];
            };
        };
        responses: {
            /** @description 已修改。 */
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
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            500: components["responses"]["InternalError"];
        };
    };
    enableAdminChannel: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 管理面对象主键。动作与修改都先确认这一行存在：id 不存在返回 `code=404`。 */
                id: components["parameters"]["AdminResourceID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 已置位。 */
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
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            500: components["responses"]["InternalError"];
        };
    };
    disableAdminChannel: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 管理面对象主键。动作与修改都先确认这一行存在：id 不存在返回 `code=404`。 */
                id: components["parameters"]["AdminResourceID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 已置位。 */
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
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
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
    createAdminCredential: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CreateAdminCredentialRequest"];
            };
        };
        responses: {
            /** @description 写入成功，`data.id` 是新行主键。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["AdminResourceID"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            500: components["responses"]["InternalError"];
        };
    };
    updateAdminCredential: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 管理面对象主键。动作与修改都先确认这一行存在：id 不存在返回 `code=404`。 */
                id: components["parameters"]["AdminResourceID"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["UpdateAdminCredentialRequest"];
            };
        };
        responses: {
            /** @description 已修改。 */
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
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            500: components["responses"]["InternalError"];
        };
    };
    enableAdminCredential: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 管理面对象主键。动作与修改都先确认这一行存在：id 不存在返回 `code=404`。 */
                id: components["parameters"]["AdminResourceID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 已置位。 */
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
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            500: components["responses"]["InternalError"];
        };
    };
    disableAdminCredential: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 管理面对象主键。动作与修改都先确认这一行存在：id 不存在返回 `code=404`。 */
                id: components["parameters"]["AdminResourceID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 已置位。 */
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
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
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
    setAdminModelMap: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SetAdminModelMapRequest"];
            };
        };
        responses: {
            /** @description 已写入。 */
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
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            500: components["responses"]["InternalError"];
        };
    };
    updateAdminModelMap: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 管理面对象主键。动作与修改都先确认这一行存在：id 不存在返回 `code=404`。 */
                id: components["parameters"]["AdminResourceID"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SetAdminModelMapRequest"];
            };
        };
        responses: {
            /** @description 已修改。 */
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
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            500: components["responses"]["InternalError"];
        };
    };
    disableAdminModelMap: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 管理面对象主键。动作与修改都先确认这一行存在：id 不存在返回 `code=404`。 */
                id: components["parameters"]["AdminResourceID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 已置位。 */
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
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
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
    listAdminSettlements: {
        parameters: {
            query?: {
                /** @description 页码，从 1 起；缺省 1。 */
                page?: components["parameters"]["Page"];
                /** @description 每页条数；缺省 20，上限 100。 */
                size?: components["parameters"]["Size"];
                /** @description 按商家过滤；缺省列出全部商家。 */
                merchant_id?: number;
                /**
                 * @description 账期起点（含），RFC3339。与 `to` 一起给出即按该区间出账；都不给时按商家的账期
                 *     取上一个完整自然周期。只给一侧返回 `code=400`。
                 */
                from?: components["parameters"]["WindowFrom"];
                /** @description 账期终点（不含），RFC3339。与 `from` 一起给出。 */
                to?: components["parameters"]["WindowTo"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 对账单清单。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfAdminSettlementItem"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["InternalError"];
        };
    };
    listPartnerChannels: {
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
            /** @description 渠道列表。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfPartnerChannel"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    createPartnerChannel: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CreatePartnerChannelRequest"];
            };
        };
        responses: {
            /** @description 登记成功，返回新建的渠道。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PartnerChannel"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    enablePartnerChannel: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 资源主键；不属于本商家的取值一律按不存在处理。 */
                id: components["parameters"]["ResourceID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 启用成功。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    disablePartnerChannel: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 资源主键；不属于本商家的取值一律按不存在处理。 */
                id: components["parameters"]["ResourceID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 停用成功。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    listPartnerCredentials: {
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
            /** @description 凭据列表。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PageOfPartnerCredential"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    createPartnerCredential: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CreatePartnerCredentialRequest"];
            };
        };
        responses: {
            /** @description 登记成功，响应携带一次性明文。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["CreatedPartnerCredential"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    enablePartnerCredential: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 资源主键；不属于本商家的取值一律按不存在处理。 */
                id: components["parameters"]["ResourceID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 启用成功。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    disablePartnerCredential: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 资源主键；不属于本商家的取值一律按不存在处理。 */
                id: components["parameters"]["ResourceID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 停用成功。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    getPartnerUsageStats: {
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
            403: components["responses"]["Forbidden"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
    getPartnerSettlement: {
        parameters: {
            query?: {
                /**
                 * @description 账期起点（含），RFC3339。与 `to` 一起给出即按该区间出账；都不给时按商家的账期
                 *     取上一个完整自然周期。只给一侧返回 `code=400`。
                 */
                from?: components["parameters"]["WindowFrom"];
                /** @description 账期终点（不含），RFC3339。与 `from` 一起给出。 */
                to?: components["parameters"]["WindowTo"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 该账期的对账单。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Envelope"] & {
                        data?: components["schemas"]["PartnerSettlement"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["InternalError"];
        };
    };
}
