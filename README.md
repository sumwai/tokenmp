# TokenMP

大模型 API 转发网关与计费平台：把客户端的模型请求转发到上游渠道，按用量计费并从账户余额扣款。

## 快速开始

需要 Go 1.27 及以上。

```
make check              # 编译、单测（-race）、静态检查、格式检查
make check-integration  # 对真实 MySQL 跑迁移验证（需 TOKENMP_TEST_MYSQL_DSN）
make e2e                # 端到端运营剧本：入驻到对账全流程（需 TOKENMP_TEST_MYSQL_DSN）
make build-binary       # 产出 bin/tokenmp 并注入版本号
./bin/tokenmp version
```

`make check` 只依赖 Go 与 golangci-lint，不需要 docker、数据库或 node。
`make check-integration` 与 `make e2e` 需要指向可丢弃库的 DSN，见目标注释。

工具链版本由 `.mise.toml` 锁定，`make tools` 执行 `mise install` 装齐。

## 子命令

```
$ ./bin/tokenmp help
用法：tokenmp <子命令>

子命令：
  serve      启动网关 HTTP 服务
  admin      管理面：商家、渠道、账户、定价与充值
  version    报出版本号、构建自哪个提交，以及运行时的 Go 版本
  help       打印本帮助
```

未知子命令或缺参数时退出码为 2，运行失败为 1。

`tokenmp serve` 从环境变量读取运行配置，启动时执行数据库迁移，暴露
`GET /healthz`（200）与三个转发端点 `POST /v1/chat/completions`、
`POST /v1/responses`、`POST /v1/messages`，收到退出信号后优雅关闭。
对外契约见 [docs/openapi.yaml](docs/openapi.yaml)，参数取舍、跨协议降级与用量计费口径见
[docs/compatibility.md](docs/compatibility.md)。
转发端点要求 `Authorization: Bearer <key>`。鉴权通过后先做额度预检与窗口限额
判定：无可用额度回 402；账户或 API key 维度的窗口用量达到限额时按限额处置回 429，
`reject` 的错误码为 `quota_exceeded`，`throttle` 为 `rate_limited` 并附 `Retry-After`。

| 环境变量 | 语义 | 默认值 |
|---|---|---|
| `TOKENMP_MYSQL_DSN` | 必填，MySQL 连接串 | 无 |
| `TOKENMP_LISTEN` | 监听地址 | `:8080` |
| `TOKENMP_MYSQL_MAX_OPEN_CONNS` | 数据库连接池上限 | `25` |
| `TOKENMP_MYSQL_MAX_IDLE_CONNS` | 数据库空闲连接数 | `5` |
| `TOKENMP_MYSQL_CONN_MAX_LIFETIME` | 数据库连接最长存活时长 | `5m` |
| `TOKENMP_UPSTREAM_COMPLETE_TIMEOUT` | 非流式请求整体超时 | `120s` |
| `TOKENMP_UPSTREAM_STREAM_FIRST_BYTE_TIMEOUT` | 流式等待上游首字节 | `30s` |
| `TOKENMP_UPSTREAM_STREAM_IDLE_TIMEOUT` | 流式两帧之间的最大间隔 | `60s` |
| `TOKENMP_UPSTREAM_MAX_IDLE_CONNS` | 上游连接池空闲连接总数 | `100` |
| `TOKENMP_UPSTREAM_MAX_IDLE_CONNS_PER_HOST` | 上游连接池每主机空闲连接数 | `32` |
| `TOKENMP_UPSTREAM_IDLE_CONN_TIMEOUT` | 上游空闲连接回收时长 | `90s` |
| `TOKENMP_USAGE_WRITE_TIMEOUT` | 写用量流水超时 | `5s` |
| `TOKENMP_UPSTREAM_CREDENTIAL_COOLDOWN` | 上游凭据类失败后的冷却时长 | `60s` |
| `TOKENMP_UPSTREAM_RATE_LIMIT_WAIT` | 渠道限流下等待令牌的最长时间 | `2s` |
| `TOKENMP_UPSTREAM_BREAKER_THRESHOLD` | 渠道连续失败多少次后熔断打开 | `5` |
| `TOKENMP_UPSTREAM_BREAKER_COOLDOWN` | 渠道熔断打开后多久允许一笔探测 | `30s` |
| `TOKENMP_UPSTREAM_BREAKER_PROBE_CONCURRENCY` | 半开态同时放行的探测条数 | `1` |

超时与连接池取值必须为正数或合法时长，非法取值在启动前报错并以 1 退出。
流式转发不设整体超时，只受首字节与空闲读两级约束。

同一分组下的多条启用上游凭据按轮换顺序取用；上游拒绝凭据（401 / 403 等）时，
在同一条渠道内换下一条凭据重试，失败的那条进入冷却（内存态，重启即重置）。

渠道的 `rate_limit_qps` 与 `rate_limit_concurrency` 在网关侧执行：进入渠道尝试前
取令牌与并发位，令牌不足时短暂等待，超过等待上限则按可重试失败换下一条候选。
流式请求占并发位到流终态才释放。限流状态是进程内的，多实例部署时各实例独立计数。

渠道连续出现上游硬故障（5xx、连接失败、上游超时）达到阈值时进入熔断：冷却期内该渠道
不参与调度，冷却期后放行探测，成功恢复闭合、失败重新打开。上游限流、上游 4xx 拒绝、
客户端取消与本端写出失败不计入熔断。全部候选都处于打开态时放行其中一条探测一次，
而不是直接把请求判为失败。熔断状态是进程内的，多实例部署时各实例独立计数。

选路按两级取候选：与客户端方言一致的渠道成段在前，不限方言的渠道成段在后，回退按链上顺序推进。
同协议候选始终排在跨协议候选之前；跨协议段排除已在同协议段出现过的渠道。
两级都无候选时仍回 404。
尝试预算按段计量：同协议段与跨协议段各有额度，默认分别为 2 次与 1 次，
同协议候选全部失败不会挤掉跨协议降级的机会；总次数另设防御性上限。

每请求写一条 JSON 请求日志到标准输出：请求 id（客户端带 `X-Request-Id` 时沿用）、
协议方言、请求模型名、命中的渠道 id、上游状态码、是否跨协议重建与耗时。凭据与密钥不进入日志。

每次上游尝试另写一条 JSON 尝试日志：同一请求 id、尝试序号、渠道 id、协议方言、
上游状态码、耗时、是否重试、是否换渠道、是否跨协议重建、错误码与已取得的用量分量。
被熔断跳过的候选也各写一条 `outcome=skipped` 的记录，说明该候选未发起上游调用。
一次请求发生重试或换渠道时会留下多行，与请求日志按请求 id 关联。凭据与密钥不进入日志。

## 管理面

`tokenmp admin <组> <动作>` 直连数据库执行运营动作（本地运维工具，不经网络鉴权），
数据库连接取自 `TOKENMP_MYSQL_DSN`。组与动作：

| 组 | 动作 |
|---|---|
| `merchant` | `create` / `list` / `disable` |
| `channel` | `create` / `list` / `enable` / `disable` |
| `credential` | `add` / `list` / `disable` |
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

```
$ ./bin/tokenmp admin merchant create --code partner-1 --name 入驻 --kind partner
$ ./bin/tokenmp admin account create --code acct-1 --name 账户
$ ./bin/tokenmp admin key issue --account 1
$ ./bin/tokenmp admin price publish --merchant 1 --model glm-5 \
    --component input_token:0.27:currency:1000000
$ ./bin/tokenmp admin usage list --account 1 --json
```

所有 `list` 动作输出对齐的纯文本表格，加 `--json` 输出机器可读格式。
`key issue` 的明文只在签发那一次输出并标注「仅此一次」，库中只存哈希与前缀；
凭据与密钥的明文不进入 `list` 输出。用法错误退出码为 2，运行失败为 1。

`calendar import` 从标准输入或 `--file` 读 `日期,day_kind` 行；`price publish` 的
`--component` 形如 `metric:price:unit_settle:qty`，可重复。

`quota add` 的 `--scope` 取规则范围、`--metric` 取计费指标、`--window` 取
`rolling` / `calendar`、`--period` 取 `5h` / `day` / `week` / `month` / `total`、
`--action` 取 `reject` / `throttle`；`quota list` 附当前窗口已用量与剩余额度，
可按 `--scope` 与 `--scope-id` 过滤或按 `--account` 列出某账户的限额，不填列出全部；
`quota reset` 在指定限额上追加一条重置基准，`--reason` 与 `--operator` 必填。

## 目录结构

```
cmd/tokenmp/         单一入口二进制，子命令按文件拆分
internal/domain/     协议无关的内部统一请求/响应与端口定义
internal/adapters/   三种线协议与内部统一格式的双向转换
internal/pipeline/   核心转发流水线：选路、请求定稿、上游调用与候选回退
internal/transport/  共用 HTTP 入口与 SSE 逐帧读取
internal/upstream/   调用上游渠道的 HTTP 客户端
internal/credential/ 按路由引用取凭据、轮换并拼装上游请求头
internal/ratelimit/  渠道级进程内限流：令牌桶与并发位
internal/circuit/    渠道级进程内熔断：连续失败隔离与半开探测
internal/store/      MySQL 连接、迁移与 schema 读写
internal/admin/      管理面业务层：商家、渠道、账户、定价与充值
internal/billing/    计费指标与用量映射
internal/quota/      窗口限额判定：窗口计算与超限比较
internal/config/     环境变量到运行配置
pkg/                 可被外部导入的包
.github/workflows/   CI 与发布链路
```

## 文档

| 文件 | 内容 |
|---|---|
| [docs/openapi.yaml](docs/openapi.yaml) | 数据面 OpenAPI 3.1 规范：端点、请求/响应 schema、SSE、错误体与状态码 |
| [docs/compatibility.md](docs/compatibility.md) | 三方言参数处理、模型名替换、跨协议降级、流式用量帧与用量计费口径 |
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

- **版本号**：构建期注入值 → 未注入时退化为提交短哈希 → 都没有时为 `dev`
- **提交行**：来自二进制内嵌的 VCS 信息，取不到时整行省略
- **构建日期**：GoReleaser 注入，本机构建没有这一行

版本号随 tag 发布，产物挂在 GitHub Release 页面，变更记录见 [CHANGELOG.md](CHANGELOG.md)。

## 贡献

开发门禁与提交规范见 [CONTRIBUTING.md](CONTRIBUTING.md)，发布流程见 [RELEASE.md](RELEASE.md)。

## License

MIT，见 [LICENSE](LICENSE)。
