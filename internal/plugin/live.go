package plugin

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/sumwai/tokenmp/internal/domain"
)

// 本文件是「按本机清单装配、可在运行期换装」的中间件集合。
//
// 装配方拿到的始终是同一个 Live：它把端口调用转发给「此刻那一代」集合，
// 于是 `admin plugin add/disable/del` 与文件修好后的重新装配都不需要重启进程。

// liveGenerationKey 是请求上下文里钉住的那一代集合的键。
type liveGenerationKey struct{}

// Live 是按本机清单装配的中间件集合，支持运行期换装。
//
// 一次换装对新请求立即生效；已在途的请求继续用它开始时的那一代 —— 请求状态与钩子
// 必须来自同一代，否则新集合的钩子会去读旧一代建出的运行时。
type Live struct {
	stateFile string
	opts      Options
	logger    *slog.Logger
	current   atomic.Pointer[Set]
	// reloading 是换装的单飞闸：连发 SIGHUP 只跑一轮。
	reloading atomic.Bool
	// onSwap 在换装成功后调用，供装配方重跑依赖产物的一致性检查。
	onSwap atomic.Pointer[func()]
}

// 编译期断言：Live 实现转发路径用到的全部端口。
var (
	_ domain.StreamMiddleware    = (*Live)(nil)
	_ domain.StreamEndMiddleware = (*Live)(nil)
	_ domain.ResponseMiddleware  = (*Live)(nil)
)

// NewLive 按清单装配第一代集合并返回。
//
// 清单读不出来是配置错误，直接报出；清单里装不上的插件只记 ERROR 并跳过 ——
// 一个写坏的文件不该拦住进程启动，运营要能让服务先起来再去修它。
// stateFile 为空表示没用清单（网关被直接构造的场合），得到空集合。
func NewLive(stateFile string, opts Options) (*Live, error) {
	opts = opts.withDefaults()
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	live := &Live{stateFile: stateFile, opts: opts, logger: logger}
	if stateFile == "" {
		empty, _ := LoadAvailable(nil, opts)
		live.current.Store(empty)
		return live, nil
	}
	registry, err := LoadRegistry(stateFile)
	if err != nil {
		return nil, err
	}
	live.current.Store(live.build(registry))
	return live, nil
}

// Reload 重读清单并换装：新增、停用、删除以及文件修好后重新装配都在这里生效。
//
// 单飞：连发信号只跑一轮。清单读不出来时保留当前一代并记 ERROR —— 配置读坏的那一瞬间
// 把插件全部摘掉，比继续用旧清单更危险。
func (l *Live) Reload() {
	if l == nil || !l.reloading.CompareAndSwap(false, true) {
		return
	}
	defer l.reloading.Store(false)

	registry, err := LoadRegistry(l.stateFile)
	if err != nil {
		l.logger.Error("插件清单读取失败，继续使用当前中间件集合", logKeyError, err.Error())
		return
	}
	l.swap(l.build(registry))
}

// build 按清单装配一代集合：装不上的项记 ERROR 并跳过。
//
// 集合内部的惰性重编译（依赖文件指纹变化）也接到换装通知上：产物换代后，
// 依赖它的一致性检查要重跑。
func (l *Live) build(registry *Registry) *Set {
	set, failures := LoadAvailable(registry.EnabledPaths(), l.opts)
	set.OnReload(l.notifySwap)
	for _, failure := range failures {
		l.logger.Error("中间件装配失败，本次跳过该插件",
			"path", failure.Path, logKeyError, failure.Reason.Error())
	}
	return set
}

// swap 换上新的一代并通知装配方。
func (l *Live) swap(set *Set) {
	previous := l.current.Swap(set)
	if previous == set {
		return
	}
	l.logger.Info("中间件集合已换装", "plugins", len(set.items))
	l.notifySwap()
}

// notifySwap 通知装配方「产物换代了」；回调由装配方在开始服务之前注册。
func (l *Live) notifySwap() {
	if fn := l.onSwap.Load(); fn != nil {
		(*fn)()
	}
}

// SetOnSwap 注册换装回调；装配方在开始服务之前设置。
func (l *Live) SetOnSwap(fn func()) {
	if l == nil {
		return
	}
	l.onSwap.Store(&fn)
}

// Current 返回此刻生效的集合。
func (l *Live) Current() *Set {
	if l == nil {
		return nil
	}
	return l.current.Load()
}

// generation 返回本次请求开始时钉住的那一代；没有钉住时取此刻的集合。
func (l *Live) generation(ctx context.Context) *Set {
	if set, ok := ctx.Value(liveGenerationKey{}).(*Set); ok {
		return set
	}
	return l.Current()
}

// Empty 报告此刻是否没有中间件。
func (l *Live) Empty() bool {
	return l.Current().Empty()
}

// BeginRequest 为一次请求建立插件上下文，并把这一代集合钉进上下文。
func (l *Live) BeginRequest(ctx context.Context, req *domain.Request) (context.Context, func()) {
	set := l.Current()
	if set.Empty() {
		return ctx, func() {}
	}
	ctx, release := set.BeginRequest(ctx, req)
	return context.WithValue(ctx, liveGenerationKey{}, set), release
}

// OnRequest 在选路之前用各中间件改写请求体。
func (l *Live) OnRequest(ctx context.Context, client domain.Adapter, req *domain.Request) error {
	set := l.generation(ctx)
	if set.Empty() {
		return nil
	}
	return set.OnRequest(ctx, client, req)
}

// OnEvent 在流式分片写回客户端之前依次询问各中间件。
func (l *Live) OnEvent(ctx context.Context, req *domain.Request, chunk domain.Chunk) domain.EventResult {
	set := l.generation(ctx)
	if set.Empty() {
		return domain.EventResult{Chunk: chunk, Unchanged: true}
	}
	return set.OnEvent(ctx, req, chunk)
}

// OnStreamEnd 在流式响应结束前依次交给各中间件补发分片。
func (l *Live) OnStreamEnd(ctx context.Context, req *domain.Request) []domain.Chunk {
	set := l.generation(ctx)
	if set.Empty() {
		return nil
	}
	return set.OnStreamEnd(ctx, req)
}

// OnResponse 在非流式响应体写回客户端之前依次交给各中间件改写。
func (l *Live) OnResponse(ctx context.Context, req *domain.Request, body []byte) []byte {
	set := l.generation(ctx)
	if set.Empty() {
		return body
	}
	return set.OnResponse(ctx, req, body)
}

// ScopeDeclarations 返回此刻这一代集合里的作用域声明。
func (l *Live) ScopeDeclarations() []ScopeDeclaration {
	return l.Current().ScopeDeclarations()
}
