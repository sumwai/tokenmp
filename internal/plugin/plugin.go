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
// 都返回错误：非法配置在启动期报出，不推迟到第一个请求。
func Load(paths []string, opts Options) (*Set, error) {
	opts = opts.withDefaults()
	set := &Set{opts: opts}
	for _, path := range paths {
		middleware, err := loadMiddleware(path, opts)
		if err != nil {
			return nil, err
		}
		set.items = append(set.items, middleware)
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
}

// handles 报告事件白名单是否覆盖该事件名。
func (c *compiled) handles(kind string) bool {
	return c.hasOnEvent && c.events[kind]
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

	mu  sync.Mutex
	cur *compiled
}

// loadMiddleware 完成一个中间件的启动期装配：解析入口、编译链接、解析钩子与选项。
func loadMiddleware(path string, opts Options) (*Middleware, error) {
	entry, root, name, err := resolveEntry(path)
	if err != nil {
		return nil, err
	}
	comp, err := newCompiler(root)
	if err != nil {
		return nil, err
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	middleware := &Middleware{
		name:     name,
		entry:    entry,
		opts:     opts,
		logger:   logger,
		compiler: comp,
		pool:     make(chan *pooledRuntime, opts.PoolSize),
	}
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
	events, options, err := m.probeExports(module)
	if err != nil {
		return nil, err
	}
	result.events, result.eventsList = events, sortedKeys(events)
	result.options = options
	if result.hasOnEvent && len(result.events) == 0 {
		// onEvent 导出但未声明 events 白名单：没有可调用的事件，按未启用处理并留痕。
		m.logger.Warn("中间件导出了 onEvent 但未声明 events 白名单，该钩子不会生效", "plugin", m.name)
		result.hasOnEvent = false
	}
	if len(result.eventsList) > maxDeclaredEvents {
		m.logger.Warn("中间件声明的事件白名单过长", "plugin", m.name, "events", len(result.eventsList))
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

// probeExports 在一个临时运行时里求值模块，读出 events 白名单与 options 配置。
func (m *Middleware) probeExports(module *moejs.Module) (map[string]bool, map[string]any, error) {
	runtime, err := m.newBaseRuntime()
	if err != nil {
		return nil, nil, err
	}
	if err := m.loadModule(runtime, module); err != nil {
		return nil, nil, fmt.Errorf("求值插件顶层失败：%w", err)
	}
	return readEvents(runtime), readOptions(runtime), nil
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
func (m *Middleware) current() *compiled {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil && !m.staleLocked() {
		return m.cur
	}
	next, err := m.assemble()
	if err != nil {
		m.logger.Warn("中间件热重载失败，继续使用上一份产物", "plugin", m.name, "error", err.Error())
		return m.cur
	}
	m.cur = next
	m.drainPoolLocked()
	m.logger.Info("中间件已热重载", "plugin", m.name, "entry", m.entry)
	return m.cur
}

// staleLocked 报告当前产物的任一依赖文件指纹是否已变化。
func (m *Middleware) staleLocked() bool {
	for _, dep := range m.cur.deps {
		stamp, err := statFile(dep)
		if err != nil || stamp != m.cur.stamps[dep] {
			return true
		}
	}
	return false
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
}

// Stats 返回全部中间件的统计快照，顺序与配置一致。
//
// 运行时代码池中新建的运行时需要重新求值模块，装配期已求值一次，故统计快照随取随算。
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
func (m *Middleware) info() Info {
	comp := m.current()
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
	return info
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
	path     string
	agent    *Agent
	runtimes []*requestRuntime
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
	state := &requestState{path: PathFromContext(ctx)}
	if agent, ok := AgentFromContext(ctx); ok {
		state.agent = &agent
	}
	for _, middleware := range s.items {
		runtime, comp, err := middleware.acquire()
		if err != nil {
			middleware.logger.Warn("中间件运行时不可用，本次请求跳过该插件",
				"plugin", middleware.name, "error", err.Error())
			continue
		}
		stateValue, err := runtime.FromGo(map[string]any{})
		if err != nil {
			middleware.release(runtime, comp)
			middleware.logger.Warn("中间件状态对象创建失败，本次请求跳过该插件",
				"plugin", middleware.name, "error", err.Error())
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
	for _, runtime := range state.runtimes {
		if !runtime.comp.hasOnRequest {
			continue
		}
		started := time.Now()
		rewritten, err := runtime.middleware.callRequest(state, runtime, req)
		runtime.middleware.stats.record(time.Since(started), err)
		if err != nil {
			runtime.middleware.logHookFailure(hookOnRequest, err)
			continue
		}
		if runtime.rejected {
			return rejectError(runtime.status, runtime.message)
		}
		if rewritten == nil {
			continue
		}
		if err := applyRequestRewrite(client, req, rewritten); err != nil {
			runtime.middleware.stats.record(0, err)
			runtime.middleware.logHookFailure(hookOnRequest, err)
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
	for _, runtime := range state.runtimes {
		if !runtime.comp.handles(string(current.Kind)) {
			continue
		}
		started := time.Now()
		next, drop, err := runtime.middleware.callEvent(state, runtime, req, current)
		runtime.middleware.stats.record(time.Since(started), err)
		if err != nil {
			runtime.middleware.logHookFailure(hookOnEvent, err)
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
	for _, runtime := range state.runtimes {
		if !runtime.comp.hasOnStreamEnd {
			continue
		}
		started := time.Now()
		chunks, err := runtime.middleware.callStreamEnd(state, runtime, req)
		runtime.middleware.stats.record(time.Since(started), err)
		if err != nil {
			runtime.middleware.logHookFailure(hookOnStreamEnd, err)
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
	for _, runtime := range state.runtimes {
		if !runtime.comp.hasResponse {
			continue
		}
		started := time.Now()
		next, err := runtime.middleware.callResponse(state, runtime, req, current)
		runtime.middleware.stats.record(time.Since(started), err)
		if err != nil {
			runtime.middleware.logHookFailure(hookOnResponse, err)
			continue
		}
		if next != nil {
			current = next
		}
	}
	return current
}

// logHookFailure 写一条钩子失败日志；失败一律按原样放行，因此固定记录放行语义。
func (m *Middleware) logHookFailure(hook string, err error) {
	m.logger.Warn("中间件钩子失败，按原样放行",
		"plugin", m.name, "hook", hook, "kind", hookErrorKind(err), "error", err.Error())
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
