# 发布

## 链路

发布由两条 GitHub Actions 工作流组成：

1. 推送到 `main` 后，`release-please` 解析 Conventional Commits，累积 changelog 并维护 release PR
2. 合并该 PR 会打 `v*` tag 并建 GitHub Release，tag 触发 `release` 工作流运行 GoReleaser，把多平台二进制挂到该 Release 上

配置文件：

```
release-please-config.json     release-please 的版本与 changelog 配置
.release-please-manifest.json  已发布版本号
.goreleaser.yaml               构建、归档与挂载配置
.github/workflows/             三条工作流
```

## 前置配置

`release-please` 工作流使用 GitHub App token，需配置两个仓库 secret：

| secret | 内容 |
|---|---|
| `RELEASE_BOT_APP_ID` | GitHub App 的 App ID |
| `RELEASE_BOT_APP_PRIVATE_KEY` | GitHub App 的私钥（PEM 全文） |

App 需要 `contents:write` 与 `pull_requests:write` 权限。

默认 `GITHUB_TOKEN` 创建的事件不产生新的 workflow run：用它打出的 tag 不会触发 `release`
工作流，Release 建出来但没有任何产物，且没有报错。

## 本地验证

```
goreleaser check                          # 校验 .goreleaser.yaml
goreleaser release --snapshot --clean     # 本地产出快照，不推送
```
