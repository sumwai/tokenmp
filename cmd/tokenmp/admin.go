package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/config"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是 `tokenmp admin` 的入口与分发。
//
// 管理面直连数据库执行（本地运维工具，不经网络鉴权）。cmd 只做参数解析与输出；
// 业务动作（校验、默认值、派生计算）在 internal/admin。
//
// 退出码口径与既有子命令一致：用法错误 2（缺参、未知组 / 动作、枚举非法），
// 运行失败 1（配置缺失、连接失败、SQL 失败）。

// 管理面的 flag 名集中声明：同一名字在多个动作里出现，散落的字面量会漂移。
const (
	flagJSON          = "json"
	flagID            = "id"
	flagCode          = "code"
	flagName          = "name"
	flagKind          = "kind"
	flagMerchant      = "merchant"
	flagType          = "type"
	flagVendor        = "vendor"
	flagBaseURL       = "base-url"
	flagCredGroup     = "cred-group"
	flagPriority      = "priority"
	flagWeight        = "weight"
	flagGroup         = "group"
	flagAPIKey        = "api-key"
	flagChannel       = "channel"
	flagModel         = "model"
	flagUpstreamModel = "upstream-model"
	flagMultiplier    = "multiplier"
	flagOverrides     = "overrides"
	flagAccount       = "account"
	flagExpires       = "expires"
	flagUnit          = "unit"
	flagAmount        = "amount"
	flagFallback      = "fallback"
	flagSource        = "source"
	flagQty           = "qty"
	flagPrice         = "price"
	flagModelScope    = "model-scope"
	flagValidity      = "validity"
	flagProduct       = "product"
	flagEffective     = "effective"
	flagComponent     = "component"
	flagScope         = "scope"
	flagScopeID       = "scope-id"
	flagMetric        = "metric"
	flagValidFrom     = "valid-from"
	flagValidTo       = "valid-to"
	flagTimeFrom      = "time-from"
	flagTimeTo        = "time-to"
	flagWeekdayMask   = "weekday-mask"
	flagDayKindMask   = "day-kind-mask"
	flagCalendar      = "calendar"
	flagFile          = "file"
	flagFrom          = "from"
	flagTo            = "to"
	flagSince         = "since"
	flagReason        = "reason"
	flagOperator      = "operator"
)

// 管理动作名集中声明：同一动作名在多个组里出现。
const (
	actionCreate        = "create"
	actionList          = "list"
	actionDisable       = "disable"
	actionEnable        = "enable"
	actionAdd           = "add"
	actionSet           = "set"
	actionDel           = "del"
	actionIssue         = "issue"
	actionRevoke        = "revoke"
	actionImport        = "import"
	actionPublish       = "publish"
	actionBuy           = "buy"
	actionCredit        = "credit"
	actionSetMultiplier = "set-multiplier"
	actionSetMerchant   = "set-merchant"
)

// 表格列名里出现三次以上的取值。
const (
	headerEnabled   = "enabled"
	headerCreatedAt = "created_at"
	headerStatus    = "status"
)

// adminEnv 是一次 admin 命令执行的依赖与输出流。
type adminEnv struct {
	service *admin.Service
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
}

// usageError 打印一条用法提示与总帮助，返回用法退出码。
func (e *adminEnv) usageError(message string) int {
	_, _ = fmt.Fprintf(e.stderr, "%s\n\n", message)
	_, _ = io.WriteString(e.stderr, adminUsageText())
	return exitUsage
}

// usageErrorf 是 usageError 的格式化版本。
func (e *adminEnv) usageErrorf(format string, args ...any) int {
	return e.usageError(fmt.Sprintf(format, args...))
}

// fail 打印运行失败原因，返回失败退出码。
func (e *adminEnv) fail(err error) int {
	_, _ = fmt.Fprintf(e.stderr, "错误：%v\n", err)
	return exitFailure
}

// newFlagSet 构造一个把错误写到 stderr 的 flag 集合。
//
// 解析失败时 flag 自己已经把原因与用法写到 stderr，调用方只需返回用法码。
func (e *adminEnv) newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	return fs
}

// stringList 是支持重复出现的字符串 flag，用于 --component 这类多值参数。
type stringList []string

// String 实现 flag.Value。
func (s *stringList) String() string { return strings.Join(*s, ",") }

// Set 追加一个取值。
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// adminGroupSpec 是一组管理动作的声明。
//
// 它是帮助文本与用法预检的单一出处：命令层在连库前用它拦住未知组 / 未知动作，
// 让用法错误以退出码 2 报出，而不是因为缺 DSN 变成运行失败 1。
type adminGroupSpec struct {
	name    string
	actions []string
}

// adminGroups 按帮助展示顺序列出全部组与动作。
var adminGroups = []adminGroupSpec{
	{flagMerchant, []string{actionCreate, actionList, actionDisable}},
	{flagChannel, []string{actionCreate, actionList, actionEnable, actionDisable}},
	{"credential", []string{actionAdd, actionList, actionDisable}},
	{"model-map", []string{actionSet, actionList, actionDisable}},
	{flagAccount, []string{actionCreate, actionList, actionDisable, actionSetMultiplier, actionSetMerchant}},
	{"key", []string{actionIssue, actionList, actionRevoke}},
	{"bucket", []string{actionCredit, actionList}},
	{"product", []string{actionCreate, actionList}},
	{"purchase", []string{actionBuy, actionList}},
	{"price", []string{actionPublish, actionList}},
	{"rule", []string{actionAdd, actionList, actionDel}},
	{"calendar", []string{actionImport, actionList}},
	{"usage", []string{actionList}},
	{"adjust", []string{actionAdd, actionList}},
}

// lookupGroup 按名字找组声明。
func lookupGroup(name string) (adminGroupSpec, bool) {
	for _, group := range adminGroups {
		if group.name == name {
			return group, true
		}
	}
	return adminGroupSpec{}, false
}

// allows 报告组是否声明了该动作。
func (g adminGroupSpec) allows(action string) bool {
	for _, candidate := range g.actions {
		if candidate == action {
			return true
		}
	}
	return false
}

// cmdAdmin 是 admin 子命令入口：读配置、连库、分发。
//
// 刻意不跑迁移：迁移是会改 schema 的写操作，应由部署流程或 serve 启动显式触发，
// 不该由一次运维查询的副作用完成。
func cmdAdmin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeAdminUsage(stderr, exitUsage, "缺少管理组名")
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		return writeAdminUsage(stdout, exitOK, "")
	}
	group, ok := lookupGroup(args[0])
	if !ok {
		return writeAdminUsage(stderr, exitUsage, fmt.Sprintf("未知管理组 %q", args[0]))
	}
	if len(args) < 2 {
		return writeAdminUsage(stderr, exitUsage, fmt.Sprintf("%s 需要动作：%s", group.name, strings.Join(group.actions, " / ")))
	}
	if !group.allows(args[1]) {
		return writeAdminUsage(stderr, exitUsage, fmt.Sprintf("%s 未知动作 %q", group.name, args[1]))
	}

	cfg, err := config.Load()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "配置错误：%v\n", err)
		return exitFailure
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "连接数据库失败：%v\n", err)
		return exitFailure
	}
	defer func() { _ = st.Close() }()

	return dispatchAdmin(ctx, args, &adminEnv{
		service: admin.New(st),
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
	})
}

// writeAdminUsage 打印可选的前置提示与帮助，返回指定退出码。
func writeAdminUsage(w io.Writer, code int, message string) int {
	if message != "" {
		_, _ = fmt.Fprintf(w, "%s\n\n", message)
	}
	_, _ = io.WriteString(w, adminUsageText())
	return code
}

// dispatchAdmin 按管理组分发；与 cmdAdmin 拆开是为了让单测注入假 store。
func dispatchAdmin(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("缺少管理组名")
	}
	group, rest := args[0], args[1:]
	switch group {
	case flagMerchant:
		return adminMerchant(ctx, rest, env)
	case flagChannel:
		return adminChannel(ctx, rest, env)
	case "credential":
		return adminCredential(ctx, rest, env)
	case "model-map":
		return adminModelMap(ctx, rest, env)
	case flagAccount:
		return adminAccount(ctx, rest, env)
	case "key":
		return adminKey(ctx, rest, env)
	case "bucket":
		return adminBucket(ctx, rest, env)
	case "product":
		return adminProduct(ctx, rest, env)
	case "purchase":
		return adminPurchase(ctx, rest, env)
	case "price":
		return adminPrice(ctx, rest, env)
	case "rule":
		return adminRule(ctx, rest, env)
	case "calendar":
		return adminCalendar(ctx, rest, env)
	case "usage":
		return adminUsage(ctx, rest, env)
	case "adjust":
		return adminAdjust(ctx, rest, env)
	case "help", "-h", "--help":
		_, _ = io.WriteString(env.stdout, adminUsageText())
		return exitOK
	default:
		return env.usageErrorf("未知管理组 %q", group)
	}
}

// adminUsageText 返回管理面帮助。与主用法文本分开：admin 的组与动作数量远多于
// 顶层子命令，塞进主帮助会淹没 serve / version。
func adminUsageText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "用法：%s admin <组> <动作> [选项]\n\n", programName)
	b.WriteString("组与动作：\n")
	for _, group := range adminGroups {
		fmt.Fprintf(&b, "  %-11s %s\n", group.name, strings.Join(group.actions, " | "))
	}
	b.WriteString("\n所有 list 动作输出对齐表格，加 --json 输出机器可读格式。\n")
	b.WriteString("数据库连接取自 TOKENMP_MYSQL_DSN。\n")
	return b.String()
}
