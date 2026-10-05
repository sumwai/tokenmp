# AGENTS.md

在本仓库工作的规则。

## 项目

TokenMP 是大模型 API 转发网关与计费平台，Go 单模块 `github.com/sumwai/tokenmp`。

```
cmd/tokenmp/        单一入口二进制，子命令按文件拆分
internal/           仓库内部包，不对外暴露
pkg/                可被外部导入的包
.github/workflows/  CI 与发布链路
```

## 命令

```
make check          # 门禁：编译、单测（-race）、静态检查、格式检查
make build-binary   # 产出 bin/tokenmp 并注入版本号
make tools          # 按 .mise.toml 装齐工具链
```

改动后必须跑 `make check` 并保持全绿。该目标只依赖 Go 与 golangci-lint。

## 约定

- 注释与用户可见文案使用中文。
- 注释写取舍与原因，不复述代码在做什么。
- 新包放在 `internal/`；`pkg/` 只放确实需要被外部导入的包。
- 错误返回值必须处理，`context` 随请求传递，HTTP 响应体必须关闭 —— 相关 linter 已开启。
- 不引入 `make check` 之外的新依赖门槛；确需 docker、数据库或 node 的检查另开目标。
- 工具链版本只在 `.mise.toml` 中声明，不在别处硬编码版本号。

## 文档

仓库内容默认公开，文档按职责分流，不在一个文件里混写不同受众的内容：

| 文件 | 职责 |
|---|---|
| `README.md` | 项目定位与已存在事实：命令、子命令、目录结构、版本行为 |
| `CONTRIBUTING.md` | 开发门禁与提交规范 |
| `RELEASE.md` | 发布链路、所需 secret、本地验证 |
| `CHANGELOG.md` | 由 release-please 自动维护，不手工编辑版本条目 |
| `AGENTS.md` | 本文件：工作规则 |

规则：

- 只陈述仓库中已经存在的事实。不写计划、进度、待办清单、设计推演或决策记录。
- 功能先落地，再更新文档；命令、目录、行为的变更与文档同步。
- 实现取舍写进对应源文件的注释，不写进公开文档。
- 文案与提交信息不使用对话式指代（你 / 我 / 咱们），只陈述代码与事实。
- 新增根目录文档前，先确认职责没有与上表重叠。

## GitHub 协作

`gh` CLI 已登录（账号 `sumwai`），Issue 与 PR 用它操作，不经网页手工编辑：

- 取任务：`gh issue list` / `gh issue view <n>`；新建：`gh issue create -t "<type>: <标题>" -b "<现象、期望、验收条件>"`
- 实现走分支与 PR：`git checkout -b <type>/<短描述>`，`gh pr create`，标题符合 Conventional Commits，正文写 `Closes #<n>`
- 合并用 squash：`gh pr merge --squash --delete-branch`；squash 后 PR 标题即 `main` 上的提交信息，决定版本号与 changelog 分区
- 合并前置：`make check` 全绿，`CI` 与 `PR 标题` 检查通过，评审无未解决意见
- 结论回写：`gh issue comment` / `gh pr comment`；关闭用 `gh issue close` 并附理由
- 不 force push `main`，不删除或改写已发布 tag，不批量关闭无关 Issue

## 提交

使用 Conventional Commits（`<type>(<scope>): <描述>`），完整规则见 [CONTRIBUTING.md](CONTRIBUTING.md)。
