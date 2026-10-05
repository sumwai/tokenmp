GO ?= go
GOLANGCI_LINT ?= golangci-lint

.PHONY: build build-binary test lint fmt fmt-check check check-integration e2e tools

# 编译检查。刻意把产物导到临时目录并在退出时删除，而不是裸跑 `go build ./...`：
# 当模块里只有一个 main 包时（本仓库当前就是），`go build ./...` 会把可执行文件
# 写到**当前目录**，于是这个只该做检查的目标会在仓库根丢下一个二进制。
# 包多了以后该行为会消失 —— 也就是说缺陷只在特定阶段出现，正是最难察觉的一类。
# -o 指向一个已存在的目录时，多包与单包都写进该目录，行为一致。
build:
	@set -eu; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT INT TERM; \
	$(GO) build -o "$$tmp/" ./...

# 版本号取值优先级，取第一个非空者：
#   1. VERSION 变量，两个来源都算：
#      - 命令行：make build-binary VERSION=v1.2.3；
#      - 环境变量：VERSION=v1.2.3 make build-binary。
#   2. git describe --tags --dirty --always：有 tag 时是 tag，此时工作区脏会带 -dirty；
#      仓库没有任何 tag 时由 --always 兜住 —— 这正是该选项的用途，它退回提交短哈希且退出 0，
#      不是失败路径。真正会失败的是「不在 git 仓库内」。
#      实测注意：--always 的兜底路径上 --dirty **不会**追加后缀（--dirty 只在描述 tag 时生效），
#      所以无 tag 且工作区脏时这里拿到的是裸短哈希。工作区的脏状态由 tokenmp version 的
#      提交行单独报告，它的来源是 buildinfo 的 vcs.modified，与本处无关。
#   3. 字面量 dev：上面都拿不到时的兜底。
#
# 取值与校验必须在同一个 shell 调用里完成，make 全程不得持有这个值去展开或拼接。
# 因为 tag 名、命令行 VERSION、环境变量 VERSION 三者都是外部输入，交给 make 会被二次求值：
#   - $(shell ...) 读到的输出会被 make 再展开一次，于是 tag 名里的反引号变成命令执行；
#   - 拼进 -ldflags "..." 的词里时，内含的引号能从词中逃逸，同样变成命令执行；
#   - 该值含空白（如 'my version 1.0'）时，go build 会按空白切词而报出晦涩的 linker usage。
# 因此这里有两处专门的反求值处理，都不只是风格问题：
#   - VERSION 变量：以 $$version 在 recipe 的 shell 里读，而不是写成 $(VERSION) 让 make 拼进命令行；
#   - unexport VERSION：make 在执行 recipe 前会把 VERSION 展开后再交给子进程，
#     仅靠 set -u 与白名单拦不住那次展开 —— 值里的 $(shell ...) 会在校验之前就被执行。
#     故先 unexport 阻止那次展开，再用 $(value VERSION) 取回未展开的原文传给 shell 校验。
#     $(value ...) 取出的是变量原文、不会再求值，所以这一步不会引入新的执行点。
#
# 校验用白名单，只放行字母、数字与 . _ + -：版本号不需要别的字符。
# 这里刻意不做静默清洗（例如把非法字符删掉后就地使用）：清洗会让二进制报出一个
# 与 tag／VERSION 都不对应的版本号，正是本目标要消灭的「不知道是哪个版本」，
# 因此宁可让构建明确失败，也不要产出一个看似正常、实则对不上号的版本号。
#
# 失败语义因此分两类，界限是「输入取值」而非「能否取到」：
#   - 取不到版本号（不在 git 仓库内、环境里没有 git、git describe 其它失败）→ 退化为 dev，不失败；
#   - 取到了但含非法字符 → 明确报错并非零退出（有意的：那是要人来处理的输入问题）。
#
# 产出可执行文件 bin/tokenmp，并把版本号注入包级变量 main.version。
# 与 build 的分工：build 只做全仓库编译检查，不产出产物、不注入版本；
# 本目标才产出二进制，供本机实跑与后续打包取用。
# 注入只对包级变量生效：-X 命中常量时静默失效且不报错，目标符号不存在时构建也照样成功，
# 因此这里不对注入结果做断言，注入是否真的生效由实跑 bin/tokenmp version 验证。
#
# 本目标刻意不加 -buildvcs=false：go build 默认会把 vcs.revision / vcs.time / vcs.modified
# 写进 buildinfo，没被注入版本号的二进制就靠这份 buildinfo 追溯自己构建自哪个提交。
# 加 -buildvcs=false 等于主动放弃它。除主动放弃外，另有两种情形会让 vcs setting 静默消失
# 而构建照常成功（退出码 0、无告警）：构建目录不在 git 仓库内；环境里没有 git 可执行文件。
# 对部署的直接含义：凡以容器等方式构建本二进制，构建上下文必须同时具备 .git 与可用的 git。
unexport VERSION
export VERSION_RAW := $(value VERSION)

build-binary:
	@mkdir -p bin
	@set -eu; \
	version="$${VERSION_RAW:-$$(git describe --tags --dirty --always 2>/dev/null || echo dev)}"; \
	case "$$version" in \
		*[!A-Za-z0-9._+-]*) \
			echo "错误：版本号 '$$version' 含非法字符，只允许字母、数字与 . _ + -" >&2; \
			exit 1 ;; \
	esac; \
	$(GO) build -ldflags "-X main.version=$$version" -o bin/tokenmp ./cmd/tokenmp

test:
	$(GO) test -race ./...

lint:
	$(GOLANGCI_LINT) run

fmt:
	$(GOLANGCI_LINT) fmt

fmt-check:
	$(GOLANGCI_LINT) fmt --diff

# 快速门禁：不依赖 docker、数据库或 node，只需要 go 与 golangci-lint。
check: build test lint fmt-check

# 真实 MySQL 的迁移验证，刻意不加入 check。
#
# check 的契约是「无外部依赖」，把需要数据库的检查塞进去会让本机与 CI 都
# 必须常备一个 MySQL，门禁变重后就会有人绕开它；因此分成独立目标，
# 由需要验证迁移的人在具备数据库时显式执行。
#
# DSN 经 TOKENMP_TEST_MYSQL_DSN 传入，须指向可丢弃的库：测试会先删除
# 迁移涉及的表再重建。未设置该变量时测试跳过而非失败。
check-integration:
	$(GO) test -tags=integration -race -count=1 ./internal/store/...

# 端到端运营剧本：真实 MySQL、真实监听端口与进程内假上游，一条贯穿入驻到对账的验证。
#
# 刻意不加入 check：与 check-integration 同一理由 —— check 的契约是无外部依赖。
# DSN 同样经 TOKENMP_TEST_MYSQL_DSN 传入，须指向可丢弃的库；未设置该变量时测试跳过而非失败。
# 剧本自带数据清理（开跑前与跑完各清一次全库），连跑两次不会因残留冲突。
#
# 构建标签把剧本文件与 make check 隔离；-run '^TestE2E' 再把本次执行限定在剧本本身 ——
# cmd/tokenmp 下还有一批不依赖数据库的单测，它们不在本目标的验证范围内。
e2e:
	$(GO) test -tags=e2e -race -count=1 -run '^TestE2E' ./cmd/tokenmp/...

# 按 .mise.toml 装齐本机工具链。
tools:
	mise install
