package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Calcium-Ion/moejs"

	"github.com/sumwai/tokenmp/internal/domain"
)

// 控制台输出与日志的上限与限频。
const (
	// consoleGlobal 是注入到插件的控制台全局名。
	consoleGlobal = "console"
	// consoleMaxRunes 是单条控制台日志的字符数上限。
	consoleMaxRunes = 512
	// consolePerSecond 是每个中间件每秒放行的控制台日志条数上限。
	consolePerSecond = 20
	// failureLogPerSecond 是每个中间件每秒放行的失败日志条数上限，
	// 钩子失败与取用失败各自计数。取值低于控制台输出：失败日志每请求一条，
	// 热路径上会盖住其它记录，而它要说明的只是「仍在失败」与失败原因，5 条足以看到。
	failureLogPerSecond = 5
	// maxDeclaredEvents 是单个中间件声明的最大事件数，超过时只留痕提示，不拒绝加载。
	maxDeclaredEvents = 12
)

// pooledRuntime 是池中的一个运行时及其装载的模块。
type pooledRuntime struct {
	runtime *moejs.Runtime
	comp    *compiled
}

// acquire 取一个已装载当前模块的运行时：优先复用池中的，池中为空时新建。
//
// 池中运行时装载的是旧模块（热重载发生过后）时丢弃并继续取，绝不把旧模块
// 当作新模块使用。
func (m *Middleware) acquire() (*moejs.Runtime, *compiled, error) {
	comp := m.current()
	if comp == nil {
		return nil, nil, errors.New("中间件当前没有可用产物")
	}
	for {
		select {
		case pooled := <-m.pool:
			if pooled.comp == comp {
				pooled.runtime.ClearInterrupt()
				return pooled.runtime, comp, nil
			}
		default:
			runtime, err := m.newRuntime(comp)
			if err != nil {
				return nil, nil, err
			}
			return runtime, comp, nil
		}
	}
}

// newRuntime 新建一个运行时：注入控制台全局、按引擎选项限长动态代码，再求值模块。
func (m *Middleware) newRuntime(comp *compiled) (*moejs.Runtime, error) {
	runtime, err := m.newBaseRuntime()
	if err != nil {
		return nil, err
	}
	if err := m.loadModule(runtime, comp.module); err != nil {
		return nil, err
	}
	return runtime, nil
}

// newBaseRuntime 建一个与本包其余运行时同形的空运行时：同一套全局、同一套导入解析。
//
// 装配期探测导出必须走同一条构造路径，否则顶层使用 console 或动态导入的插件会在
// 探测时与真实运行时表现不一致。
func (m *Middleware) newBaseRuntime() (*moejs.Runtime, error) {
	runtime := moejs.NewRuntime(moejs.Options{
		MaxDynamicSource: m.opts.MaxDynamicSource,
		TimeZone:         time.UTC,
		Importer: &moejs.Importer{Resolve: func(referrer moejs.Referrer, specifier string) (*moejs.Module, error) {
			return m.compiler.resolve(referrer, specifier)
		}},
	})
	if err := m.installConsole(runtime); err != nil {
		return nil, err
	}
	return runtime, nil
}

// release 让运行时放掉本次请求的数据与可能残留的中断，再还回池；池满时交给 GC。
//
// 这里清中断是第二道防线：withTimeout 已经闭合了「定时器窗口内留下残留中断」这条路径
// （见它的注释），而池中的运行时还可能带着别处留下的中断。删掉这两行就会让下一次取用
// 以「timeout」的名义失败，且现场没有任何超时 —— 因此它不是可有可无的清理。
func (m *Middleware) release(runtime *moejs.Runtime, comp *compiled) {
	runtime.ReleaseCallData()
	runtime.ClearInterrupt()
	select {
	case m.pool <- &pooledRuntime{runtime: runtime, comp: comp}:
	default:
	}
}

// loadModule 在预算内求值模块；预算到期经中断停止顶层代码。
func (m *Middleware) loadModule(runtime *moejs.Runtime, module *moejs.Module) error {
	return withTimeout(m.opts.LoadTimeout, runtime, func() error {
		return runtime.Load(module)
	})
}

// withTimeout 在预算内执行 fn；到期时从另一 goroutine 中断运行时，fn 以中断错误返回。
//
// 定时器回调与调用方共用一把锁，使窗口闭合：回调拿到锁时若 fn 已返回就什么都不做；
// 调用方则一定在回调临界区之后才清中断。否则存在这样一段窗口 —— timer.Stop() 已返回
// false、回调尚未执行，此刻清中断先跑，回调再把中断置上，运行时就带着残留中断回池，
// 下一次取用以「timeout」的名义失败，而现场没有任何超时。
func withTimeout(budget time.Duration, runtime *moejs.Runtime, fn func() error) error {
	if budget <= 0 {
		return fn()
	}
	var mu sync.Mutex
	done := false
	timer := time.AfterFunc(budget, func() {
		mu.Lock()
		defer mu.Unlock()
		if done {
			return
		}
		runtime.Interrupt("timeout")
	})
	err := fn()
	mu.Lock()
	done = true
	mu.Unlock()
	timer.Stop()
	runtime.ClearInterrupt()
	return err
}

// buildContext 构造传给钩子的 ctx 对象。
//
// 字段固定为协议、模型、路径、插件配置、单请求共享状态与（可知时的）调用方归属；
// reject 是宿主函数，插件经它表达拒绝。
func (m *Middleware) buildContext(state *requestState, runtime *requestRuntime, req *domain.Request) (moejs.Value, error) {
	ctxValue := map[string]any{
		"protocol": string(req.Protocol),
		"model":    req.Model,
		"path":     state.path,
		"options":  runtime.comp.options,
		"state":    runtime.state,
		"reject":   m.rejectFunc(runtime),
	}
	if state.agent != nil {
		ctxValue["agent"] = state.agent.toMap()
	}
	return runtime.runtime.FromGo(ctxValue)
}

// rejectFunc 构造插件的 ctx.reject(status, message) 宿主函数。
func (m *Middleware) rejectFunc(runtime *requestRuntime) moejs.NativeFunc {
	return moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		status := 0
		switch raw := realm.ToGo(moejs.Arg(args, 0)).(type) {
		case int64:
			status = int(raw)
		case float64:
			status = int(raw)
		}
		message := ""
		if text, err := realm.ToString(moejs.Arg(args, 1)); err == nil {
			message = text.GoString()
		}
		runtime.rejected = true
		runtime.status = status
		runtime.message = message
		return moejs.Undefined(), nil
	})
}

// callRequest 调用一次 onRequest，返回改写后的请求体；未改写时为 nil。
func (m *Middleware) callRequest(state *requestState, runtime *requestRuntime, req *domain.Request) ([]byte, error) {
	body, err := runtime.runtime.ParseJSON(req.RawBody)
	if err != nil {
		return nil, fmt.Errorf("请求体无法解析为 JSON：%w", err)
	}
	ctxValue, err := m.buildContext(state, runtime, req)
	if err != nil {
		return nil, err
	}
	var result moejs.Value
	err = withTimeout(m.opts.RequestTimeout, runtime.runtime, func() error {
		var callErr error
		result, callErr = runtime.runtime.Call(runtime.comp.onRequest, body, ctxValue)
		return callErr
	})
	if err != nil {
		return nil, err
	}
	return m.bodyResult(runtime, result, hookOnRequest)
}

// callResponse 调用一次 onResponse，返回改写后的响应体；未改写时为 nil。
func (m *Middleware) callResponse(state *requestState, runtime *requestRuntime, req *domain.Request, body []byte) ([]byte, error) {
	arg, err := runtime.runtime.ParseJSON(body)
	if err != nil {
		return nil, fmt.Errorf("响应体无法解析为 JSON：%w", err)
	}
	ctxValue, err := m.buildContext(state, runtime, req)
	if err != nil {
		return nil, err
	}
	var result moejs.Value
	err = withTimeout(m.opts.RequestTimeout, runtime.runtime, func() error {
		var callErr error
		result, callErr = runtime.runtime.Call(runtime.comp.onResponse, arg, ctxValue)
		return callErr
	})
	if err != nil {
		return nil, err
	}
	return m.bodyResult(runtime, result, hookOnResponse)
}

// bodyResult 把钩子返回值转成请求体或响应体字节。
//
// undefined 与 null 都表示未改写；其它取值必须是 JSON 对象，非对象按非法返回处理，
// 由调用方继续按原样放行。
func (m *Middleware) bodyResult(runtime *requestRuntime, result moejs.Value, hook string) ([]byte, error) {
	if result.IsUndefined() || result.IsNull() {
		return nil, nil
	}
	if err := rejectThenable(result, hook); err != nil {
		return nil, err
	}
	encoded, err := runtime.runtime.AppendJSON(nil, result)
	if err != nil {
		return nil, err
	}
	if !isJSONObject(encoded) {
		return nil, fmt.Errorf("%s 返回值不是 JSON 对象", hook)
	}
	return encoded, nil
}

// rejectThenable 拦下 Promise 返回值，并说清原因。
//
// 钩子必须是同步函数：Runtime.Call 的返回值就直接被当作钩子返回值，宿主不 settle Promise。
// 而 async 写法在 JS 里是最自然的写法，返回的 Promise 经 JSON 序列化后是一个空对象
// （Promise 的自身可枚举属性为空），isJSONObject 只看首尾字节，于是会放行：
//
//	在 onResponse 上，响应体会被换成 {} 并直接写给客户端，而流水线不校验改写后的报文；
//	在 onRequest 上，请求体会被换成 {}，下游解码失败后报的却是「请求体无法解码」，
//	把排查方向引到请求体上。
//
// 所以这一条必须在此处拦下并把原因写在文案里，而不是交给下游的形状校验去猜。
func rejectThenable(result moejs.Value, hook string) error {
	if _, _, isPromise := moejs.PromiseResult(result); isPromise {
		return fmt.Errorf("%s 返回了 Promise：钩子必须是同步函数，async 写法不会被求值", hook)
	}
	return nil
}

// callEvent 调用一次 onEvent，返回改写后的分片与是否丢弃；未改写时返回 nil。
//
// null 表示丢弃；undefined 表示未改写。返回值不是对象或分片类型非法都按失败处理，
// 由调用方按原样放行。
func (m *Middleware) callEvent(state *requestState, runtime *requestRuntime, req *domain.Request, chunk domain.Chunk) (*domain.Chunk, bool, error) {
	arg, err := runtime.runtime.FromGo(chunkToMap(chunk))
	if err != nil {
		return nil, false, err
	}
	ctxValue, err := m.buildContext(state, runtime, req)
	if err != nil {
		return nil, false, err
	}
	var result moejs.Value
	err = withTimeout(m.opts.EventTimeout, runtime.runtime, func() error {
		var callErr error
		result, callErr = runtime.runtime.Call(runtime.comp.onEvent, arg, ctxValue)
		return callErr
	})
	if err != nil {
		return nil, false, err
	}
	if result.IsNull() {
		return nil, true, nil
	}
	if result.IsUndefined() {
		return nil, false, nil
	}
	if err = rejectThenable(result, hookOnEvent); err != nil {
		return nil, false, err
	}
	raw, err := runtime.runtime.ToGo(result)
	if err != nil {
		return nil, false, err
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%s 返回值不是对象", hookOnEvent)
	}
	rewritten, err := chunkFromMap(fields)
	if err != nil {
		return nil, false, fmt.Errorf("%s %w", hookOnEvent, err)
	}
	return &rewritten, false, nil
}

// callStreamEnd 调用一次 onStreamEnd，返回待补发的分片；无补发时返回 nil。
//
// undefined 与 null 都表示无补发。返回值必须是分片对象数组：非数组、含非对象项或分片
// kind 非法都按失败处理，由调用方跳过该中间件（不补发，也不向客户端报错）。
// 超时预算与逐事件钩子相同：它同样处在流式热路径上，只是每请求只跑一次。
func (m *Middleware) callStreamEnd(state *requestState, runtime *requestRuntime, req *domain.Request) ([]domain.Chunk, error) {
	ctxValue, err := m.buildContext(state, runtime, req)
	if err != nil {
		return nil, err
	}
	var result moejs.Value
	err = withTimeout(m.opts.EventTimeout, runtime.runtime, func() error {
		var callErr error
		result, callErr = runtime.runtime.Call(runtime.comp.onStreamEnd, ctxValue)
		return callErr
	})
	if err != nil {
		return nil, err
	}
	if result.IsUndefined() || result.IsNull() {
		return nil, nil
	}
	if err = rejectThenable(result, hookOnStreamEnd); err != nil {
		return nil, err
	}
	raw, err := runtime.runtime.ToGo(result)
	if err != nil {
		return nil, err
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s 返回值不是数组", hookOnStreamEnd)
	}
	chunks := make([]domain.Chunk, 0, len(items))
	for index, item := range items {
		fields, isObject := item.(map[string]any)
		if !isObject {
			return nil, fmt.Errorf("%s 的第 %d 项不是对象", hookOnStreamEnd, index)
		}
		chunk, chunkErr := chunkFromMap(fields)
		if chunkErr != nil {
			return nil, fmt.Errorf("%s 的第 %d 项：%w", hookOnStreamEnd, index, chunkErr)
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

// chunkToMap 把内部流式分片转成传给插件的 Go map。
//
// 只用内容分片携带的字段：用量与结束原因不属于逐事件钩子的输入。
func chunkToMap(chunk domain.Chunk) map[string]any {
	fields := map[string]any{"kind": string(chunk.Kind)}
	if chunk.Model != "" {
		fields["model"] = chunk.Model
	}
	if chunk.TextDelta != "" {
		fields["text_delta"] = chunk.TextDelta
	}
	if chunk.ToolCall != nil {
		fields["tool_call"] = map[string]any{
			"index":     chunk.ToolCall.Index,
			"id":        chunk.ToolCall.ID,
			"name":      chunk.ToolCall.Name,
			"arguments": chunk.ToolCall.Arguments,
		}
	}
	return fields
}

// contentKinds 是逐事件钩子可处置的分片类型。
var contentKinds = map[string]bool{
	string(domain.ChunkTextDelta):      true,
	string(domain.ChunkToolCallDelta):  true,
	string(domain.ChunkReasoningDelta): true,
}

// 分片形状非法时的原因。调用方补上钩子名与项下标后向上返回，由「钩子失败、按原样放行」路径处理。
var (
	errChunkKind  = errors.New("返回值不是合法的分片：kind 必须是 text_delta / tool_call_delta / reasoning_delta")
	errChunkIndex = errors.New("tool_call.index 必须是整数")
)

// chunkFromMap 把插件返回的 Go map 还原成内部流式分片。
//
// 只接受内容分片类型：用量、结束原因与流结束不得经逐事件钩子改写。
func chunkFromMap(fields map[string]any) (domain.Chunk, error) {
	kind, ok := fields["kind"].(string)
	if !ok || !contentKinds[kind] {
		return domain.Chunk{}, errChunkKind
	}
	chunk := domain.Chunk{Kind: domain.ChunkKind(kind)}
	if value, ok := fields["model"].(string); ok {
		chunk.Model = value
	}
	if value, ok := fields["text_delta"].(string); ok {
		chunk.TextDelta = value
	}
	if raw, ok := fields["tool_call"].(map[string]any); ok {
		call, callErr := toolCallFromMap(raw)
		if callErr != nil {
			return domain.Chunk{}, callErr
		}
		chunk.ToolCall = call
	}
	return chunk, nil
}

// toolCallFromMap 把插件返回的 tool_call 对象还原成内部结构。
//
// 字段缺失按零值处理；`index` 存在则必须是整数（见 chunkIndex）。
func toolCallFromMap(raw map[string]any) (*domain.ToolCall, error) {
	call := &domain.ToolCall{}
	if value, ok := raw["index"]; ok {
		index, valid := chunkIndex(value)
		if !valid {
			return nil, errChunkIndex
		}
		call.Index = index
	}
	if value, ok := raw["id"].(string); ok {
		call.ID = value
	}
	if value, ok := raw["name"].(string); ok {
		call.Name = value
	}
	if value, ok := raw["arguments"].(string); ok {
		call.Arguments = value
	}
	return call, nil
}

// chunkIndex 把插件给出的 index 转成整数。
//
// moejs 的 ToGo 对整数给 int64，对整值浮点（3.0）给 float64，两者都接受。
// 非整数（0.5）与超出 int 范围的取值同属非法返回：静默归 0 会让并行工具调用串位，
// 而这是最难从响应里看出来的一类错。
func chunkIndex(raw any) (int, bool) {
	switch value := raw.(type) {
	case int64:
		return int(value), true
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
			return 0, false
		}
		if value > math.MaxInt || value < math.MinInt {
			return 0, false
		}
		return int(value), true
	default:
		return 0, false
	}
}

// isJSONObject 报告字节是否是 JSON 对象的最外层形态。
func isJSONObject(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

// installConsole 注入 console 全局，把插件输出写进结构化日志并限频。
func (m *Middleware) installConsole(runtime *moejs.Runtime) error {
	handler := func(level slog.Level) moejs.NativeFunc {
		return moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			if m.console.allow() {
				m.consoleLog(level, truncateRunes(consoleText(realm, args), consoleMaxRunes))
			}
			return moejs.Undefined(), nil
		})
	}
	return runtime.SetGlobal(consoleGlobal, map[string]any{
		"log":   handler(slog.LevelInfo),
		"info":  handler(slog.LevelInfo),
		"warn":  handler(slog.LevelWarn),
		"error": handler(slog.LevelError),
		"debug": handler(slog.LevelDebug),
	})
}

// consoleLog 按日志级别写一条中间件控制台输出。
func (m *Middleware) consoleLog(level slog.Level, message string) {
	switch level {
	case slog.LevelDebug:
		m.logger.Debug("middleware_console", "plugin", m.name, "message", message)
	case slog.LevelWarn:
		m.logger.Warn("middleware_console", "plugin", m.name, "message", message)
	case slog.LevelError:
		m.logger.Error("middleware_console", "plugin", m.name, "message", message)
	default:
		m.logger.Info("middleware_console", "plugin", m.name, "message", message)
	}
}

// consoleText 把控制台参数拼成一行文本；对象按 JSON 输出。
func consoleText(realm *moejs.Realm, args []moejs.Value) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		if arg.IsObject() {
			if encoded, _, err := realm.AppendJSON(nil, arg); err == nil {
				parts = append(parts, string(encoded))
				continue
			}
		}
		if text, err := realm.ToString(arg); err == nil {
			parts = append(parts, text.GoString())
			continue
		}
		parts = append(parts, "[unprintable]")
	}
	return strings.Join(parts, " ")
}

// truncateRunes 把字符串截断到指定字符数，避免一条日志被无界文本撑大。
func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

// stackError 给引擎错误附上 JS 栈，并保留错误链供分类与 errors.As 使用。
type stackError struct {
	err   error
	stack string
}

// Error 返回错误文本加栈。
func (e *stackError) Error() string {
	return e.err.Error() + "\n" + e.stack
}

// Unwrap 暴露原错误，使 errors.As / errors.Is 仍能命中引擎错误类型。
func (e *stackError) Unwrap() error { return e.err }

// withJSStack 给异常补上 JS 栈。
//
// 引擎把 Exception.Stack 留空，栈挂在错误对象的 stack 属性上，只能向运行时取；
// 不取的话日志里只剩一句 "Error: boom"，在几十行的插件里等于零信息。
// StackTrace 的首行是 "Name: message"，与错误自身文本重复，附上前先去掉。
// 中断与内部错误没有栈可补，原样返回。
func withJSStack(rt *moejs.Runtime, err error) error {
	if rt == nil || err == nil {
		return err
	}
	var exc *moejs.Exception
	if !errors.As(err, &exc) {
		return err
	}
	stack := rt.StackTrace(exc)
	if first, rest, ok := strings.Cut(stack, "\n"); ok && strings.TrimSpace(first) == strings.TrimSpace(err.Error()) {
		stack = rest
	}
	if stack == "" || strings.Contains(err.Error(), stack) {
		return err
	}
	return &stackError{err: err, stack: stack}
}

// rateLimiter 是每秒固定条数的简单窗口限频器。
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Time
	count  int
}

// newRateLimiter 构造一个每秒放行 limit 条的限频器。
//
// 配额随实例走，两类日志不共用一个限频器：共用时一方的突发会挤掉另一方的记录。
func newRateLimiter(limit int) rateLimiter {
	return rateLimiter{limit: limit}
}

// allow 报告本秒窗口是否还在配额内。
func (l *rateLimiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.window) >= time.Second {
		l.window = now
		l.count = 0
	}
	if l.count >= l.limit {
		return false
	}
	l.count++
	return true
}

// throttledLog 是「限频 + 报出被压条数」的日志出口。
type throttledLog struct {
	limiter    rateLimiter
	suppressed atomic.Int64
}

// newThrottledLog 构造一个每秒放行 limit 条的日志出口。
func newThrottledLog(limit int) throttledLog {
	return throttledLog{limiter: newRateLimiter(limit)}
}

// log 在配额内写一条 Warn；超配额时只计数。
//
// 被压掉的条数在下一条放行的日志里以 suppressed 报出：完全静默会让人以为插件没出问题，
// 而逐条记录又会在故障时把日志刷满。
func (t *throttledLog) log(logger *slog.Logger, msg string, attrs ...any) {
	if !t.limiter.allow() {
		t.suppressed.Add(1)
		return
	}
	if suppressed := t.suppressed.Swap(0); suppressed > 0 {
		attrs = append(attrs, "suppressed", suppressed)
	}
	logger.Warn(msg, attrs...)
}

// Agent 是可知时注入钩子上下文的调用方归属。
//
// 三个标识都取自鉴权结果，只在通过鉴权的请求上出现；未鉴权的请求（例如探活）没有它。
type Agent struct {
	AccountID  uint64 `json:"account_id"`
	MerchantID uint64 `json:"merchant_id"`
	APIKeyID   uint64 `json:"api_key_id"`
}

// toMap 把归属转成插件可见的 Go map，键名与 JSON 标签一致。
func (a Agent) toMap() map[string]any {
	return map[string]any{
		"account_id":  a.AccountID,
		"merchant_id": a.MerchantID,
		"api_key_id":  a.APIKeyID,
	}
}

// requestStateKey 是插件请求上下文在 context 里的键。
type requestStateKey struct{}

// requestStateFromContext 读取请求上下文；没有插件上下文时返回 nil。
func requestStateFromContext(ctx context.Context) *requestState {
	state, _ := ctx.Value(requestStateKey{}).(*requestState)
	return state
}

// pathKey 是请求路径在 context 里的键。
type pathKey struct{}

// WithPath 把请求路径写入上下文，供插件 ctx.path 使用。
func WithPath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, pathKey{}, path)
}

// PathFromContext 读取请求路径；未写入时为空串。
func PathFromContext(ctx context.Context) string {
	path, _ := ctx.Value(pathKey{}).(string)
	return path
}

// agentKey 是调用方归属在 context 里的键。
type agentKey struct{}

// WithAgent 把调用方归属写入上下文，供插件 ctx.agent 使用。
func WithAgent(ctx context.Context, agent Agent) context.Context {
	return context.WithValue(ctx, agentKey{}, agent)
}

// AgentFromContext 读取调用方归属；未注入时第二个返回值为 false。
func AgentFromContext(ctx context.Context) (Agent, bool) {
	agent, ok := ctx.Value(agentKey{}).(Agent)
	return agent, ok
}
