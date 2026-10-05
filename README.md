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
  version    报出版本号、构建自哪个提交，以及运行时的 Go 版本
  help       打印本帮助
```

未知子命令或缺参数时退出码为 2，运行失败为 1。

## 目录结构

```
cmd/tokenmp/        单一入口二进制，子命令按文件拆分
internal/           仓库内部包，不对外暴露
pkg/                可被外部导入的包
.github/workflows/  CI 与发布链路
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
