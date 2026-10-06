# 部署与运行配置

TokenMP 的部署形态是单个二进制加一个 MySQL 库。二进制承担数据面（`serve`）与管理面
（`admin`）两条路径，运行配置只来自环境变量，单一入口在 `internal/config`。

## 产物

```
make build-binary   # 产出 bin/tokenmp，并注入版本号
./bin/tokenmp version
```

版本注入与退化的取值规则见 [README](../README.md) 的「版本」一节。

迁移 SQL 经 `embed` 内嵌进二进制（`internal/store/migrations`），随二进制一同发布。
部署时只需分发二进制，不必额外同步一份 SQL 目录，也不会出现二进制版本与 SQL 版本不一致。

## MySQL

需要一个可写的库，连接串经 `TOKENMP_MYSQL_DSN` 传入，由 `go-sql-driver/mysql` 解析。
`parseTime` 开启与否都能工作：未开启时存储层自己把 `DATE` / `DATETIME` / `TIME` 列按
文本形态解析。`serve` 与 `admin` 都读取同一个 DSN。

```sh
TOKENMP_MYSQL_DSN='user:password@tcp(db.example:3306)/tokenmp?parseTime=true' \
  ./bin/tokenmp serve
```

表由迁移建立，使用 `InnoDB` 与 `utf8mb4`，全库不建外键。

## 环境变量全表

逐条对应 `internal/config/config.go` 的读取逻辑。`必填` 指缺失即启动失败，其余项在缺失时
取默认值。

| 环境变量 | 必填 | 默认值 | 单位 | 语义 |
|---|---|---|---|---|
| `TOKENMP_MYSQL_DSN` | 是 | 无 | — | MySQL 连接串；缺失或空串报「配置缺失」并以 1 退出 |
| `TOKENMP_LISTEN` | 否 | `:8080` | `host:port` | 监听地址；空串与只含空白视为未配置 |
| `TOKENMP_MYSQL_MAX_OPEN_CONNS` | 否 | `25` | 条 | 数据库连接池上限 |
| `TOKENMP_MYSQL_MAX_IDLE_CONNS` | 否 | `5` | 条 | 数据库空闲连接数 |
| `TOKENMP_MYSQL_CONN_MAX_LIFETIME` | 否 | `5m` | 时长 | 数据库连接最长存活时长 |
| `TOKENMP_UPSTREAM_COMPLETE_TIMEOUT` | 否 | `120s` | 时长 | 非流式请求整体超时 |
| `TOKENMP_UPSTREAM_STREAM_FIRST_BYTE_TIMEOUT` | 否 | `30s` | 时长 | 流式等待上游首字节 |
| `TOKENMP_UPSTREAM_STREAM_IDLE_TIMEOUT` | 否 | `60s` | 时长 | 流式两帧之间的最大间隔 |
| `TOKENMP_UPSTREAM_MAX_IDLE_CONNS` | 否 | `100` | 条 | 上游连接池空闲连接总数 |
| `TOKENMP_UPSTREAM_MAX_IDLE_CONNS_PER_HOST` | 否 | `32` | 条 | 上游连接池每主机空闲连接数 |
| `TOKENMP_UPSTREAM_IDLE_CONN_TIMEOUT` | 否 | `90s` | 时长 | 上游空闲连接回收时长 |
| `TOKENMP_USAGE_WRITE_TIMEOUT` | 否 | `5s` | 时长 | 写用量流水超时 |
| `TOKENMP_UPSTREAM_CREDENTIAL_COOLDOWN` | 否 | `60s` | 时长 | 上游凭据类失败后的冷却时长 |
| `TOKENMP_UPSTREAM_OAUTH_REFRESH_WINDOW` | 否 | `5m` | 时长 | 订阅型凭据的提前续期窗口：访问令牌剩余有效期不足该值时先续期 |
| `TOKENMP_UPSTREAM_OAUTH_REFRESH_TIMEOUT` | 否 | `15s` | 时长 | 单次 OAuth 续期与等待续期的上限 |
| `TOKENMP_UPSTREAM_RATE_LIMIT_WAIT` | 否 | `2s` | 时长 | 渠道限流下等待令牌的最长时间 |
| `TOKENMP_UPSTREAM_BREAKER_THRESHOLD` | 否 | `5` | 次 | 渠道连续失败多少次后熔断打开 |
| `TOKENMP_UPSTREAM_BREAKER_COOLDOWN` | 否 | `30s` | 时长 | 渠道熔断打开后多久允许一笔探测 |
| `TOKENMP_UPSTREAM_BREAKER_PROBE_CONCURRENCY` | 否 | `1` | 条 | 半开态同时放行的探测条数 |
| `TOKENMP_UPSTREAM_PROBE_INTERVAL` | 否 | `5m` | 时长 | 上游套餐探针的采集周期；快照超过它的两倍视为未知 |
| `TOKENMP_PLUGIN_FILES` | 否 | 空（禁用） | 文件或包目录列表 | 逗号分隔的中间件文件或包目录；每项去空白，空值表示禁用插件层 |

取值规则：

- `TOKENMP_MYSQL_MAX_OPEN_CONNS`、`TOKENMP_MYSQL_MAX_IDLE_CONNS` 与
  `TOKENMP_MYSQL_CONN_MAX_LIFETIME` 填 `0` 视为未配置，回落到上表默认值；负数报错。
- 其余数值与时长项必须为正：填 `0` 或负数在启动前报错。
- 时长按 Go 的 `time.ParseDuration` 解析（`120s`、`5m` 等）；非法取值在启动前报错。
- `TOKENMP_PLUGIN_FILES` 按逗号切分并逐项去空白，空项丢弃，全为空等同于未配置。
  每项的存在性、扩展名与能否编译在插件层加载时校验，非法项在启动前报错。
- 非法取值一律以退出码 1 终止，进程不带着半份配置起服。

各变量的运行语义（超时分级、限流、熔断、凭据轮换与 OAuth 续期、上游套餐探针、网关中间件）
见 [README](../README.md) 的转发行为说明与 [docs/compatibility.md](compatibility.md)。

## 启动与迁移

`tokenmp serve` 的启动顺序：

1. 读环境变量并校验，配置非法即报错退出。
2. 打开 MySQL 连接池并做一次 `Ping`，不可达即失败。
3. 执行迁移：按 `schema_migrations` 跳过已应用版本，逐条执行未应用的 DDL。
4. 装配网关：协议适配器、凭据轮换、上游客户端、渠道限流、熔断、中间件插件、上游套餐采集、流水与鉴权。
5. 监听 `TOKENMP_LISTEN`。

`TOKENMP_PLUGIN_FILES` 里的中间件在装配期编译并校验，任一项不可用即启动失败；
套餐采集器在 serve 启动后立即采一轮，随后按 `TOKENMP_UPSTREAM_PROBE_INTERVAL` 周期执行。

迁移在启动时执行，表结构随二进制一同对齐，避免新版本对着旧表运行。迁移可重入：DDL 使用
`CREATE TABLE IF NOT EXISTS`，版本登记用 `ON DUPLICATE KEY UPDATE` 去重，多实例并发启动
同一库不会互相中断。

`tokenmp admin` 直连数据库执行运营动作，**不跑迁移**。迁移是改 schema 的写操作，应由 `serve`
启动或部署流程显式触发，不该由一次运维查询的副作用完成。

## 优雅关闭

`serve` 收到 `SIGINT` 或 `SIGTERM` 后停止接受新连接，并等待在途请求完成，最长 `15s`；
到期仍未完成的连接被关闭，对应的上游调用随请求取消释放。退出码为 0。首个请求前的监听、
装配或迁移失败以退出码 1 报出。

进程内的渠道限流与熔断状态随进程结束重置，重启后从空态重新累计。

## 健康检查

`GET /healthz` 固定返回 200 与纯文本 `ok`，不经鉴权，也不探活上游：探活随上游抖动失败
会让编排系统反复重启一个本身健康的网关。

## systemd 单元示例

```
[Unit]
Description=TokenMP gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=tokenmp
EnvironmentFile=/path/to/tokenmp.env
ExecStart=/path/to/tokenmp serve
Restart=on-failure
RestartSec=2s

[Install]
WantedBy=multi-user.target
```

`/path/to/tokenmp.env` 只放环境变量：

```
TOKENMP_MYSQL_DSN=user:password@tcp(db.example:3306)/tokenmp?parseTime=true
TOKENMP_LISTEN=:8080
```

`serve` 的请求日志、尝试日志与熔断、凭据轮换的状态日志以 JSON 写标准输出，systemd 下由
journald 收集。
