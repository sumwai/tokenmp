# 贡献

## 开发

工具链版本由 `.mise.toml` 锁定，装齐：

```
make tools
```

提交前过一遍门禁：

```
make check          # 编译、单测（-race）、静态检查、格式检查
```

`make check` 只依赖 Go 与 golangci-lint，不需要 docker、数据库或 node。

## 提交规范

提交信息使用 Conventional Commits 格式：

```
<type>(<scope>): <描述>

- 详细变更点
```

`type` 取 `feat` / `fix` / `refactor` / `test` / `perf` / `docs` / `build` / `chore`。
`type` 前缀必须是 ASCII 英文（release-please 靠它归类与定版号），描述不限语言。
合并 release PR 后，条目会进入 [CHANGELOG.md](CHANGELOG.md) 的对应分区。

## 发布

发布链路、所需 secret 与本地验证见 [RELEASE.md](RELEASE.md)。
