# TokenMP

大模型 API 转发网关与计费平台：把客户端的模型请求转发到上游渠道，按用量计费并从账户余额扣款。
Go 单模块（`github.com/sumwai/tokenmp`），发布产物是一个二进制，前端静态产物另行部署。

## 能力概览

下表只是索引，行为口径与字段见「文档」一节的规范文件。

| 能力 | 说明 | 规范 |
|---|---|---|
| 数据面 | 四个转发端点：`POST /v1/chat/completions`、`POST /v1/responses`、`POST /v1/messages` 与 `POST /v1beta/models/{model}:generateContent`（含 `:streamGenerateContent`）；另有账户自助查询 `GET /v1/me/account` 与探活 `GET /healthz` | [docs/openapi.yaml](docs/openapi.yaml) |
| 协议互转 | 同一份内部请求/响应格式在四种方言之间转换，一条渠道因此可以承接任一方言的请求；含参数处理、模型名替换、跨协议降级与流式用量帧 | [docs/compatibility.md](docs/compatibility.md) |
| 鉴权与限额 | Bearer 鉴权、账户额度预检，以及账户与 API key 两个维度的窗口限额处置 | [docs/compatibility.md](docs/compatibility.md) 的错误码表 |
| 上游调度 | 候选链选路、同渠道换凭据重试与按类冷却、渠道级限流与熔断、上游套餐配额探针 | [docs/deploy.md](docs/deploy.md) |
| 商家分佣 | 按 `merchant.settle_info` 的抽成率与账期给入驻商家出账，对账单由卖出总额、平台抽成与上游成本构成；管理面与商家域各有一个读路径 | [docs/compatibility.md](docs/compatibility.md) |
| 网关中间件 | 本机可配置插件在四个时机改写请求体、流式内容事件与响应体，注册与启停用 `tokenmp admin plugin` | [docs/compatibility.md](docs/compatibility.md)、[examples/](examples/) |
| 页面通信 | `/api/v1/*` 供浏览器控制台调用，前端的响应类型与客户端由契约生成 | [docs/openapi-web.yaml](docs/openapi-web.yaml)、[web/AGENTS.md](web/AGENTS.md) |
| 日志 | 每次请求与每次上游尝试各写一条 JSON 日志到标准输出，凭据与密钥不进入日志 | [docs/deploy.md](docs/deploy.md) |

## 快速开始

迁移 SQL 已内嵌，运行只需一个可写的 MySQL 库，`serve` 启动时自动建表。

```sh
TOKENMP_MYSQL_DSN='user:password@tcp(db.example:3306)/tokenmp?parseTime=true' \
  ./tokenmp serve
```

默认监听 `:8080`，`GET /healthz` 固定返回 200。必填项、默认值、取值规则与 systemd 单元示例见
[docs/deploy.md](docs/deploy.md)。

## 构建与门禁

工具链版本由 `.mise.toml` 锁定，`make tools` 执行 `mise install` 装齐。

| 命令 | 作用 | 额外依赖 |
|---|---|---|
| `make check` | 编译、单测（`-race`）、静态检查、格式检查 | 无 |
| `make build-binary` | 产出 `bin/tokenmp` 并注入版本号 | 无 |
| `make check-integration` | 对真实 MySQL 验证迁移 | `TOKENMP_TEST_MYSQL_DSN` |
| `make e2e` | 端到端运营剧本：入驻到对账 | `TOKENMP_TEST_MYSQL_DSN` |
| `make web-install` / `web-gen` / `web-gen-check` / `web-lint` / `web-test` / `web-build` | 前端依赖、契约生成与漂移核对、静态检查、单测、构建，产物在 `web/dist` | node |

`make check` 的契约是无外部依赖。`check-integration` 与 `e2e` 的 DSN 须指向可丢弃的库，
未设置时测试跳过而不是失败。前端产物由部署侧取用，不进入二进制。开发门禁与提交规范见
[CONTRIBUTING.md](CONTRIBUTING.md)。

## 子命令

| 子命令 | 作用 |
|---|---|
| `serve` | 启动网关 HTTP 服务 |
| `admin` | 管理面：商家、渠道、账户、定价与充值 |
| `version` | 报出版本号、构建自哪个提交，以及运行时的 Go 版本 |
| `help` | 打印本帮助（`-h` / `--help` 同义） |

未知子命令或缺参数时退出码为 2，运行失败为 1。

## 管理面

`tokenmp admin <组> <动作>` 直连数据库执行运营动作（本地运维工具，不经网络鉴权），
数据库连接取自 `TOKENMP_MYSQL_DSN`；`plugin` 组只读写本机插件清单，不连库。组与动作：

| 组 | 动作 |
|---|---|
| `merchant` | `create` / `list` / `disable` / `set-owner` |
| `channel` | `create` / `list` / `enable` / `disable` |
| `credential` | `add` / `list` / `enable` / `disable` / `oauth-login` |
| `model-map` | `set` / `list` / `disable` |
| `account` | `create` / `list` / `disable` / `set-multiplier` / `set-merchant` |
| `key` | `issue` / `list` / `revoke` |
| `bucket` | `credit` / `list` |
| `product` | `create` / `list` |
| `purchase` | `buy` / `list` |
| `price` | `publish` / `list` |
| `rule` | `add` / `list` / `del` |
| `calendar` | `import` / `list` |
| `usage` | `list` |
| `adjust` | `add` / `list` |
| `quota` | `add` / `list` / `del` / `reset` |
| `plan` | `add` / `list` |
| `settlement` | `set` / `get` / `list` |
| `plugin` | `add` / `list` / `enable` / `disable` / `del` / `check` |

```sh
$ ./bin/tokenmp admin merchant create --code partner-1 --name 入驻 --kind partner
$ ./bin/tokenmp admin settlement set --merchant 2 --commission-rate 0.1 --period month
$ ./bin/tokenmp admin settlement list --merchant 2
$ ./bin/tokenmp admin key issue --account 1
$ ./bin/tokenmp admin usage list --account 1 --json
```

所有 `list` 动作输出对齐的纯文本表格，加 `--json` 输出机器可读格式；用法错误退出码为 2，
运行失败为 1。`key issue` 的明文只在签发那一次输出并标注「仅此一次」，库中只存哈希与前缀。
各动作的参数与输出形态见 [docs/operations.md](docs/operations.md) 的运营剧本。

## 目录结构

```
cmd/tokenmp/            单一入口二进制：子命令分发、参数解析与输出
internal/domain/        协议无关的内部统一请求/响应与端口定义
internal/adapters/      四种线协议与内部统一格式的双向转换
internal/pipeline/      核心转发流水线：选路、请求定稿、上游调用与候选回退
internal/transport/     共用 HTTP 入口与 SSE 逐帧读取
internal/upstream/      调用上游渠道的 HTTP 客户端
internal/credential/    按路由引用取凭据、轮换并拼装上游请求头
internal/oauth/         订阅型上游的 OAuth 协议交互：刷新、设备码轮询与授权码交换
internal/access/        客户端鉴权、额度预检与限额处置
internal/apikey/        客户端 API 密钥的生成、展示前缀与存储哈希
internal/auth/          页面账号体系：注册、登录、会话签发与销毁
internal/identity/      页面身份与能力的唯一出处：身份叠加展开与能力并集
internal/me/            账户自助查询端点：摘要、包存量、限额窗口与最近流水
internal/user/          /api/v1/user/* 用户级业务端点与控制台清单
internal/partner/       /api/v1/partner/* 商家域端点：上游账号、名下用量与分账对账单
internal/adminapi/      /api/v1/admin/* 管理面只读清单端点
internal/webapi/        页面通信的公共部分：六字段信封与会话令牌解析
internal/route/         选路候选链：加权随机首选与同协议／跨协议分段拼链
internal/failure/       上游失败处理的唯一出处：诊断、处置与动作
internal/observability/ 请求日志与尝试日志的字段拼装
internal/requestlog/    请求记录落库：请求记录主表与尝试时间线
internal/usage/         用量落库编排：归属补全、request 分量与结算接入
internal/ratelimit/     渠道级进程内限流：令牌桶与并发位
internal/circuit/       渠道级进程内熔断：连续失败隔离与半开探测
internal/billing/       计费领域的公共定义：计费指标与用量映射
internal/quota/         窗口限额判定：窗口计算与超限比较
internal/plan/          上游套餐与配额：耗尽判定、声明式探针与周期采集
internal/settlement/    用量结算与商家分佣：定价解析、倍率链、账本扣减与账期出账
internal/gateway/       转发网关装配：把适配器、凭据、上游、流水线、入口与鉴权接起来
internal/store/         MySQL 连接、迁移与 schema 读写
internal/admin/         管理面业务层：商家、渠道、账户、定价与充值
internal/plugin/        网关中间件：moejs 沙箱加载、四个钩子与进程内统计
internal/config/        环境变量到运行配置
web/                    浏览器控制台前端（React + Vite + TypeScript），产物 web/dist
examples/               可运行的中间件示例，由 internal/plugin 的用例加载以避免与文档漂移
docs/                   数据面、页面通信、兼容性与计费口径、部署、运营五份规范
.github/workflows/      CI 与发布链路
```

## 文档

| 文件 | 内容 |
|---|---|
| [docs/openapi.yaml](docs/openapi.yaml) | 数据面 OpenAPI 3.1 规范：端点、请求/响应 schema、SSE、错误体与状态码 |
| [docs/openapi-web.yaml](docs/openapi-web.yaml) | 页面通信 OpenAPI 3.1 规范：信封字段、业务码与会话流程 |
| [docs/compatibility.md](docs/compatibility.md) | 参数处理、模型名替换、跨协议降级、流式用量帧、用量计费口径与错误码表 |
| [docs/deploy.md](docs/deploy.md) | 部署形态、环境变量全表、启动与迁移、优雅关闭、systemd 单元示例 |
| [docs/operations.md](docs/operations.md) | 运营剧本：入驻到对账的命令序列，与端到端剧本互引 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 开发门禁与提交规范 |
| [RELEASE.md](RELEASE.md) | 发布链路与本地验证 |
| [CHANGELOG.md](CHANGELOG.md) | 由 release-please 维护的变更记录 |

## 版本

`tokenmp version` 报出三条事实，取不到的整行省略：

```
$ ./bin/tokenmp version
tokenmp v0.1.0 (go go1.27.1)
提交 ef2b353
构建 2026-10-05T06:33:15Z
```

版本号取构建期注入值，未注入时退化为提交短哈希，都取不到时为 `dev`；提交行来自二进制内嵌的
VCS 信息；构建日期由 GoReleaser 注入，本机构建没有这一行。版本号随 tag 发布，产物挂在
GitHub Release 页面，变更记录见 [CHANGELOG.md](CHANGELOG.md)。

## 贡献

开发门禁与提交规范见 [CONTRIBUTING.md](CONTRIBUTING.md)，发布流程见 [RELEASE.md](RELEASE.md)。

## License

MIT，见 [LICENSE](LICENSE)。
