package credential

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
)

// 本文件实现跨请求的凭据轮换与失败切换：同组启用凭据按轮换顺序取用，凭据类失败后
// 在同一次渠道尝试内换下一条，失败的那条进入进程内冷却。
//
// 状态都在进程内：冷却不落库是刻意的取舍 —— 落库要在多实例间抢行锁并引入写放大，
// 换来的只是「重启后仍然记得这把 key 坏了」，而重启本身已重置一切、上游也早该换 key。

const (
	// defaultCooldown 是凭据类失败后的默认冷却时长。
	defaultCooldown = 60 * time.Second
	// maxTrialsPerAttempt 是单次渠道尝试内的凭据试用上限。
	//
	// 固定取 4 而不是「有多少试多少」：大组（轮换期批量导入几十把 key）在一次请求内
	// 逐把试到成功，会把一次客户端请求放大成对上游的凭据枚举，请求时序也随之失控。
	// 与组内启用数取较小者：组里只有两把时不该凭空多试。
	maxTrialsPerAttempt = 4
	// credentialLogPrefixRunes 是日志里凭据前缀的最大字符数。
	// 前缀只用于人工比对「换的是不是同一把」，不参与鉴权，过长会把可恢复的信息量放大。
	credentialLogPrefixRunes = 4
)

// NamedCredential 是组内一份启用凭据。
type NamedCredential struct {
	// Name 是凭据行的名字，用于日志与冷却键。它不是密钥，可以出现在日志里。
	Name string
	// APIKey 是明文密钥，只用于注入上游请求头，不落日志。
	APIKey string
}

// Group 是一个渠道分组在某个归属下的启用凭据快照，按轮换顺序排列。
type Group struct {
	// Scope 是归属的稳定标识（例如商家 id 的字符串形式）。
	//
	// 冷却键由 Scope、分组名与凭据名共同构成：分组名只在归属内唯一，不带 Scope 会让
	// 两个归属的同名凭据互相冷却，把另一个归属的好 key 也跳过。
	Scope   string
	Entries []NamedCredential
}

// GroupLoader 按本次路由读出组内启用凭据。
//
// 实现必须是只读的：轮换游标与冷却都在 Rotator 内，加载器每次返回同一份顺序即可。
type GroupLoader interface {
	LoadGroup(ctx context.Context, route domain.Route) (Group, error)
}

// RotationOptions 是构造 Rotator 的依赖与默认值。
type RotationOptions struct {
	// Loader 按分组读出启用凭据。必填。
	Loader GroupLoader
	// Cooldown 是凭据类失败后的冷却时长；<= 0 时取 defaultCooldown。
	Cooldown time.Duration
	// Clock 取当前时刻；nil 时用 time.Now。注入后冷却判定不依赖真实时间，测试无需 sleep。
	Clock func() time.Time
	// Logger 记录冷却跳过与切换；nil 时不记录。
	Logger *slog.Logger
}

// Rotator 是按轮换顺序取用组内凭据的 Resolver，同时实现 domain.CredentialRotation。
//
// 可并发使用：游标与冷却表由互斥锁保护，请求内的轮换状态随调用方的 context 传递。
type Rotator struct {
	loader   GroupLoader
	cooldown time.Duration
	clock    func() time.Time
	logger   *slog.Logger

	mu sync.Mutex
	// cursors 是每分组的轮换起点游标，使多个并发请求不会永远从同一条凭据开始。
	cursors map[string]uint64
	// cooling 是凭据的冷却截止时刻，键为 coolingKey。
	cooling map[string]time.Time
}

// 编译期断言：轮换器既能给请求头提供者当解析器，又满足流水线的轮换端口。
var (
	_ Resolver                  = (*Rotator)(nil)
	_ domain.CredentialRotation = (*Rotator)(nil)
)

// rotationStateKey 是尝试级轮换状态在 context 里的键。类型不导出，避免外部写入。
type rotationStateKey struct{}

// attemptState 是一次渠道尝试内的轮换状态。
//
// 它随 context 传递而不放进 Rotator：同一份 Rotator 服务全部并发请求，
// 「这条请求已经试过组内哪几条」属请求级事实，混进共享状态会互相串用。
type attemptState struct {
	loaded bool
	group  Group
	// order 是本次尝试的试用顺序，元素为 group.Entries 的下标，已按上限截断。
	order []int
	tried int
	// current 是最近一次解析选中的凭据，Advance 据此把失败的那条标记进冷却。
	current *NamedCredential
}

// NewRotator 构造凭据轮换器。缺少加载器在构造时报出，不推迟到请求时。
func NewRotator(opts RotationOptions) (*Rotator, error) {
	if opts.Loader == nil {
		return nil, domain.NewError(domain.CodeInternal, "缺少凭据分组加载器")
	}
	cooldown := opts.Cooldown
	if cooldown <= 0 {
		cooldown = defaultCooldown
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Rotator{
		loader:   opts.Loader,
		cooldown: cooldown,
		clock:    clock,
		logger:   opts.Logger,
		cursors:  make(map[string]uint64),
		cooling:  make(map[string]time.Time),
	}, nil
}

// Begin 实现 domain.CredentialRotation：建立本次渠道尝试的轮换状态。
func (r *Rotator) Begin(ctx context.Context, _ domain.Route) context.Context {
	return context.WithValue(ctx, rotationStateKey{}, &attemptState{})
}

// Resolve 实现 Resolver：按本次尝试的轮换顺序取出下一份凭据。
//
// 没有尝试级状态时（Provider 被直接构造调用，而非经流水线）按「本次请求的第一条可用凭据」
// 处理，仍走同一套冷却跳过与游标起点，行为与流水线路径一致。
func (r *Rotator) Resolve(ctx context.Context, route domain.Route) (Credential, error) {
	state, _ := ctx.Value(rotationStateKey{}).(*attemptState)
	if state == nil {
		return r.resolveFirst(ctx, route)
	}
	if err := r.prepare(ctx, route, state); err != nil {
		return Credential{}, err
	}
	if state.tried >= len(state.order) {
		return Credential{}, domain.NewError(domain.CodeInternal,
			fmt.Sprintf("凭据分组 %q 的可用凭据已试遍", route.CredentialRef))
	}
	entry := state.group.Entries[state.order[state.tried]]
	state.tried++
	state.current = &entry
	return Credential{APIKey: entry.APIKey}, nil
}

// Advance 实现 domain.CredentialRotation：凭据类失败后推进到下一条凭据。
func (r *Rotator) Advance(ctx context.Context, route domain.Route, failure error) (context.Context, bool) {
	if !domain.CredentialRejected(failure) {
		return ctx, false
	}
	state, _ := ctx.Value(rotationStateKey{}).(*attemptState)
	if state == nil || !state.loaded {
		return ctx, false
	}
	if failed := state.current; failed != nil {
		r.markCooling(state.group.Scope, failed.Name)
		r.logSwitch(route, *failed, state.tried, len(state.order))
	}
	if state.tried >= len(state.order) {
		return ctx, false
	}
	return ctx, true
}

// Renew 解除本次尝试所用凭据的既有冷却：满足流水线的可选能力 credentialRenewer。
//
// 只认「上游明确声明」这一事实，不把一次成功的上游调用当作续期事后补记：
// 成功只说明本次请求可用，并不意味着此前那次凭据类失败已经恢复。
func (r *Rotator) Renew(ctx context.Context, route domain.Route) {
	state, _ := ctx.Value(rotationStateKey{}).(*attemptState)
	if state == nil || !state.loaded || state.current == nil {
		return
	}
	if r.clearCooling(state.group.Scope, state.current.Name) {
		r.logRenew(route, *state.current)
	}
}

// prepare 在第一次解析时加载分组并算出本次尝试的试用顺序。
func (r *Rotator) prepare(ctx context.Context, route domain.Route, state *attemptState) error {
	if state.loaded {
		return nil
	}
	group, err := r.loader.LoadGroup(ctx, route)
	if err != nil {
		return err
	}
	if len(group.Entries) == 0 {
		return domain.NewError(domain.CodeInternal,
			fmt.Sprintf("凭据分组 %q 没有可用凭据", route.CredentialRef))
	}
	state.group = group
	state.order = r.rotationOrder(group, route)
	state.loaded = true
	return nil
}

// resolveFirst 处理没有尝试级状态的解析：取轮换顺序里的第一条。
func (r *Rotator) resolveFirst(ctx context.Context, route domain.Route) (Credential, error) {
	group, err := r.loader.LoadGroup(ctx, route)
	if err != nil {
		return Credential{}, err
	}
	if len(group.Entries) == 0 {
		return Credential{}, domain.NewError(domain.CodeInternal,
			fmt.Sprintf("凭据分组 %q 没有可用凭据", route.CredentialRef))
	}
	order := r.rotationOrder(group, route)
	if len(order) == 0 {
		return Credential{}, domain.NewError(domain.CodeInternal,
			fmt.Sprintf("凭据分组 %q 没有可用凭据", route.CredentialRef))
	}
	return Credential{APIKey: group.Entries[order[0]].APIKey}, nil
}

// rotationOrder 算出本次尝试的凭据试用顺序：跳过冷却中的，按分组游标错开起点，再截到上限。
//
// 全部处于冷却时按原顺序全部取用（fail-open）：跳过会让本次请求无凭据可用，
// 直接以「分组没有可用凭据」失败，比再撞一次冷却中的 key 更差；冷却只是避免连撞，
// 不是把分组判定为不可用。
func (r *Rotator) rotationOrder(group Group, route domain.Route) []int {
	now := r.clock()
	order := make([]int, 0, len(group.Entries))
	skipped := make([]string, 0)
	for i, entry := range group.Entries {
		if r.coolingUntil(group.Scope, entry.Name, now) {
			skipped = append(skipped, entry.Name)
			continue
		}
		order = append(order, i)
	}
	switch {
	case len(order) == 0:
		r.logAllCooling(route, len(group.Entries))
		for i := range group.Entries {
			order = append(order, i)
		}
	case len(skipped) > 0:
		r.logCooldownSkip(route, skipped)
	}
	order = rotate(order, r.advanceCursor(cursorKey(group.Scope, route.CredentialRef)))
	if len(order) > maxTrialsPerAttempt {
		order = order[:maxTrialsPerAttempt]
	}
	return order
}

// rotate 把切片左移 start 位，使 order[start] 成为新的首位。
func rotate(order []int, start uint64) []int {
	if len(order) == 0 {
		return order
	}
	// 取模结果严格小于 len(order)，转 int 不会溢出。
	offset := int(start % uint64(len(order))) //nolint:gosec // G115：取模结果小于切片长度，不溢出。
	if offset == 0 {
		return order
	}
	rotated := make([]int, 0, len(order))
	rotated = append(rotated, order[offset:]...)
	rotated = append(rotated, order[:offset]...)
	return rotated
}

// advanceCursor 取分组当前游标并推进一位，使下一次请求从下一条凭据开始。
func (r *Rotator) advanceCursor(key string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	cursor := r.cursors[key]
	r.cursors[key] = cursor + 1
	return cursor
}

// markCooling 把一条凭据标记为冷却到 now+cooldown。
func (r *Rotator) markCooling(scope, name string) {
	if name == "" {
		// 名字缺失时无法定位冷却对象：宁可不冷却，也不要冷却到同组的其它凭据。
		return
	}
	key := coolingKey(scope, name)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cooling[key] = r.clock().Add(r.cooldown)
}

// clearCooling 解除一条凭据的冷却，返回它此前是否处于冷却中。
func (r *Rotator) clearCooling(scope, name string) bool {
	if name == "" {
		return false
	}
	key := coolingKey(scope, name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.cooling[key]; !ok {
		return false
	}
	delete(r.cooling, key)
	return true
}

// coolingUntil 报告一条凭据是否仍在冷却中，并顺手清掉已到期的记录。
func (r *Rotator) coolingUntil(scope, name string, now time.Time) bool {
	key := coolingKey(scope, name)
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.cooling[key]
	if !ok {
		return false
	}
	if !until.After(now) {
		delete(r.cooling, key)
		return false
	}
	return true
}

// coolingKey 由归属、分组与凭据名拼出冷却键。
func coolingKey(scope, name string) string {
	return scope + "\x00" + name
}

// cursorKey 由归属与分组拼出轮换游标键。
func cursorKey(scope, group string) string {
	return scope + "\x00" + group
}

// keyPrefix 返回密钥的前若干字符，供日志人工比对换的是哪一把。
//
// 只取前缀且不参与鉴权：完整密钥与可拼回完整密钥的后缀都不落日志。
func keyPrefix(apiKey string) string {
	runes := []rune(apiKey)
	if len(runes) <= credentialLogPrefixRunes {
		return string(runes)
	}
	return string(runes[:credentialLogPrefixRunes])
}

// logSwitch 记录一次凭据切换，字段只含分组、凭据名与密钥前缀，不含 secret。
func (r *Rotator) logSwitch(route domain.Route, failed NamedCredential, tried, limit int) {
	if r.logger == nil {
		return
	}
	r.logger.Info("上游拒绝本次凭据，切换组内下一条",
		"cred_group", route.CredentialRef,
		"credential", failed.Name,
		"credential_prefix", keyPrefix(failed.APIKey),
		"tried", tried,
		"limit", limit,
	)
}

// logRenew 记录一次由上游声明登录态续期导致的冷却解除；只列出凭据名，不含 secret。
func (r *Rotator) logRenew(route domain.Route, renewed NamedCredential) {
	if r.logger == nil {
		return
	}
	r.logger.Info("上游声明凭据登录态已续期，解除冷却",
		"cred_group", route.CredentialRef,
		"credential", renewed.Name,
	)
}

// logCooldownSkip 记录本次解析跳过的冷却凭据；只列出凭据名。
func (r *Rotator) logCooldownSkip(route domain.Route, names []string) {
	if r.logger == nil {
		return
	}
	r.logger.Info("凭据处于冷却，本次跳过",
		"cred_group", route.CredentialRef,
		"credentials", names,
	)
}

// logAllCooling 记录「整组都在冷却、按 fail-open 全部取用」这一事实。
func (r *Rotator) logAllCooling(route domain.Route, total int) {
	if r.logger == nil {
		return
	}
	r.logger.Warn("凭据分组全部处于冷却，本次不跳过",
		"cred_group", route.CredentialRef,
		"credentials", total,
	)
}
