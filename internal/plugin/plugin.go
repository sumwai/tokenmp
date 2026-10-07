// Package plugin 加载并运行网关中间件插件：用 moejs（纯 Go 的 JavaScript 引擎）
// 沙箱执行插件目录内的中间件文件，在请求改写、流式逐事件过滤与非流式响应改写
// 三个时机介入转发。
//
// # 安全口径
//
// 中间件只处理本机配置指定的本地文件。引擎不注入 fetch、XMLHttpRequest、定时器
// 与任何 Node API；模块导入被限制在插件目录内的相对路径；动态代码（eval 与
// Function 构造器）按字节数限长。因此插件能读到的请求与响应内容始终留在这个
// 进程内，不会被插件直接外发。
//
// 插件仍能读到完整的请求体与响应体，包括用户消息与模型回答：加载一个插件等同于
// 让该插件的代码接触这些内容。本包只应加载本机信任的插件。
//
// # 生命周期
//
// 每个中间件的模块编译一次，运行时按请求取用：一次请求从池中取一个运行时，
// 装载模块、按顺序调用三个钩子，请求结束后释放回池。运行时里模块顶层状态在
// 请求之间保留，因此插件应把逐请求状态放进 ctx.state，而不是模块级变量。
package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Calcium-Ion/moejs"

	"github.com/sumwai/tokenmp/internal/domain"
)

// 钩子预算与默认值。
const (
	// defaultRequestTimeout 是 onRequest 与 onResponse 的单次预算：
	// 两者面对的都是整段请求体或响应体，共用同一额度。
	defaultRequestTimeout = 250 * time.Millisecond
	// defaultEventTimeout 是 onEvent 的单次预算：逐事件处在流式热路径上，
	// 单事件预算必须显著小于整段预算，避免事件累积把流式延迟放大。
	defaultEventTimeout = 50 * time.Millisecond
	// defaultLoadTimeout 是模块求值（runtime.Load）的单次预算。
	// 编译本身是 Go 侧的解析，无法被中断；该预算只覆盖插件顶层代码的执行。
	defaultLoadTimeout = time.Second
	// defaultMaxDynamicSource 是 eval 与 Function 构造器可编译的源码字节上限。
	defaultMaxDynamicSource = 4 << 10
	// defaultPoolSize 是每个中间件缓存的运行时数量上限。
	defaultPoolSize = 8
	// defaultReloadBackoff 是重编译失败后的退避窗口。
	// 取秒级：写坏的文件往往会连着被读写，逐请求重试只会把编译开销平摊到每次转发上。
	defaultReloadBackoff = time.Second
)

// 模块导出名。
const (
	hookOnRequest  = "onRequest"
	hookOnEvent    = "onEvent"
	hookOnResponse = "onResponse"
	// hookOnStreamEnd 是流末补发钩子：逐事件钩子拿不到流结束分片，
	// 跨分片缓冲的改写靠它在终止帧前冲刷尾巴。
	hookOnStreamEnd = "onStreamEnd"
	exportEvents    = "events"
	exportOptions   = "options"
	// exportScope 是模块导出的作用域：声明只对哪些模型 / 方言 / 厂商生效，
	// 由宿主在 Go 侧判定，越界不进入 JS 运行时。
	exportScope = "scope"
)

// 作用域的轴名，与模块导出的 scope 对象的键一一对应。导出给装配方做漂移检查。
const (
	AxisModels    = "models"
	AxisProtocols = "protocols"
	AxisVendors   = "vendors"
)

// Options 是加载中间件的可调参数；零值字段取对应默认值。
type Options struct {
	// Logger 记录插件装载、钩子失败与控制台输出；nil 时不记录。
	Logger *slog.Logger
	// RequestTimeout 是 onRequest 与 onResponse 的单次预算。
	RequestTimeout time.Duration
	// EventTimeout 是 onEvent 的单次预算。
	EventTimeout time.Duration
	// LoadTimeout 是模块求值的单次预算。
	LoadTimeout time.Duration
	// MaxDynamicSource 是 eval 与 Function 构造器的源码字节上限；<= 0 时取默认值。
	MaxDynamicSource int
	// PoolSize 是每个中间件缓存的运行时数量上限；<= 0 时取默认值。
	PoolSize int
	// ReloadBackoff 是重编译失败后的退避窗口：窗口内不再检查指纹、也不再重试。
	// <= 0 时取默认值。
	ReloadBackoff time.Duration
}

// withDefaults 补齐零值字段。
func (o Options) withDefaults() Options {
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = defaultRequestTimeout
	}
	if o.EventTimeout <= 0 {
		o.EventTimeout = defaultEventTimeout
	}
	if o.LoadTimeout <= 0 {
		o.LoadTimeout = defaultLoadTimeout
	}
	if o.MaxDynamicSource <= 0 {
		o.MaxDynamicSource = defaultMaxDynamicSource
	}
	if o.PoolSize <= 0 {
		o.PoolSize = defaultPoolSize
	}
	if o.ReloadBackoff <= 0 {
		o.ReloadBackoff = defaultReloadBackoff
	}
	return o
}

// Set 是一组按配置顺序加载的中间件。
//
// 它同时实现 domain.StreamMiddleware 与 domain.ResponseMiddleware，供流水线注入；
// 请求改写由装配层在调用流水线之前经 OnRequest 触发。一次 Set 在装配完成后只读，
// 可供并发请求共享。
type Set struct {
	opts  Options
	items []*Middleware
}

// Load 加载配置里的全部中间件；空列表返回一个空 Set。
//
// 任一项不可用（路径不存在、后缀不符、包入口缺失、编译失败或钩子导出不是函数）
// 都返回错误：非法配置在启动期报出，不推迟到第一个请求。加载不因单项失败而中止，
// 一次报出全部不可用的项：多个插件同时写坏时，逐个修复逐次重启没有意义。
func Load(paths []string, opts Options) (*Set, error) {
	opts = opts.withDefaults()
	set := &Set{opts: opts}
	var failures []error
	for _, path := range paths {
		middleware, err := loadMiddleware(path, opts)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		set.items = append(set.items, middleware)
	}
	if len(failures) > 0 {
		return nil, errors.Join(failures...)
	}
	return set, nil
}

// Empty 报告是否没有加载任何中间件。
func (s *Set) Empty() bool {
	return s == nil || len(s.items) == 0
}

// compiled 是一次编译产物：链接后的模块、依赖指纹与已解析的钩子。
type compiled struct {
	module         *moejs.Module
	deps           []string
	stamps         map[string]fileStamp
	onRequest      moejs.Hook
	hasOnRequest   bool
	onEvent        moejs.Hook
	hasOnEvent     bool
	onResponse     moejs.Hook
	hasResponse    bool
	onStreamEnd    moejs.Hook
	hasOnStreamEnd bool
	events         map[string]bool
	eventsList     []string
	options        map[string]any
	// scope 是模块声明的作用域：越界的请求不进入 JS 运行时。
	// 零值表示不限制任何轴，行为与未声明 scope 一致。
	scope scopeSpec
}

// handles 报告事件白名单是否覆盖该事件名。
func (c *compiled) handles(kind string) bool {
	return c.hasOnEvent && c.events[kind]
}

// hooks 返回已解析的钩子，键为导出名；未导出的不出现在结果里。
//
// 供装配期校验可调用性：Module.Hook 只回答导出槽是否存在，形状问题要另判。
func (c *compiled) hooks() map[string]moejs.Hook {
	hooks := map[string]moejs.Hook{}
	if c.hasOnRequest {
		hooks[hookOnRequest] = c.onRequest
	}
	if c.hasOnEvent {
		hooks[hookOnEvent] = c.onEvent
	}
	if c.hasResponse {
		hooks[hookOnResponse] = c.onResponse
	}
	if c.hasOnStreamEnd {
		hooks[hookOnStreamEnd] = c.onStreamEnd
	}
	return hooks
}

// Middleware 是一个已加载的中间件：入口、编译缓存、运行时池与统计。
type Middleware struct {
	name     string
	entry    string
	opts     Options
	logger   *slog.Logger
	compiler *compiler
	pool     chan *pooledRuntime
	stats    stats
	console  rateLimiter
	hookLog  throttledLog
	// skipLog 限频「取不到运行时而跳过该插件」的日志。
	skipLog throttledLog
	// skipped 是因取不到运行时而被跳过的请求数。
	skipped atomic.Int64
	// reloading 是重编译的单飞闸：同一时刻只允许一个 goroutine 编译。
	// 抢不到闸的请求不排队等结果，直接用当前产物继续服务。
	reloading atomic.Bool
	// onReload 是产物换代回调，由装配方在开始服务之前注册；无回调时为空操作。
	onReload func()
	// runtimeDeps 是动态 import() 触及的文件与登记时的指纹。
	// 用 sync.Map：读在每次取用的指纹检查上，写在 JS 运行期的解析上，两者并发且互不阻塞。
	runtimeDeps sync.Map

	mu  sync.Mutex
	cur *compiled
	// reload 是重编译的状态位，与 cur 共锁。
	reload reloadState
	// skipError 是最近一次取用失败的原因。
	skipError string
}

// reloadState 是一个中间件的重编译状态。
type reloadState struct {
	// lastSuccess 是最近一次成功重编译的时刻；零值表示启动后未重编译过。
	lastSuccess time.Time
	// consecutiveFailures 是连续失败次数，成功一次即归零。
	consecutiveFailures int64
	// lastError 是最近一次失败原因；成功后清空。
	lastError string
	// backoffUntil 之前不再检查指纹、也不再重试。
	backoffUntil time.Time
}

// loadMiddleware 完成一个中间件的启动期装配：解析入口、编译链接、解析钩子与选项。
func loadMiddleware(path string, opts Options) (*Middleware, error) {
	entry, root, name, err := resolveEntry(path)
	if err != nil {
		return nil, err
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	middleware := &Middleware{
		name:    name,
		entry:   entry,
		opts:    opts,
		logger:  logger,
		pool:    make(chan *pooledRuntime, opts.PoolSize),
		console: newRateLimiter(consolePerSecond),
		hookLog: newThrottledLog(failureLogPerSecond),
		skipLog: newThrottledLog(failureLogPerSecond),
	}
	// 编译器要能回传运行期解析触及的文件，回调因此挂在 middleware 上，装配在它之后进行。
	comp, err := newCompiler(root, middleware.noteRuntimeDep)
	if err != nil {
		return nil, err
	}
	middleware.compiler = comp
	assembled, err := middleware.assemble()
	if err != nil {
		return nil, fmt.Errorf("插件 %s 装配失败：%w", path, err)
	}
	middleware.cur = assembled
	return middleware, nil
}

// assemble 编译入口模块、解析钩子，并探测事件白名单与插件配置。
func (m *Middleware) assemble() (*compiled, error) {
	module, deps, stamps, err := m.compiler.compileGraph(m.entry)
	if err != nil {
		return nil, err
	}
	result := &compiled{module: module, deps: deps, stamps: stamps}
	if result.onRequest, result.hasOnRequest, err = resolveHook(module, hookOnRequest); err != nil {
		return nil, err
	}
	if result.onEvent, result.hasOnEvent, err = resolveHook(module, hookOnEvent); err != nil {
		return nil, err
	}
	if result.onResponse, result.hasResponse, err = resolveHook(module, hookOnResponse); err != nil {
		return nil, err
	}
	if result.onStreamEnd, result.hasOnStreamEnd, err = resolveHook(module, hookOnStreamEnd); err != nil {
		return nil, err
	}
	exports, err := m.probeExports(module, result.hooks())
	if err != nil {
		return nil, err
	}
	result.events, result.eventsList = exports.events, sortedKeys(exports.events)
	result.options = exports.options
	result.scope = exports.scope
	if result.hasOnEvent && len(result.events) == 0 {
		// onEvent 导出但未声明 events 白名单：没有可调用的事件，按未启用处理并留痕。
		m.logger.Warn("中间件导出了 onEvent 但未声明 events 白名单，该钩子不会生效", "plugin", m.name)
		result.hasOnEvent = false
	}
	if len(result.eventsList) > maxDeclaredEvents {
		m.logger.Warn("中间件声明的事件白名单过长", "plugin", m.name, "events", len(result.eventsList))
	}
	if unknown := unknownEventNames(result.events); len(unknown) > 0 {
		// 拼错的事件名与「导出 onEvent 却没声明白名单」是同一类后果：钩子永不触发。
		// 但前者原先完全静默 —— hooks 列看起来正常、Calls 恒不增长，
		// 而后者有明确告警，同一份配置里两种反馈强度会让排查方向被带偏。
		m.logger.Warn("中间件声明了无法送达的事件名，这些事件不会被送到钩子",
			"plugin", m.name, "events", unknown)
	}
	return result, nil
}

// resolveHook 从模块里解析一个钩子导出。
//
// 未导出（含 undefined / null）视为该钩子不参与；导出但不是函数属配置错误。
func resolveHook(module *moejs.Module, name string) (moejs.Hook, bool, error) {
	hook, err := module.Hook(name)
	switch {
	case err == nil:
		return hook, true, nil
	case errors.Is(err, moejs.ErrHookNotFound):
		return moejs.Hook{}, false, nil
	default:
		return moejs.Hook{}, false, fmt.Errorf("导出 %s 不是函数：%w", name, err)
	}
}

// hookNames 是装配期校验可调用性的钩子，顺序固定使错误文案稳定。
var hookNames = []string{hookOnRequest, hookOnEvent, hookOnResponse, hookOnStreamEnd}

// logKeyError 是结构化日志里错误文本的键名。
//
// 抽成常量而不是到处写 "error"：同一个键名散在多个日志调用里，改口径时容易漏改。
const logKeyError = "error"

// verifyHooks 确认已解析的钩子确实是可调用的函数。
//
// moejs 的 Module.Hook 只回答导出槽是否存在：`export const onRequest = 42` 也返回成功，
// 于是 hooks 列显示正常、直到第一个请求才失败，属反向信心。可调用性只能在求值顶层之后
// 于运行时里判，因此这一步不在 resolveHook 里做。
func verifyHooks(runtime *moejs.Runtime, hooks map[string]moejs.Hook) error {
	for _, name := range hookNames {
		hook, ok := hooks[name]
		if !ok {
			continue
		}
		callable, err := runtime.Has(hook)
		if err != nil {
			return fmt.Errorf("导出 %s 无法解析：%w", name, err)
		}
		if !callable {
			return fmt.Errorf("导出 %s 不是函数", name)
		}
	}
	return nil
}

// probeExports 在一个临时运行时里求值模块，校验钩子可调用性，读出 events 白名单、
// options 配置与 scope 作用域。
func (m *Middleware) probeExports(module *moejs.Module, hooks map[string]moejs.Hook) (moduleExports, error) {
	runtime, err := m.newBaseRuntime()
	if err != nil {
		return moduleExports{}, err
	}
	if err = m.loadModule(runtime, module); err != nil {
		// 顶层抛异常原先只有一句错误文本、没有抛出位置；栈在错误对象上，向运行时取。
		return moduleExports{}, fmt.Errorf("求值插件顶层失败：%w", withJSStack(runtime, err))
	}
	if err = verifyHooks(runtime, hooks); err != nil {
		return moduleExports{}, err
	}
	scope, err := readScope(runtime)
	if err != nil {
		// 作用域写坏按「不限制」处理并留痕：让插件静默失效正是作用域要避免的失败形态，
		// 但也不该因为一个声明性的优化项就让整个插件加载失败。
		m.logger.Warn("中间件的 scope 声明非法，本次按不限制处理",
			"plugin", m.name, logKeyError, err.Error())
		scope = scopeSpec{}
	}
	return moduleExports{
		events:  readEvents(runtime),
		options: readOptions(runtime),
		scope:   scope,
	}, nil
}

// moduleExports 是一次模块求值的产物：逐事件白名单、插件配置与作用域声明。
type moduleExports struct {
	events  map[string]bool
	options map[string]any
	scope   scopeSpec
}

// unknownEventNames 返回声明了但不属于可处置分片类型的事件名，按字典序排列。
//
// 可处置集合由 contentKinds 定义：逐事件钩子只发内容分片，用量与结束原因不在其中。
func unknownEventNames(events map[string]bool) []string {
	unknown := map[string]bool{}
	for name := range events {
		if !contentKinds[name] {
			unknown[name] = true
		}
	}
	return sortedKeys(unknown)
}

// readEvents 读出模块导出的 events 白名单；未导出或形状非预期时返回空集。
func readEvents(runtime *moejs.Runtime) map[string]bool {
	events := map[string]bool{}
	value, ok := runtime.Export(exportEvents)
	if !ok {
		return events
	}
	raw, err := runtime.ToGo(value)
	if err != nil {
		return events
	}
	items, ok := raw.([]any)
	if !ok {
		return events
	}
	for _, item := range items {
		if name, ok := item.(string); ok && strings.TrimSpace(name) != "" {
			events[name] = true
		}
	}
	return events
}

// readOptions 读出模块导出的 options 配置；未导出或形状非预期时返回空对象。
//
// 返回空对象而不是 nil：ctx.options 始终是个可读属性，插件不必先判空。
func readOptions(runtime *moejs.Runtime) map[string]any {
	options := map[string]any{}
	value, ok := runtime.Export(exportOptions)
	if !ok {
		return options
	}
	raw, err := runtime.ToGo(value)
	if err != nil {
		return options
	}
	if parsed, ok := raw.(map[string]any); ok {
		return parsed
	}
	return options
}

// sortedKeys 返回集合的键，按字典序排列，使输出稳定。
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// current 返回当前生效的编译产物；文件指纹变化时惰性重编译。
//
// 重编译失败时保留上一份产物并留痕：一次写坏的插件不该让正在服务的网关整体失效。
// 三条可用性约束：
//   - 单飞：同一时刻只有一个 goroutine 编译，其余请求不排队，直接用当前产物；
//     否则 N 个并发请求会各编译一遍，并在锁上互相排队。
//   - 退避：失败后一个退避窗口内不再检查指纹、不再重试。写坏的文件会被一连串
//     请求反复撞上，逐请求重试等于把编译开销平摊到每次转发上。
//   - 兜底：编译失败不清空产物、不清空运行时池，旧产物继续服务。
func (m *Middleware) current() *compiled {
	now := time.Now()
	m.mu.Lock()
	cur := m.cur
	due := m.reloadDueLocked(now)
	m.mu.Unlock()

	if cur != nil && !due {
		return cur
	}
	// 需要重编译：只有抢到单飞闸的 goroutine 编译，其余不排队。
	m.recompile(reloadLazy)
	return m.snapshot()
}

// reloadMode 区分惰性重载与强制重载。
type reloadMode bool

const (
	// reloadLazy 按指纹判断，缓存命中即复用已编译模块。
	reloadLazy reloadMode = false
	// reloadForced 忽略指纹并丢掉编译缓存，重新读文件。
	// 指纹是「修改时间 + 大小」，cp -p、同尺寸覆盖、网络文件系统丢精度都会漏检；
	// 强制重载是不依赖指纹的兜底，因此连缓存一起丢，否则缓存会把旧模块还回来。
	reloadForced reloadMode = true
)

// recompile 在锁外编译一次并换代；抢不到单飞闸时直接返回。
//
// 返回值不给调用方，因为它取决于另一把锁上的进度：抢不到闸的调用方取快照即可，
// 拿到的是此刻真正的当前产物。
func (m *Middleware) recompile(mode reloadMode) {
	if !m.reloading.CompareAndSwap(false, true) {
		return
	}
	defer m.reloading.Store(false)

	if mode == reloadForced {
		m.compiler.reset()
	}
	// 编译在锁外进行：持锁编译会把所有请求一起挡在门外。
	next, err := m.assemble()

	reloaded := false
	m.mu.Lock()
	if err != nil {
		m.reloadFailureLocked(err, time.Now())
	} else {
		m.cur = next
		m.drainPoolLocked()
		m.reloadSuccessLocked(time.Now())
		reloaded = true
	}
	m.mu.Unlock()

	// 回调在锁外调用：它可能去读配置、跑一致性检查，不能把持锁时间拉长到那上面。
	if reloaded && m.onReload != nil {
		m.onReload()
	}
}

// reloadDueLocked 报告是否该尝试重编译。调用方必须已持有 m.mu。
//
// 退避窗口内不做指纹检查：失败已经记过，再查一遍只会让每个请求都去 stat。
func (m *Middleware) reloadDueLocked(now time.Time) bool {
	if m.cur == nil {
		return true
	}
	if now.Before(m.reload.backoffUntil) {
		return false
	}
	return m.staleLocked()
}

// reloadFailureLocked 记一次重编译失败并开启退避窗口。调用方必须已持有 m.mu。
func (m *Middleware) reloadFailureLocked(err error, now time.Time) {
	m.reload.consecutiveFailures++
	m.reload.lastError = err.Error()
	m.reload.backoffUntil = now.Add(m.opts.ReloadBackoff)
	m.logger.Warn("中间件热重载失败，继续使用上一份产物",
		"plugin", m.name, logKeyError, err.Error(),
		"consecutive_failures", m.reload.consecutiveFailures,
		"retry_after_ms", m.opts.ReloadBackoff.Milliseconds())
}

// reloadSuccessLocked 记一次重编译成功并清掉失败状态。调用方必须已持有 m.mu。
func (m *Middleware) reloadSuccessLocked(now time.Time) {
	m.reload.lastSuccess = now
	m.reload.consecutiveFailures = 0
	m.reload.lastError = ""
	m.reload.backoffUntil = time.Time{}
	m.refreshRuntimeDeps()
	m.logger.Info("中间件已热重载", "plugin", m.name, "entry", m.entry)
}

// snapshot 返回当前产物的只读快照，不触发重编译。//
// 状态读取不该有副作用：调用方只想知道此刻在跑什么，不该顺带把一次重编译拉进自己的路径。
func (m *Middleware) snapshot() *compiled {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur
}

// Reload 立刻重编译全部中间件，忽略文件指纹与退避窗口。
//
// 指纹是「修改时间 + 大小」，cp -p、网络文件系统丢精度、同尺寸覆盖一类操作会让它漏检；
// 该方法是不依赖指纹的兜底手段。触发源（信号、管理接口）不在本包内，由装配层决定。
func (s *Set) Reload() {
	if s == nil {
		return
	}
	for _, middleware := range s.items {
		middleware.recompile(reloadForced)
	}
}

// OnReload 注册产物换代回调：任一中间件热重载成功后调用一次。
//
// 回调在中间件锁外执行，且必须自己限时：一次重载发生在某个请求的 goroutine 上，
// 回调里做重活会把延迟加到那次转发上。装配方用它把「产物换代之后才成立」的检查
// 再跑一遍（例如作用域漂移），因此应在开始服务之前注册。
func (s *Set) OnReload(fn func()) {
	if s == nil {
		return
	}
	for _, middleware := range s.items {
		middleware.onReload = fn
	}
}

// staleLocked 报告当前产物的任一依赖文件指纹是否已变化。
//
// 静态导入来自产物快照，动态 import() 的文件来自运行期登记：两类都要看，
// 否则改了插件按需加载的子模块不会触发换代，池里那批运行时一直用旧模块。
func (m *Middleware) staleLocked() bool {
	for _, dep := range m.cur.deps {
		stamp, err := statFile(dep)
		if err != nil || stamp != m.cur.stamps[dep] {
			return true
		}
	}
	stale := false
	m.runtimeDeps.Range(func(key, value any) bool {
		path, ok := key.(string)
		want, ok2 := value.(fileStamp)
		if !ok || !ok2 {
			return true
		}
		stamp, err := statFile(path)
		if err != nil || stamp != want {
			stale = true
			return false
		}
		return true
	})
	return stale
}

// noteRuntimeDep 记录一次动态 import() 触及的文件与当时的指纹。
//
// 登记发生在 JS 运行期：这类文件不在启动期的静态模块图里，不补这一笔，
// 改动它既不会触发重载，也不会清掉已经装载旧模块的池中运行时。
func (m *Middleware) noteRuntimeDep(path string, stamp fileStamp) {
	m.runtimeDeps.Store(path, stamp)
}

// refreshRuntimeDeps 把运行期依赖的基线指纹刷成当前值。
//
// 重载成功后旧的一批基线已经过时：新产物按当前内容编译，基线不刷新会让同一个文件
// 被反复判成「又变了」。读不到的文件不刷新，保持「不匹配即过期」。
func (m *Middleware) refreshRuntimeDeps() {
	m.runtimeDeps.Range(func(key, _ any) bool {
		path, ok := key.(string)
		if !ok {
			return true
		}
		if stamp, err := statFile(path); err == nil {
			m.runtimeDeps.Store(path, stamp)
		}
		return true
	})
}

// drainPoolLocked 丢弃池中全部运行时；旧模块的运行时不能用于新产物。
func (m *Middleware) drainPoolLocked() {
	for {
		select {
		case <-m.pool:
		default:
			return
		}
	}
}

// Info 是一个中间件的可观测快照。
type Info struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Hooks     []string `json:"hooks"`
	Events    []string `json:"events"`
	Calls     int64    `json:"calls"`
	Failures  int64    `json:"failures"`
	AverageMS float64  `json:"average_ms"`
	LastError string   `json:"last_error,omitempty"`
	// ReloadedAt 是最近一次成功重编译的时刻（RFC3339）；空表示启动后未重编译过。
	ReloadedAt string `json:"reloaded_at,omitempty"`
	// ReloadFailures 是连续重编译失败次数；成功一次即归零。
	ReloadFailures int64 `json:"reload_failures,omitempty"`
	// ReloadError 是最近一次重编译失败的原因。
	ReloadError string `json:"reload_error,omitempty"`
	// Stale 表示文件指纹已变化但重编译尚未成功，即当前在跑旧版本。
	Stale bool `json:"stale,omitempty"`
	// Skipped 是因取不到运行时而被跳过的请求数；非零表示插件此刻并不在处理请求，
	// 而转发仍然成功——只看失败计数会以为一切正常。
	Skipped int64 `json:"skipped,omitempty"`
	// SkipError 是最近一次取用失败的原因。
	SkipError string `json:"skip_error,omitempty"`
}

// Stats 返回全部中间件的统计快照，顺序与配置一致。
//
// 该调用不触发重编译：它是状态读取，不是取用路径。但它为了算 stale 会对依赖文件做一遍
// 指纹比对（见 statFile），因此不适合高频轮询；将来的状态出口若按秒级采集，应避免直接
// 轮询本方法。
func (s *Set) Stats() []Info {
	if s == nil {
		return nil
	}
	infos := make([]Info, 0, len(s.items))
	for _, middleware := range s.items {
		infos = append(infos, middleware.info())
	}
	return infos
}

// info 汇总一个中间件的当前状态。
//
// 只读快照与状态位，不做重编译，也不改任何状态；stale 需要比对文件指纹，
// 因此该调用会 stat 一遍依赖文件，但它不在转发热路径上。
func (m *Middleware) info() Info {
	m.mu.Lock()
	comp := m.cur
	reload := m.reload
	skipError := m.skipError
	stale := comp != nil && m.staleLocked()
	m.mu.Unlock()

	info := Info{Name: m.name, Path: m.entry}
	if comp != nil {
		if comp.hasOnRequest {
			info.Hooks = append(info.Hooks, hookOnRequest)
		}
		if comp.hasOnEvent {
			info.Hooks = append(info.Hooks, hookOnEvent)
		}
		if comp.hasResponse {
			info.Hooks = append(info.Hooks, hookOnResponse)
		}
		if comp.hasOnStreamEnd {
			info.Hooks = append(info.Hooks, hookOnStreamEnd)
		}
		info.Events = append(info.Events, comp.eventsList...)
	}
	info.Calls, info.Failures, info.AverageMS, info.LastError = m.stats.snapshot()
	if !reload.lastSuccess.IsZero() {
		info.ReloadedAt = reload.lastSuccess.UTC().Format(time.RFC3339)
	}
	info.ReloadFailures = reload.consecutiveFailures
	info.ReloadError = reload.lastError
	info.Stale = stale
	info.Skipped = m.skipped.Load()
	info.SkipError = skipError
	return info
}

// ScopeDeclaration 是一个中间件声明的作用域取值，供装配方做漂移检查。
type ScopeDeclaration struct {
	// Middleware 是中间件名（入口文件名），用于告警定位。
	Middleware string
	// Values 按轴分组，只含声明过的轴。
	Values map[string][]string
}

// ScopeDeclarations 返回各中间件声明的作用域取值；未声明 scope 的中间件不出现在结果里。
//
// 装配方拿它与当前配置比对，把「声明了却一个都匹配不上」的名字报出来：三个名字轴都依赖
// 运营在数据里的命名，改了名字就会让作用域静默失效，靠告警而不是靠文档发现漂移。
func (s *Set) ScopeDeclarations() []ScopeDeclaration {
	if s == nil {
		return nil
	}
	var out []ScopeDeclaration
	for _, middleware := range s.items {
		comp := middleware.snapshot()
		if comp == nil {
			continue
		}
		values := comp.scope.declared()
		if len(values) == 0 {
			continue
		}
		out = append(out, ScopeDeclaration{Middleware: middleware.name, Values: values})
	}
	return out
}

// stats 累计一个中间件的调用数与耗时。
type stats struct {
	calls    atomic.Int64
	failures atomic.Int64
	nanos    atomic.Int64

	mu        sync.Mutex
	lastError string
}

// record 记一次钩子调用。
func (s *stats) record(elapsed time.Duration, err error) {
	s.calls.Add(1)
	s.nanos.Add(elapsed.Nanoseconds())
	if err == nil {
		return
	}
	s.failures.Add(1)
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
}

// snapshot 返回调用数、失败数、平均耗时与最后错误。
func (s *stats) snapshot() (calls, failures int64, averageMS float64, lastError string) {
	calls = s.calls.Load()
	failures = s.failures.Load()
	if calls > 0 {
		averageMS = float64(s.nanos.Load()) / float64(calls) / float64(time.Millisecond)
	}
	s.mu.Lock()
	lastError = s.lastError
	s.mu.Unlock()
	return calls, failures, averageMS, lastError
}

// requestState 是一次请求的插件上下文：每个中间件一个运行时与一个共享状态对象。
type requestState struct {
	path      string
	requestID string
	agent     *Agent
	runtimes  []*requestRuntime
}

// requestRuntime 是一个中间件在一次请求内的运行时。
type requestRuntime struct {
	middleware *Middleware
	comp       *compiled
	runtime    *moejs.Runtime
	state      moejs.Value
	rejected   bool
	status     int
	message    string
}

// release 把本次请求取用的运行时还回各自的池。
func (st *requestState) release() {
	for _, runtime := range st.runtimes {
		runtime.middleware.release(runtime.runtime, runtime.comp)
	}
	st.runtimes = nil
}

// BeginRequest 为一次请求建立插件上下文：为每个中间件取一个运行时并装载其模块，
// 返回携带该上下文的 ctx 与释放函数。
//
// 释放函数必须被调用且只调用一次，且必须在本次请求最后一次使用运行时之后调用；
// 装配层的转发装饰器把它 defer 在整段转发之外。
func (s *Set) BeginRequest(ctx context.Context, req *domain.Request) (context.Context, func()) {
	if s.Empty() || req == nil {
		return ctx, func() {}
	}
	requestID := req.RequestID
	if requestID == "" {
		requestID = "-"
	}
	state := &requestState{path: PathFromContext(ctx), requestID: requestID}
	if agent, ok := AgentFromContext(ctx); ok {
		state.agent = &agent
	}
	for _, middleware := range s.items {
		runtime, comp, err := middleware.acquire()
		if err != nil {
			middleware.noteSkip(err)
			continue
		}
		stateValue, err := runtime.FromGo(map[string]any{})
		if err != nil {
			middleware.release(runtime, comp)
			middleware.noteSkip(err)
			continue
		}
		state.runtimes = append(state.runtimes, &requestRuntime{
			middleware: middleware,
			comp:       comp,
			runtime:    runtime,
			state:      stateValue,
		})
	}
	return context.WithValue(ctx, requestStateKey{}, state), state.release
}

// OnRequest 在选路之前用各中间件改写请求体；返回非 nil 错误表示某个中间件拒绝该请求。
//
// 改写后的请求体经客户端适配器重新解码，使改写对选路可见（模型名、消息、参数都在其中）。
// 解码失败、钩子抛错或超时都按原样放行，不改写请求也不向上报错。
func (s *Set) OnRequest(ctx context.Context, client domain.Adapter, req *domain.Request) error {
	state := requestStateFromContext(ctx)
	if state == nil || client == nil || req == nil {
		return nil
	}
	// 本次调用的事实只算一次：循环里逐中间件重复求值没有任何好处。
	model, protocol, vendor, known := scopeFacts(ctx, req)
	for _, runtime := range state.runtimes {
		if !runtime.comp.hasOnRequest {
			continue
		}
		// 选路之前厂商未知，作用域里的厂商轴不参与判定（见 scopeSpec.allows）。
		if !runtime.comp.scope.allows(model, protocol, vendor, known) {
			continue
		}
		started := time.Now()
		rewritten, err := runtime.middleware.callRequest(state, runtime, req)
		if err == nil && rewritten != nil && !runtime.rejected {
			// 宿主侧解码失败属于这一次钩子调用的失败，并进同一条记录：
			// 另记一次会让 Calls 多算一拍，失败也落到第二次调用上。
			err = applyRequestRewrite(client, req, rewritten)
		}
		runtime.middleware.stats.record(time.Since(started), err)
		if err != nil {
			runtime.middleware.logHookFailure(runtime.runtime, state.requestID, hookOnRequest, err)
			continue
		}
		if runtime.rejected {
			return rejectError(runtime.status, runtime.message)
		}
	}
	return nil
}

// applyRequestRewrite 用改写后的请求体刷新内部请求。
//
// 协议、请求 id 与流式形态取自端点事实，不因请求体改写而变；模型名在改写后的
// 请求体未给出时保留原值（路径携带模型名的协议即属这种情形）。
func applyRequestRewrite(client domain.Adapter, req *domain.Request, body []byte) error {
	decoded, err := client.DecodeRequest(body)
	if err != nil {
		return fmt.Errorf("改写后的请求体无法解码：%w", err)
	}
	if decoded == nil {
		return errors.New("改写后的请求体解码为空")
	}
	if decoded.Model == "" {
		decoded.Model = req.Model
	}
	decoded.RequestID = req.RequestID
	decoded.Protocol = req.Protocol
	decoded.Stream = req.Stream
	*req = *decoded
	return nil
}

// OnEvent 在流式分片写回客户端之前依次询问各中间件。
//
// 实现 domain.StreamMiddleware：消费方是流水线的流式下沉目标。
func (s *Set) OnEvent(ctx context.Context, req *domain.Request, chunk domain.Chunk) domain.EventResult {
	state := requestStateFromContext(ctx)
	if state == nil {
		return domain.EventResult{Chunk: chunk, Unchanged: true}
	}
	current := chunk
	changed := false
	model, protocol, vendor, known := scopeFacts(ctx, req)
	for _, runtime := range state.runtimes {
		if !runtime.comp.handles(string(current.Kind)) {
			continue
		}
		if !runtime.comp.scope.allows(model, protocol, vendor, known) {
			continue
		}
		started := time.Now()
		next, drop, err := runtime.middleware.callEvent(state, runtime, req, current)
		runtime.middleware.stats.record(time.Since(started), err)
		if err != nil {
			runtime.middleware.logHookFailure(runtime.runtime, state.requestID, hookOnEvent, err)
			continue
		}
		if drop {
			return domain.EventResult{Drop: true}
		}
		if next != nil {
			changed = true
			current = *next
		}
	}
	if !changed {
		return domain.EventResult{Chunk: chunk, Unchanged: true}
	}
	return domain.EventResult{Chunk: current}
}

// OnStreamEnd 在流式响应结束前依次交给各中间件补发分片。
//
// 实现 domain.StreamEndMiddleware 这一可选端口：消费方是流水线的流式下沉目标，
// 写出时机在终止帧之前。某条中间件失败只跳过它自己，其余中间件的补发照常进行。
func (s *Set) OnStreamEnd(ctx context.Context, req *domain.Request) []domain.Chunk {
	state := requestStateFromContext(ctx)
	if state == nil {
		return nil
	}
	var flushed []domain.Chunk
	model, protocol, vendor, known := scopeFacts(ctx, req)
	for _, runtime := range state.runtimes {
		if !runtime.comp.hasOnStreamEnd {
			continue
		}
		if !runtime.comp.scope.allows(model, protocol, vendor, known) {
			continue
		}
		started := time.Now()
		chunks, err := runtime.middleware.callStreamEnd(state, runtime, req)
		runtime.middleware.stats.record(time.Since(started), err)
		if err != nil {
			runtime.middleware.logHookFailure(runtime.runtime, state.requestID, hookOnStreamEnd, err)
			continue
		}
		flushed = append(flushed, chunks...)
	}
	return flushed
}

// OnResponse 在非流式响应体写回客户端之前依次交给各中间件改写。
//
// 实现 domain.ResponseMiddleware：消费方是流水线的非流式写出分支。
func (s *Set) OnResponse(ctx context.Context, req *domain.Request, body []byte) []byte {
	state := requestStateFromContext(ctx)
	if state == nil {
		return body
	}
	current := body
	model, protocol, vendor, known := scopeFacts(ctx, req)
	for _, runtime := range state.runtimes {
		if !runtime.comp.hasResponse {
			continue
		}
		if !runtime.comp.scope.allows(model, protocol, vendor, known) {
			continue
		}
		started := time.Now()
		next, err := runtime.middleware.callResponse(state, runtime, req, current)
		runtime.middleware.stats.record(time.Since(started), err)
		if err != nil {
			runtime.middleware.logHookFailure(runtime.runtime, state.requestID, hookOnResponse, err)
			continue
		}
		if next != nil {
			current = next
		}
	}
	return current
}

// logHookFailure 写一条钩子失败日志；失败一律按原样放行，因此固定记录放行语义。
//
// 日志按中间件限频：一次写坏的文件会让每个请求各失败一次，不限频时同一句话把日志刷满，
// 排障现场看不到别的东西。被压掉的条数在下一条放行的日志里报出，症状消失前不丢计数。
func (m *Middleware) logHookFailure(rt *moejs.Runtime, requestID, hook string, err error) {
	m.hookLog.log(m.logger, "中间件钩子失败，按原样放行",
		"plugin", m.name, "hook", hook, "kind", hookErrorKind(err),
		"request_id", requestID, logKeyError, withJSStack(rt, err).Error())
}

// noteSkip 记一次「取不到运行时而跳过该插件」。
//
// 转发放行不等于插件在工作：计数让人能回答「插件此刻到底有没有介入」这个问题，
// 日志则限频，否则一个不可用的插件会让每条请求各写一条。
func (m *Middleware) noteSkip(err error) {
	m.skipped.Add(1)
	m.mu.Lock()
	m.skipError = err.Error()
	m.mu.Unlock()
	m.skipLog.log(m.logger, "中间件取用失败，本次请求跳过该插件",
		"plugin", m.name, logKeyError, err.Error())
}

// hookErrorKind 把钩子错误归成便于聚合的类别。
func hookErrorKind(err error) string {
	var interrupted *moejs.InterruptedError
	var exception *moejs.Exception
	var syntax *moejs.SyntaxError
	var internal *moejs.InternalError
	switch {
	case errors.As(err, &interrupted):
		return "timeout"
	case errors.As(err, &exception):
		return "exception"
	case errors.As(err, &syntax):
		return "syntax"
	case errors.As(err, &internal):
		return "internal"
	default:
		return "error"
	}
}

// rejectError 把中间件的拒绝结论翻成统一错误，HTTP 状态码取插件给出的值。
func rejectError(status int, message string) *domain.Error {
	if status < 100 || status > 599 {
		status = http.StatusForbidden
	}
	if strings.TrimSpace(message) == "" {
		message = "请求被中间件拒绝"
	}
	err := domain.NewError(rejectCode(status), message)
	err.HTTPStatus = status
	return err
}

// rejectCode 按 HTTP 状态码选择统一错误码，使错误体与既有分类口径一致。
func rejectCode(status int) domain.Code {
	switch {
	case status == http.StatusUnauthorized:
		return domain.CodeUnauthorized
	case status == http.StatusForbidden:
		return domain.CodeForbidden
	case status == http.StatusTooManyRequests:
		return domain.CodeRateLimited
	case status == http.StatusNotFound:
		return domain.CodeNotFound
	case status >= 400 && status < 500:
		return domain.CodeInvalidRequest
	default:
		return domain.CodeInternal
	}
}
