# TokenMP

大模型 API 转发网关与计费平台：把客户端的模型请求转发到上游渠道，按用量计费并从账户余额扣款。

## 快速开始

需要 Go 1.27 及以上。

```
make check              # 编译、单测（-race）、静态检查、格式检查
make check-integration  # 对真实 MySQL 跑迁移验证（需 TOKENMP_TEST_MYSQL_DSN）
make build-binary       # 产出 bin/tokenmp 并注入版本号
./bin/tokenmp version
```

`make check` 只依赖 Go 与 golangci-lint，不需要 docker、数据库或 node。
`make check-integration` 需要指向可丢弃库的 DSN，见目标注释。

工具链版本由 `.mise.toml` 锁定，`make tools` 执行 `mise install` 装齐。

## 子命令

```
$ ./bin/tokenmp help
用法：tokenmp <子命令>

子命令：
  serve      启动网关 HTTP 服务
  version    报出版本号、构建自哪个提交，以及运行时的 Go 版本
  help       打印本帮助
```

未知子命令或缺参数时退出码为 2，运行失败为 1。

`tokenmp serve` 从环境变量读取运行配置，启动时执行数据库迁移，暴露
`GET /healthz`（200）与三个转发端点 `POST /v1/chat/completions`、
`POST /v1/responses`、`POST /v1/messages`，收到退出信号后优雅关闭。
转发端点要求 `Authorization: Bearer <key>`。

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

超时与连接池取值必须为正数或合法时长，非法取值在启动前报错并以 1 退出。
流式转发不设整体超时，只受首字节与空闲读两级约束。

每请求写一条 JSON 请求日志到标准输出：请求 id（客户端带 `X-Request-Id` 时沿用）、
协议方言、请求模型名、命中的渠道 id、上游状态码与耗时。凭据与密钥不进入日志。

## 目录结构

```
cmd/tokenmp/         单一入口二进制，子命令按文件拆分
internal/domain/     协议无关的内部统一请求/响应与端口定义
internal/adapters/   三种线协议与内部统一格式的双向转换
internal/pipeline/   核心转发流水线：选路、请求定稿、上游调用与候选回退
internal/transport/  共用 HTTP 入口与 SSE 逐帧读取
internal/upstream/   调用上游渠道的 HTTP 客户端
internal/credential/ 按路由引用取凭据并拼装上游请求头
internal/store/      MySQL 连接、迁移与 schema 读写
internal/billing/    计费指标与用量映射
internal/config/     环境变量到运行配置
pkg/                 可被外部导入的包
.github/workflows/   CI 与发布链路
```

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
