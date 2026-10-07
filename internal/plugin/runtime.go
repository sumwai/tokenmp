package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Calcium-Ion/moejs"

	"github.com/sumwai/tokenmp/internal/domain"
)

// 控制台输出的上限与限频。
const (
	// consoleGlobal 是注入到插件的控制台全局名。
	consoleGlobal = "console"
	// consoleMaxRunes 是单条控制台日志的字符数上限。
	consoleMaxRunes = 512
	// consolePerSecond 是每个中间件每秒放行的控制台日志条数上限。
	consolePerSecond = 20
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
// 无论 fn 是否正常返回都停表并清中断：定时器可能恰好在 fn 返回后触发，
// 残留的中断会打断下一次调用。
func withTimeout(budget time.Duration, runtime *moejs.Runtime, fn func() error) error {
	if budget <= 0 {
		return fn()
	}
	timer := time.AfterFunc(budget, func() { runtime.Interrupt("timeout") })
	defer func() {
		timer.Stop()
		runtime.ClearInterrupt()
	}()
	return fn()
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
	encoded, err := runtime.runtime.AppendJSON(nil, result)
	if err != nil {
		return nil, err
	}
	if !isJSONObject(encoded) {
		return nil, fmt.Errorf("%s 返回值不是 JSON 对象", hook)
	}
	return encoded, nil
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
	raw, err := runtime.runtime.ToGo(result)
	if err != nil {
		return nil, false, err
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%s 返回值不是对象", hookOnEvent)
	}
	rewritten, ok := chunkFromMap(fields)
	if !ok {
		return nil, false, fmt.Errorf("%s 返回的分片缺少合法的 kind", hookOnEvent)
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
		chunk, isChunk := chunkFromMap(fields)
		if !isChunk {
			return nil, fmt.Errorf("%s 的第 %d 项缺少合法的 kind", hookOnStreamEnd, index)
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

// chunkFromMap 把插件返回的 Go map 还原成内部流式分片。
//
// 只接受内容分片类型：用量、结束原因与流结束不得经逐事件钩子改写。
func chunkFromMap(fields map[string]any) (domain.Chunk, bool) {
	kind, ok := fields["kind"].(string)
	if !ok || !contentKinds[kind] {
		return domain.Chunk{}, false
	}
	chunk := domain.Chunk{Kind: domain.ChunkKind(kind)}
	if value, ok := fields["model"].(string); ok {
		chunk.Model = value
	}
	if value, ok := fields["text_delta"].(string); ok {
		chunk.TextDelta = value
	}
	if raw, ok := fields["tool_call"].(map[string]any); ok {
		call := &domain.ToolCall{}
		if value, ok := raw["index"].(int64); ok {
			call.Index = int(value)
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
		chunk.ToolCall = call
	}
	return chunk, true
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

// rateLimiter 是每秒固定条数的简单窗口限频器。
type rateLimiter struct {
	mu     sync.Mutex
	window time.Time
	count  int
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
	if l.count >= consolePerSecond {
		return false
	}
	l.count++
	return true
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
