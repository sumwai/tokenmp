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
CI 另外在真实 MySQL 上执行 `make check-integration` 与 `make e2e`，两者都需 `TOKENMP_TEST_MYSQL_DSN`。

## 提交规范

提交信息使用 Conventional Commits 格式：

```
<type>(<scope>): <描述>

- 详细变更点
```

`type` 取 `feat` / `fix` / `refactor` / `test` / `perf` / `docs` / `build` / `chore`。
`type` 前缀必须是 ASCII 英文（release-please 靠它归类与定版号），描述不限语言。
合并 release PR 后，条目会进入 [CHANGELOG.md](CHANGELOG.md) 的对应分区。

## 行为变更与规范同步

对外可观察的行为变更必须在同一个 PR 里同步更新规范：

- 端点路径、请求/响应字段、状态码、错误码、SSE 事件形态变更 → 更新 [docs/openapi.yaml](docs/openapi.yaml)；
- 参数处理、模型名替换、跨协议降级、用量与计费口径变更 → 更新 [docs/compatibility.md](docs/compatibility.md)；
- 命令、目录、环境变量或版本行为的变更 → 更新 [README.md](README.md)。

规范只写已经存在的事实：功能先落地，再改规范；不写计划、进度与待办。
新增根目录文档前先确认职责不与上表及 [AGENTS.md](AGENTS.md) 的文档职责表重叠。

## 协作

- 所有改动走分支 → PR → squash 合并。`main` 受分支保护：禁止 force push、禁止删除、
  禁止直接推送，管理员同样受限。
- 分支命名为 `<type>/<短描述>`，如 `feat/upstream-retry`。
- 缺陷与需求用 Issue 承载，模板位于 `.github/ISSUE_TEMPLATE/`。
- PR 模板位于 `.github/PULL_REQUEST_TEMPLATE.md`，门禁清单需勾选。
- PR 标题同样使用 Conventional Commits 格式，`pr-title` 工作流会校验：
  采用 squash 合并时 PR 标题即进入 `main` 的提交信息，直接决定版本号与 changelog 分区。
- 合并使用 squash（`gh pr merge --squash --delete-branch`），前置条件为 `make check` 全绿
  且 `CI` 与 `PR 标题` 两项检查通过。

## 发布

发布链路、所需 secret 与本地验证见 [RELEASE.md](RELEASE.md)。
