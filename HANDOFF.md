# 交接：网关中间件插件层

本文件是一次工作的交接快照，供接手者建立上下文用。它含进度与待办，不属于
`docs/` 的文档职责（`AGENTS.md` 规定公开文档只写已存在的事实），也不打算合并进
`main` —— 这个分支只是载体，需要时把结论按仓库规范转成 issue 或文档。

交接范围：网关中间件插件层（`internal/plugin`、`internal/gateway` 的插件装配、
`cmd/tokenmp` 的 `admin plugin` 组）以及一次发布流程修复。

---

## 1. 已合并的改动

| PR | 主题 | 关键内容 |
|---|---|---|
| #100 | 失败日志补 JS 栈与请求标识，装配期拦下非函数钩子 | 钩子异常与顶层抛错经 `Runtime.StackTrace` 补栈（引擎的 `Exception.Stack` 恒空，栈挂在错误对象的 `stack` 属性上）；`Module.Hook` 只查导出槽是否存在，可调用性改用 `Runtime.Has` 在求值顶层之后判定；失败日志带 `request_id` 并按插件限频（`suppressed` 补报）；改写后解码失败不再重复计入统计 |
| #103 | 热重载单飞与退避 | 编译移出 `m.mu` 并加 `reloading` 单飞闸（抢不到闸的请求不排队、用当前产物）；失败进入退避窗口（`Options.ReloadBackoff`，默认 1s）；`Info` 增加 `reloaded_at` / `reload_failures` / `reload_error` / `stale`；`Stats()` 与 `ScopeDeclarations()` 改读快照，不再触发重编译 |
| #105 | 热重载覆盖运行期依赖 | 运行期 `import()` 触及的文件经 `noteRuntimeDep` 纳入指纹、换代后刷新基线；取用失败计入 `skipped` / `skip_error` 并限频；`Load` 一次报出全部坏项；日志限频抽成 `throttledLog` |
| #106 | SIGHUP 强制重载、换代后重跑漂移检查 | `Set.OnReload`；`Gateway.ReloadPlugins()`；漂移检查在换代后重跑（后台 goroutine + 5s 预算 + `atomic.Bool` 单飞闸） |
| #107 | 离线校验命令与可运行示例 | `admin plugin check`；`examples/think-tag.mw.js` 由 `internal/plugin/example_test.go` 加载；`docs/compatibility.md` 补事件对象与返回分片字段表 |
| #110 | 混合帧边界、`stale` 口径、取用开销基准 | 写明混合帧绕过 `onEvent` 的边界；纠正「`stale` 在独立进程里有效」的错误说法；新增 `internal/plugin/bench_test.go` |
| #111 | 插件改由本机清单与 `admin plugin` 管理 | 新增本机注册表（`internal/plugin/registry.go`）与 `add/list/enable/disable/del/check`；移除 `TOKENMP_PLUGIN_FILES` |
| #113 | 清单改动运行期生效、坏插件只跳过自己 | 新增 `plugin.Live`（清单驱动、可换装）；新增 `LoadAvailable`；删除 `Set.Reload()` 与 `compiler.reset()` |
| #114 | 版本回到 0.1.10 | 发布流程修复，含 `CONTRIBUTING.md` 的预 1.0 约定（见 §5.3） |

---

## 2. 当前仓库状态

- `main` 头部：见 `git log -1 origin/main`（本文件写下时为 `feat: 用户级端点落地密钥自助管理 (#118)`）。
- 版本：`0.1.10` 已发布（tag 与 GitHub Release），`.release-please-manifest.json` = `0.1.10`。
- 待发布的发布 PR：`chore(main): release 0.1.11`（release-please 自动维护，何时合并由发布者决定）。
- 未关闭的 issue：`#109`、`#93`、`#90`、`#74`。
- 插件层的两条工作线之外的在途工作：用户侧端点（`internal/me` 一类的自助端点）由另一条线推进，
  本分支不涉及。

历史里有一条标题为 `chore(main): release 0.2.0` 的 squash 提交，它对应的发布已被改写为
`0.1.10`（原因见 §5.4）。不改写 `main` 历史的前提下，这是能做到的最干净状态。

---

## 3. 插件层现在的样子

### 3.1 清单与命令

- 清单文件路径来自 `TOKENMP_PLUGIN_STATE_FILE`（默认 `/var/lib/tokenmp/plugins.json`），
  由 `internal/config` 解析，内容由 `internal/plugin/registry.go` 读写：
  `{"plugins":[{"name":..., "path":..., "enabled":...}]}`。
- 写入是「临时文件 + `rename`」；读取时形状非法（非 JSON、字段不认识、名字或路径为空、
  名字重复）按配置错误报出；文件不存在视为没注册任何插件。
- 命令（`cmd/tokenmp/admin_plugin.go`）：
  - `add <路径> [--name N] [--disabled]`：先装配校验，通过才写入；
  - `list`：逐项装配探测，列 `enabled` 与 `result`；退出码 0 表示「列出来了」；
  - `enable` / `disable` / `del`：改清单，`del` 不动插件文件；
  - `check [路径...]`：装配任意路径，不读清单，任一失败退出码 1。
- `plugin` 组只读写本机文件，**不连数据库**：`cmd/tokenmp/admin.go` 在打开存储之前分发它。
  这条是有意的 —— 坏插件让 `serve` 起不来时，`disable` 是唯一能用的手段。
- 顺序即装配顺序（注册顺序），调整顺序用 `del` 后重新 `add`。

### 3.2 装配与换装

- `plugin.Live`（`internal/plugin/live.go`）是按清单装配的集合，装配方拿到的始终是同一个对象；
  端口调用转发给「此刻那一代」集合。
- `Gateway` 持有 `Live`，注入三处：`pipeline.Options.Stream/Response`、
  `pluginForwarder.plugins`、`pluginContextMiddleware`。后两者**始终安装**（换装可让集合从空变非空），
  集合为空时短路，因此没装插件时没有额外分配。
- 换装入口：`Gateway.ReloadPlugins()` ← `serve` 的 SIGHUP。换装 = 重读清单 → 重新装配整代集合 →
  `atomic.Pointer` 换上 → 通知装配方（重跑漂移检查）。单飞闸保证连发信号只跑一轮。
- **代际钉住**：`Live.BeginRequest` 把这一代集合写进请求上下文，后续钩子取同一代。
  不加这条，一次换装会让新集合的钩子去读旧一代建出的运行时。
- 一次换装重新读文件（新集合 = 新编译缓存），因此**指纹看不见的改动**（`cp -p`、等长覆盖）
  也在这条路径上生效。

### 3.3 失败与容错口径

| 场景 | 行为 |
|---|---|
| 启用的插件编译失败 / 路径不在 | 跳过它并记 ERROR，其余插件与转发照常（`LoadAvailable`）；`admin plugin list` 显示 `fail` |
| 清单文件本身读不出来 | 启动期报错退出；运行期保留当前集合并记 ERROR |
| 插件在运行中被写坏 | 当前那一代继续服务；文件指纹触发的惰性重编译失败时保留旧产物并退避（默认 1s） |
| 取不到运行时（模块求值失败/超时） | 本次请求跳过该插件、按原样转发，计入 `Info.Skipped` |
| 钩子抛错 / 超时 / 返回非法值 | 按原样放行，日志带 JS 栈、`request_id`，按插件限频 |
| 注册前校验 | `admin plugin add` 用严格版 `Load`：任何一个装不上就不写入 |

严格版 `Load` 与宽容版 `LoadAvailable` 的分工不要混淆：前者用于「这些文件都必须能用」
（`add`、`check`），后者用于运行期装配。

### 3.4 依赖与指纹

- 指纹是 `fileStamp`（修改时间纳秒 + 大小），见 `internal/plugin/compiler.go`。
- 静态依赖来自 `compileGraph` 的编译会话；运行期 `import()` 触及的文件经 `compiler.noteRuntimeDep`
  登记到 `Middleware.runtimeDeps`（`sync.Map`），换代成功后刷新基线。
- `staleLocked` 两类都看；重编译在锁外进行，失败退避。
- 编译缓存按文件路径 + 指纹，一个中间件一个 `compiler`（沙箱根目录决定）。

### 3.5 可观测面

- 进程内可读：`plugin.Set.Stats()` / `plugin.Live.Current().Stats()` 返回 `Info`，字段含
  `name` / `path` / `hooks` / `events` / `calls` / `failures` / `average_ms` / `last_error` /
  `reloaded_at` / `reload_failures` / `reload_error` / `stale` / `skipped` / `skip_error`。
- **没有外部出口**：跨进程读不到运行期状态（`admin plugin list` 是另一个进程，只能给装配结论）。
  数据面已就绪，出口按 §4.2 决定。
- `Stats()` 为了算 `stale` 会比对一遍依赖文件，不适合高频轮询。
- 日志限频：`throttledLog`（钩子失败、取用失败各一份，每秒各 5 条，被压条数用 `suppressed` 补报）。

---

## 4. 未完事项

### 4.1 `#109` 同帧内容与 `finish_reason` 绕过 `onEvent`（已决定推迟）

- 位置：`internal/pipeline/sink.go` 的 `passthroughSink.filter` / `SendFrame`。一帧里混有内容与
  非内容分片时整帧透传，`onEvent` 一次不被调用。
- 唯一可达组合：`openai_chat` 方言 + 「内容 + `ChunkFinish`」同帧。
- 决定与依据、落地方案、验收清单、开工前必须先定的 `finish_reason` 字面量保真口径，都记在
  issue `#109` 的描述与评论里。触发条件：某个在用渠道确实如此下发，或上线必须覆盖全部内容的插件。
- 更便宜的中间选项（只把静默变可见）也在该 issue 的评论里，它的成本是给 `sink` 接一条 logger 注入链。

### 4.2 运行期状态的外部出口（按口径等）

- 现状：数据面就绪（`Info`），无外部出口。
- 既定口径：以后再做，且可能要设计成「用户可选择开启」。
- 若要做，注意三点：出口形态与消费者；**不要**按秒轮询 `Stats()`（见 §3.5）；`stale` 的语义是
  「相对本次 `Load` 抓的指纹快照」，独立进程里几乎恒为 `false`。

### 4.3 每请求 N 次 `os.Stat`

- 现状：`staleLocked` 每次取用会对全部依赖文件 `stat` 一遍。单文件插件 N = 1。
- 已有 `internal/plugin/bench_test.go` 可对照开销；若要优化，先考虑把 `stat` 移出 `m.mu`
  （持锁取依赖与基线快照、锁外 `stat`、再持锁校验产物未换），这不会改变重载延迟。
  时间维度的节流会把「下一个请求生效」变成「最多 N 秒后生效」，需要权衡。

### 4.4 启动期容错：已按需求实现，取证问题作废

原先记录的「需要仓库外信息才能决定是否容错」的清单（镜像内文件还是挂载卷、是否已有事故等）
已不再需要：口径改成「错误就跳过该插件」，见 §3.3。

### 4.5 不属于本次范围的 issue

- `#90` 失败分类收敛的第二部分（`onFailure` 插件裁定钩子）：设计被评审推翻后未收敛，落在
  `internal/failure`、`internal/upstream`、各适配器，与插件层只有名义上的交集。
- `#74` 路由选择钩子 `onRoute`：评审结论是不排期，缺口用内置排序策略解决。
- `#93` 除混合帧一条外，其余条目都有对应改动（见 §1），可关闭或留作索引。

---

## 5. 运行、验证与发布

### 5.1 常用命令

```sh
make check              # 编译 + go test -race + golangci-lint + 格式检查，必须全绿
make check-integration  # 需要 MySQL
make e2e                # -tags e2e，端到端剧本（plugin_e2e_test.go 等）
go test -run XXX -bench . -benchtime 200x ./internal/plugin/   # 取用开销基准
```

`cmd/tokenmp` 下带 `//go:build e2e` 的文件只在 `make e2e` 下编译；改动 `gateway.Options`
一类装配面时要顺带 `go vet -tags e2e ./cmd/tokenmp/`。

### 5.2 插件层的操作

```sh
tokenmp admin plugin add /opt/plugins/x.mw.js   # 校验通过才写入清单
tokenmp admin plugin list                       # 逐项装配探测
tokenmp admin plugin disable x.mw.js            # serve 起不来时也能用
tokenmp admin plugin check ./draft.mw.js        # 离线校验任意路径
kill -HUP <serve pid>                           # 重读清单并换装
```

### 5.3 提交与发布约定

- 提交走 Conventional Commits；PR 标题即 squash 后的提交信息，`pr-title` 工作流会校验，
  正则不接受 `!`。
- **预 1.0 阶段不写 `BREAKING CHANGE:` 脚注**（`CONTRIBUTING.md` 已写明）：release-please
  会因此把版本升到下一个 minor。升级注意写进 PR 描述与文档。
- 发布链路：release-please 维护发布 PR（打 tag + 建 GitHub Release），tag 触发 `release.yml`
  的 goreleaser；goreleaser 配置是 `changelog: disable` + `release.mode: keep-existing`，
  也就是说 **Release 必须由 release-please 先建**，goreleaser 只挂产物。手工打 tag 时要把
  Release 一起建出来，否则产物没地方挂。
- release-please 用「上一个 tag」划界扫描提交：缺 tag 时它会把更早的提交（包括
  `BREAKING CHANGE:` 脚注）重新算进来。

---

## 6. 踩过的坑（对后来者有用）

1. **管道会掩盖退出码**：`make check 2>&1 | tail` 的退出码取自 `tail`，红的会被当成绿的。
   跑门禁不要接管道，或检查输出尾部的 `0 issues.`。
2. **`-race` 会抓到自己写的测试**：后台 goroutine 写日志、测试侧轮询 `bytes.Buffer` 是竞争。
   `internal/gateway/pluginscope_test.go` 里的 `syncBuffer` 就是为此加的。
3. **堆叠 PR 的 base 分支被删会让 GitHub 关闭子 PR**（不是自动 retarget），重开也会失败
   （base 已不存在）。需要堆叠时从 `main` 分支，或接受按顺序合并。
4. **发布 PR 与并发 push 撞车**：改完发布 PR 后、合并前，若有别的提交落进 `main`，
   release-please 会重算并覆盖手工改动，合并的就是被覆盖后的内容（本次因此误发了 `0.2.0`）。
   另外，只删 tag 而不补上正确的边界 tag，release-please 下次仍会按旧范围重算同一个版本
   （`0.2.0` 复活过一次）。正确顺序：补正确的 tag 与 Release → 再关闭/重建发布 PR。
5. **`gh pr checks --watch` 会因 API 抖动退出 1**，那不是检查失败，看输出确认。
6. **手工建 Release 时要与 `release.yml` 的分工对齐**（见 §5.3），否则 goreleaser 找不到目标。

---

## 7. 接手建议的第一步

按这个顺序读，大约能把插件层看完：

1. `internal/plugin/registry.go` —— 清单格式与不变量；
2. `internal/plugin/live.go` —— 换装与代际钉住；
3. `internal/plugin/plugin.go` —— `Set` / `Middleware` / `Load`（严格）/ `LoadAvailable`（宽容）/
   `Info` / `current()` 的单飞与退避；
4. `internal/plugin/compiler.go` —— 编译缓存与依赖登记；
5. `internal/gateway/plugin.go`、`internal/gateway/pluginscope.go` —— 装配与漂移检查；
6. `cmd/tokenmp/admin_plugin.go` —— CLI 六个动作；
7. `docs/operations.md` 的中间件步骤、`docs/compatibility.md` 的中间件小节 —— 对外口径。
