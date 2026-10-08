# 用户级端点实现交接

本文件只存在于 `handoff/user-endpoints` 分支，用于交接进行中的工作，不作为仓库文档合并到 `main`。

## 背景

用户侧端点契约定义在 `docs/openapi-web.yaml`，随 #112 合并。契约覆盖 9 个端点，实现分块推进，每块一个 PR。已合并两块，剩余三块。

## 已完成

### 骨架与账户概览（#116，已合并）

| 文件 | 内容 |
|---|---|
| `internal/webapi/envelope.go`（新） | 六字段信封、页面业务码常量、`WriteOK` / `WriteError` / `WritePage` / `WriteEnvelope` |
| `internal/webapi/auth.go`（新） | `BearerToken` |
| `internal/auth/envelope.go` | 改为转发到 `webapi`，保留包内名字，调用点不变 |
| `internal/auth/handler.go` | `bearerToken` 转发到 `webapi.BearerToken` |
| `internal/auth/service.go` | 新增 `SessionUserID(ctx, accessToken) (uint64, error)` |
| `internal/store/admin.go` | 新增 `AccountByOwner` |
| `internal/user/user.go`（新） | 路径常量、`Sessions` / `Store` / `SummaryReader` 接口、`Options`、`Handler` |
| `internal/user/handler.go`（新） | 路径分发、`currentAccount`、账户概览、`parseRecent` |
| `internal/gateway/gateway.go` | 挂载 `/api/v1/user/` |

测试：`internal/user/handler_test.go`、`internal/gateway/user_test.go`、`internal/store/migrate_integration_test.go` 中的归属断言。

### 密钥自助管理（#118，已合并）

| 文件 | 内容 |
|---|---|
| `internal/apikey/apikey.go`（新） | `Generate` / `Prefix` / `Hash`，密钥规则唯一实现 |
| `internal/access/auth.go` | `HashAPIKey` 转发到 `apikey.Hash` |
| `internal/admin/account.go` | 改用 `apikey.*`，删除 `generateAPIKey` / `hashAPIKey`；`format.go` 删除 `plaintextPrefix` |
| `internal/store/admin.go` | `scanAPIKeys`（抽取）、`ListAPIKeysByAccount`、`APIKeyByID` |
| `internal/user/keys.go`（新） | 密钥列出、创建、吊销 |

测试：`internal/apikey/apikey_test.go`、`internal/user/keys_test.go`、store 集成测试中的分页与跨账户隔离断言。

## 待办

### 一、`GET /api/v1/user/usage`

契约字段：`id`、`model`、`upstream_model`、`protocol`、`cross_protocol`、`usage`、`charged_amount`、`created_at`。

落差：`billing_usage` 只有 `model` 列且存的是实际履约模型，没有 `requested_model` / `protocol` / `cross_protocol`。`settlement.Input` 有 `RequestedModel`，但落库时被丢弃；`protocol` 与 `cross_protocol` 需要从 `pipeline` 传进 `domain.UsageRecord`。

三个处理方式：

1. 迁移给 `billing_usage` 加三列，`UsageRecord` 扩展并写入。历史行为 NULL，契约不动。
2. **倾向**：只加 `requested_model`；契约的 `UsageItem` 删掉 `protocol` 与 `cross_protocol`，`model` 保持「实际履约模型」并新增 `requested_model`。理由：协议与降级属于请求级观测，`/user/requests` 已定义这两个字段。
3. 完全不动表，契约把 `model` 改成「实际履约模型」并删掉另外两个，别名映射前后的对照信息一并失去。

选 2 或 3 会改动已合并的契约，需要先确认。

查询需要新 store 方法：按账户 + 时间区间 + 模型 + `api_key_id` 分页。`billing_usage` 已有 `idx_usage_account_time`，`0004` 又加了 `idx_usage_api_key_time`。

### 二、`GET /api/v1/user/models`

按账户可见范围列出模型名与支持的线协议。数据源是 `upstream_model_map` 与渠道类型（`upstream_channel.channel_type`）。契约 `ModelInfo` 只要求 `name` 与 `protocols`，不含单价。

### 三、请求记录：`GET /user/requests`、`/user/requests/{id}`、`/user/requests/stats`

最重的一块，需要新建三样东西：

- `request_log` 热表（保留 31 天）
- 按天 × 模型 × 状态的聚合缓存（供 `stats` 在明细归档后仍可回答）
- 采集端脱敏器：保留键名、嵌套结构与参数白名单真值，用户内容替换为 `{"__redacted": "<类型>", "len": <长度>}`，数组保留元素形状并在超过采样上限时补 `__total`
- 报文存储：本地文件 + gzip + 按日期分目录（`<data-dir>/requests/<yyyy-mm-dd>/<request_id>.json.gz`），读写经接口抽象以便替换为对象存储；只存失败请求，保留 7 天
- 归档：热表超期明细导出冷介质

现有可复用的采集点，字段已经在手：

| 来源 | 粒度 | 字段 |
|---|---|---|
| `transport.AccessRecord` → `observability.accessLogger` | 每次客户端请求一行 | `request_id`、`protocol`、`model`、`channel_id`、`upstream_status`、`cross_protocol`、`stream`、`http_status`、`duration_ms`、`written_bytes`、`error_code`、`remote_addr`、`user_agent` |
| `domain.AttemptRecord` → `observability.attemptObserver` | 每次上游尝试一行 | 上述之外加 `failure_class`、`error_code`、`error_detail`、`usage`、`rate_limit_wait_ms`、`rewritten_parts` |

两条链路目前都只写 JSON 日志文件，不入库。写入点应在请求终态、与访问日志同一时机，归属从 `access.IdentityFromContext` 取。

## 代码导航

| 关注点 | 位置 |
|---|---|
| 用户业务面 | `internal/user` |
| 页面信封与 Bearer 解析 | `internal/webapi` |
| 密钥生成规则 | `internal/apikey` |
| 会话解析为登录主体 | `auth.Service.SessionUserID` |
| 会话推导账户 | `store.AccountByOwner` |
| 密钥数据面 | `store.ListAPIKeysByAccount` / `APIKeyByID` / `InsertAPIKey` / `SetAPIKeyEnabled` |
| 账户摘要口径 | `internal/me` 的 `Service.Summary`（`apiKeyID` 传 0 表示只看账户维度） |

## 装配与扩展注意

- 新增 store 方法后，`internal/gateway` 的 `gatewayStore` 接口要同步加方法，且 `internal/gateway` 的 `fakeGatewayStore` 必须实现（缺口补在 `webauth_fake_test.go` 或对应 `*_test.go`）
- `internal/user.Store` 接口同样要扩展，`internal/user` 的 `fakeStore`（在 `handler_test.go`）同步实现
- `internal/user` 的路径分发在 `Handler.ServeHTTP` 的 `switch`，新增端点要在那里登记；动态段用 `parseRevokePath` 这类显式解析，未匹配一律回页面信封 404
- 契约里新增或变更端点，`docs/openapi-web.yaml` 与实现在同一 PR 内同步；`docs/openapi-web.yaml` 是页面通信的单一事实源

## 验证方式

```
make check                 # 编译、单测（-race）、golangci-lint、格式检查
```

集成与端到端需要可丢弃的数据库：

```
TOKENMP_TEST_MYSQL_DSN='<user>:<pass>@tcp(<host>:<port>)/<db>?parseTime=true' make check-integration
TOKENMP_TEST_MYSQL_DSN='<user>:<pass>@tcp(<host>:<port>)/<db>?parseTime=true' make e2e
```

两者都会先删表再重建，DSN 必须指向可丢弃的库；未设置该变量时相关用例跳过而非失败。

前端：

```
make web-lint && make web-test && make web-build
```

## 契约与前端状态

- `docs/openapi-web.yaml` 已定义全部 9 个用户级端点，含请求记录的可见性分层与报文脱敏规则
- `web/AGENTS.md` 的分页规范已改为偏移（信封的 `page` / `size` / `total`），与契约一致
- 前端尚未接入任何用户级端点，`web/src` 目前只有认证页

## 提交规范

Conventional Commits；改动走分支 + PR，`main` 有分支保护。合并前置：`make check` 全绿、CI 与 PR 标题检查通过。
