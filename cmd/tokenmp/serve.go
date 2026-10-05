package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sumwai/tokenmp/internal/adapters/anthropic"
	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/adapters/openairesponses"
	"github.com/sumwai/tokenmp/internal/circuit"
	"github.com/sumwai/tokenmp/internal/config"
	"github.com/sumwai/tokenmp/internal/credential"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/observability"
	"github.com/sumwai/tokenmp/internal/pipeline"
	"github.com/sumwai/tokenmp/internal/quota"
	"github.com/sumwai/tokenmp/internal/ratelimit"
	"github.com/sumwai/tokenmp/internal/settlement"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/transport"
	"github.com/sumwai/tokenmp/internal/upstream"
)

// 本文件是 serve 子命令：装配网关、启动监听并在信号到达时优雅退出。
//
// 装配顺序与依赖方向一致（适配器 → 凭据 → 上游 → 选路 → 流水线 → 入口 → 鉴权），
// 任一步失败都在启动期报出，不把装配缺陷推到第一个请求。

const (
	// healthzPath 是健康检查的固定路径。
	healthzPath = "/healthz"
	// defaultCompleteTimeout 是未配置非流式整体超时时的兜底值。
	defaultCompleteTimeout = 120 * time.Second
	// 上游连接池里与 DefaultTransport 对齐的基本超时。
	upstreamDialTimeout           = 30 * time.Second
	upstreamKeepAlive             = 30 * time.Second
	upstreamTLSHandshakeTimeout   = 10 * time.Second
	upstreamExpectContinueTimeout = time.Second
	// readHeaderTimeout 是读取客户端请求头的最长等待时间，
	// 用来拦住只发一半请求头的连接占住服务端连接（Slowloris）。
	readHeaderTimeout = 30 * time.Second
	// shutdownTimeout 是收到退出信号后等待在途请求完成的最长时间。
	shutdownTimeout = 15 * time.Second
)

// supportedProtocols 是网关暴露的三种协议；顺序即端点的声明顺序。
//
// 端点路径只有 domain.Protocol.EndpointPath 一处来源，mux 注册与路径到适配器的映射
// 共用它，两处不会分叉。
var supportedProtocols = []domain.Protocol{
	domain.ProtocolOpenAIChat,
	domain.ProtocolOpenAIResponses,
	domain.ProtocolAnthropicMessages,
}

// gatewayStore 是装配层需要的存储能力。
//
// 抽成接口而不是直接依赖 *store.Store：装配测试注入内存替身即可覆盖三方言端到端路径，
// 不必连数据库。生产路径注入真实 Store。
type gatewayStore interface {
	LookupAPIKey(ctx context.Context, keyHash string, now time.Time) (*store.APIKeyAuth, error)
	RouteCandidates(ctx context.Context, channelType store.ChannelType, model string, merchantID uint64) ([]store.RouteCandidate, error)
	// RouteCandidatesAnyType 返回不限协议方言的候选渠道，供同协议缺位时的跨协议补段使用。
	RouteCandidatesAnyType(ctx context.Context, model string, merchantID uint64) ([]store.RouteCandidate, error)
	CredentialsByGroup(ctx context.Context, credGroup string, merchantID uint64) ([]store.Credential, error)
	// InsertUsage 写一条 billing_usage 流水（占位口径，结算失败时使用），返回新行 id。
	InsertUsage(ctx context.Context, row store.UsageRow) (uint64, error)
	// settlement.Repo 提供结算事务、账本查询与额度预检所需的账户账本读取。
	settlement.Repo
	// quota.Repo 提供窗口限额判定所需的限额定义与窗口用量聚合。
	quota.Repo
}

// 编译期断言：真实存储层满足装配层的依赖面。
var _ gatewayStore = (*store.Store)(nil)

// 编译期断言：熔断器同时满足流水线的熔断端口与「全部候选被拒时放行探测」的兜底能力。
// 兜底能力经类型断言检测，缺失时不会编译报错，因此在这里固定住。
var (
	_ pipeline.Breaker       = (*circuit.Breaker)(nil)
	_ pipeline.BreakerProber = (*circuit.Breaker)(nil)
)

// gatewayOptions 是装配的可调参数；零值字段取对应默认值。
type gatewayOptions struct {
	// CompleteTimeout 是非流式请求的整段超时：既是 handler 侧整体 deadline，
	// 也是渠道未配置超时时上游调用的兜底截止时间。
	CompleteTimeout time.Duration
	// StreamFirstByteTimeout、StreamIdleTimeout 是流式转发的两级超时：
	// 前者限住等待上游第一个字节的时长，后者限住两个字节之间的最大间隔。
	StreamFirstByteTimeout time.Duration
	StreamIdleTimeout      time.Duration
	// UpstreamMaxIdleConns、UpstreamMaxIdleConnsPerHost 与 UpstreamIdleConnTimeout
	// 是上游 HTTP 连接池参数；零值交由 http.Transport 自身语义处理。
	UpstreamMaxIdleConns        int
	UpstreamMaxIdleConnsPerHost int
	UpstreamIdleConnTimeout     time.Duration
	// MaxAttempts 是同协议段的尝试上限（含首次）；<= 0 时取流水线默认值。
	MaxAttempts int
	// CrossProtocolAttempts 是跨协议段的尝试上限；<= 0 时取流水线默认值。
	// 预算语义（含两段各自的默认值与总上限）只在流水线内定义一处，此处只做覆盖。
	CrossProtocolAttempts int
	// CredentialCooldown 是上游凭据遭遇凭据类失败后的冷却时长；非正时取凭据包的默认值。
	CredentialCooldown time.Duration
	// RateLimitWait 是渠道限流下等待令牌的最长时间；非正时取限流包的默认值。
	// 等待超时的渠道尝试按可重试失败换下一条候选，不直接回给客户端报错。
	RateLimitWait time.Duration
	// BreakerThreshold 是渠道连续失败多少次后熔断打开；非正时取熔断包的默认值。
	BreakerThreshold int
	// BreakerCooldown 是渠道熔断打开后多久允许一笔探测；非正时取熔断包的默认值。
	BreakerCooldown time.Duration
	// BreakerProbes 是半开态同时放行的探测条数；非正时取熔断包的默认值。
	BreakerProbes int
	// BreakerLogger 记录熔断状态迁移；nil 时不记录。
	BreakerLogger *slog.Logger
	// CredentialLogger 记录凭据冷却与切换；nil 时不记录。
	CredentialLogger *slog.Logger
	// Now 取当前时刻，用于密钥过期判定；为 nil 时取系统时钟。
	Now func() time.Time
	// UsageWriteTimeout 是写用量流水的耗时上限；非正时取默认值。
	UsageWriteTimeout time.Duration
	// Logger 是结构化请求日志实现；nil 时不记录。
	Logger transport.AccessLogger
	// Observer 记录每次上游尝试；nil 时不记录尝试级日志。
	Observer domain.Observer
}

// gateway 是一次装配的产物：HTTP 入口与它持有的连接资源。
type gateway struct {
	handler  http.Handler
	upstream *http.Client
}

// Close 释放装配持有的上游连接资源。
func (g *gateway) Close() {
	if g != nil && g.upstream != nil {
		g.upstream.CloseIdleConnections()
	}
}

// newGateway 完成一次装配。
func newGateway(st gatewayStore, opts gatewayOptions) (*gateway, error) {
	if st == nil {
		return nil, domain.NewError(domain.CodeInternal, "缺少存储层")
	}
	completeTimeout := opts.CompleteTimeout
	if completeTimeout <= 0 {
		completeTimeout = defaultCompleteTimeout
	}

	// 协议适配器做成单例：不持有跨请求业务状态（流式状态由 NewStream 派生），可按协议共享。
	adapters := map[domain.Protocol]domain.Adapter{
		domain.ProtocolOpenAIChat:        openaichat.New(),
		domain.ProtocolOpenAIResponses:   openairesponses.New(),
		domain.ProtocolAnthropicMessages: anthropic.New(),
	}
	lookupAdapter := func(protocol domain.Protocol) (domain.Adapter, error) {
		adapter, ok := adapters[protocol]
		if !ok {
			return nil, domain.NewError(domain.CodeInternal, fmt.Sprintf("没有协议 %q 的适配器", string(protocol)))
		}
		return adapter, nil
	}
	resolveAdapter := func(path string) (domain.Adapter, bool) {
		for _, protocol := range supportedProtocols {
			if protocol.EndpointPath() == path {
				adapter, ok := adapters[protocol]
				return adapter, ok
			}
		}
		return nil, false
	}

	rotation, err := credential.NewRotator(credential.RotationOptions{
		Loader:   storeCredentialGroupLoader{store: st},
		Cooldown: opts.CredentialCooldown,
		Logger:   opts.CredentialLogger,
	})
	if err != nil {
		return nil, err
	}
	upstreamHTTP := &http.Client{Transport: newUpstreamTransport(opts)}
	upstreamClient, err := upstream.New(upstream.Options{
		HTTPClient:             upstreamHTTP,
		Headers:                credential.NewWithResolver(rotation),
		Adapters:               lookupAdapter,
		DefaultTimeout:         completeTimeout,
		StreamFirstByteTimeout: opts.StreamFirstByteTimeout,
		StreamIdleTimeout:      opts.StreamIdleTimeout,
	})
	if err != nil {
		return nil, err
	}

	forwarder, err := pipeline.New(pipeline.Options{
		Adapters:    lookupAdapter,
		Upstream:    upstreamClient,
		Routes:      storeRouteResolver{store: st},
		Observer:    opts.Observer,
		Usage:       newUsageRecorder(st, settlement.New(st, slog.Warn), opts.UsageWriteTimeout, nil),
		Credentials: rotation,
		// 限流器按渠道 id 缓存：同一渠道的所有请求共享一个令牌桶与一个并发信号量。
		Limiter: ratelimit.NewManager(ratelimit.Options{MaxWait: opts.RateLimitWait}),
		// 熔断器按渠道标识统计连续上游硬故障，状态只存进程内。
		Breaker: circuit.NewBreaker(circuit.Options{
			FailureThreshold: opts.BreakerThreshold,
			Cooldown:         opts.BreakerCooldown,
			ProbeConcurrency: opts.BreakerProbes,
			Logger:           opts.BreakerLogger,
		}),
		MaxAttempts:           opts.MaxAttempts,
		CrossProtocolAttempts: opts.CrossProtocolAttempts,
	})
	if err != nil {
		return nil, err
	}

	handler, err := transport.New(transport.Options{
		Forwarder:       forwarder,
		Adapters:        resolveAdapter,
		CompleteTimeout: completeTimeout,
		Logger:          opts.Logger,
	})
	if err != nil {
		return nil, err
	}

	auth := newAuthenticator(st, opts.Now)
	mux := http.NewServeMux()
	// 健康检查独立于转发与鉴权：探活只关心进程是否在线，不应因密钥配置而失败。
	mux.HandleFunc(healthzPath, healthz)
	// 三个端点各自注册精确路径并套上鉴权；其余路径一律走下面的 JSON 404。
	for _, protocol := range supportedProtocols {
		mux.Handle(protocol.EndpointPath(), auth.middleware(handler))
	}
	mux.HandleFunc("/", notFoundJSON)

	return &gateway{handler: mux, upstream: upstreamHTTP}, nil
}

// newUpstreamTransport 按配置构造上游连接池。
//
// 不复用 http.DefaultTransport：它是进程级共享的全局对象，改其连接池参数会影响同进程里
// 所有 HTTP 客户端，关闭空闲连接也会连带关掉别人的。自建一份，附带上与 DefaultTransport
// 相同的基本超时；代理取自环境变量，与标准库一致。
func newUpstreamTransport(opts gatewayOptions) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   upstreamDialTimeout,
			KeepAlive: upstreamKeepAlive,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          opts.UpstreamMaxIdleConns,
		MaxIdleConnsPerHost:   opts.UpstreamMaxIdleConnsPerHost,
		IdleConnTimeout:       opts.UpstreamIdleConnTimeout,
		TLSHandshakeTimeout:   upstreamTLSHandshakeTimeout,
		ExpectContinueTimeout: upstreamExpectContinueTimeout,
	}
}

// healthz 是健康检查处理器：固定返回 200 与纯文本 ok。
//
// 不在此处探活依赖（如上游连通性）：那会让探活随上游抖动而失败，
// 进而在编排系统里反复重启一个本身健康的网关。
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

// notFoundJSON 是未注册路径的兜底处理器：回与鉴权失败同形的 JSON 404。
//
// 不走 transport 的原因是那里需要按协议编码错误体，而未注册路径没有可归的协议；
// 这里只能给一个与协议无关的统一错误体。
func notFoundJSON(w http.ResponseWriter, _ *http.Request) {
	writeJSONError(w, http.StatusNotFound, domain.CodeNotFound, "路径不存在")
}

// storeRouteResolver 是按数据库选路的 domain.RouteResolver。
//
// 商家来自鉴权上下文，不来自请求体：候选渠道必须与密钥所属商家一致，
// 否则一个商家的密钥可以打到另一个商家的渠道。
type storeRouteResolver struct {
	store gatewayStore
	// intN 是加权随机的随机源，返回 [0, n) 内的整数；nil 时用 defaultIntN。
	// 注入后同优先级候选的首选可被测试固定。
	intN func(int) int
}

// Candidates 返回本次请求按降级顺序排列的候选渠道。
//
// 分两级取候选：同协议查询先成段，不限协议的跨协议查询补段，两段都交给 routeChain 拼成
// 唯一回退链。两次查询都不做加权随机（那是选路策略，见 routeChain），只回答有哪些候选。
func (r storeRouteResolver) Candidates(ctx context.Context, req *domain.Request) ([]domain.Route, error) {
	if req == nil {
		return nil, nil
	}
	id, ok := identityFromContext(ctx)
	if !ok {
		// 未鉴权不应走到这里；没有生效商家时按无候选处理，由流水线统一回「无可用渠道」。
		return nil, nil
	}
	sameProtocol, err := r.store.RouteCandidates(ctx, store.ChannelType(req.Protocol), req.Model, id.merchantID)
	if err != nil {
		return nil, err
	}
	crossProtocol, err := r.store.RouteCandidatesAnyType(ctx, req.Model, id.merchantID)
	if err != nil {
		return nil, err
	}
	return routeChain(req.Protocol, sameProtocol, crossProtocol, r.intN), nil
}

// endpointURL 把库里存的根地址与协议端点段拼成完整上游地址。
//
// 库里只存到端点段之前（如 https://host/api/v3）：同一台主机上的不同协议端点因此共享一段根地址，
// 端点段由本次协议决定。拼接去掉根地址末尾的斜杠，避免出现 //chat/completions 这样的双斜杠。
func endpointURL(base string, protocol domain.Protocol) string {
	root := strings.TrimRight(strings.TrimSpace(base), "/")
	if root == "" {
		return ""
	}
	return root + protocol.EndpointSegment()
}

// storeCredentialGroupLoader 是按数据库读凭据的 credential.GroupLoader。
//
// 商家来自鉴权上下文：凭据分组只在商家内唯一，跨商家取用会把一个商家的密钥发给另一个商家的上游。
type storeCredentialGroupLoader struct {
	store gatewayStore
}

// LoadGroup 读本次路由对应分组的启用凭据，按 id 升序（存储层已定序）。
//
// 密钥字段写坏的行被跳过而不是让整个分组失败：一行配置错误不该把同组其它可用 key 一起废掉，
// 而「双行轮换期间旧行格式变了」正是本功能要兼顾的场景。全部行都不可用时返回空组，
// 由轮换器报出「分组没有可用凭据」。
func (l storeCredentialGroupLoader) LoadGroup(ctx context.Context, route domain.Route) (credential.Group, error) {
	id, ok := identityFromContext(ctx)
	if !ok {
		return credential.Group{}, domain.NewError(domain.CodeInternal, "缺少鉴权上下文，无法读取上游凭据")
	}
	rows, err := l.store.CredentialsByGroup(ctx, route.CredentialRef, id.merchantID)
	if err != nil {
		return credential.Group{}, err
	}
	entries := make([]credential.NamedCredential, 0, len(rows))
	for _, row := range rows {
		apiKey, err := credentialAPIKey(row.Secret)
		if err != nil {
			continue
		}
		entries = append(entries, credential.NamedCredential{Name: row.Name, APIKey: apiKey})
	}
	return credential.Group{Scope: strconv.FormatUint(id.merchantID, 10), Entries: entries}, nil
}

// upstreamCredentialSecret 是 upstream_credential.secret 里本网关认识的字段。
//
// 其余的厂商专属字段本轮不消费；多出来的键照常忽略。
type upstreamCredentialSecret struct {
	APIKey string `json:"api_key"`
}

// credentialAPIKey 从凭据 JSON 里取出 api_key；解析失败或缺少密钥时返回错误。
//
// 不返回空密钥：空密钥会把「凭据行写坏了」推迟成上游的 401，
// 排查从一个与配置无关的症状出发很难回到真正原因。
func credentialAPIKey(raw []byte) (string, error) {
	var secret upstreamCredentialSecret
	if err := json.Unmarshal(raw, &secret); err != nil {
		return "", domain.NewError(domain.CodeInternal, "上游凭据 JSON 无法解析").WithCause(err)
	}
	if strings.TrimSpace(secret.APIKey) == "" {
		return "", domain.NewError(domain.CodeInternal, "上游凭据缺少 api_key")
	}
	return secret.APIKey, nil
}

// cmdServe 是 serve 子命令的入口：读配置、开存储、跑迁移、装配并在信号到达时退出。
func cmdServe(stderr io.Writer) int {
	cfg, err := config.LoadServe()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "配置错误：%v\n", err)
		return exitFailure
	}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "启动失败：%v\n", err)
		return exitFailure
	}
	defer func() { _ = st.Close() }()

	// 迁移在启动时执行：表结构随二进制一同发布，启动即对齐，避免新版本对着旧表跑。
	if migrateErr := st.Migrate(ctx); migrateErr != nil {
		_, _ = fmt.Fprintf(stderr, "启动失败：%v\n", migrateErr)
		return exitFailure
	}

	gw, err := newGateway(st, gatewayOptions{
		CompleteTimeout:             cfg.CompleteTimeout,
		StreamFirstByteTimeout:      cfg.StreamFirstByteTimeout,
		StreamIdleTimeout:           cfg.StreamIdleTimeout,
		UpstreamMaxIdleConns:        cfg.UpstreamMaxIdleConns,
		UpstreamMaxIdleConnsPerHost: cfg.UpstreamMaxIdleConnsPerHost,
		UpstreamIdleConnTimeout:     cfg.UpstreamIdleConnTimeout,
		UsageWriteTimeout:           cfg.UsageWriteTimeout,
		CredentialCooldown:          cfg.CredentialCooldown,
		RateLimitWait:               cfg.RateLimitWait,
		BreakerThreshold:            cfg.BreakerThreshold,
		BreakerCooldown:             cfg.BreakerCooldown,
		BreakerProbes:               cfg.BreakerProbes,
		CredentialLogger:            observability.NewJSONLogger(os.Stdout),
		BreakerLogger:               observability.NewJSONLogger(os.Stdout),
		Logger:                      observability.NewAccessLogger(os.Stdout),
		Observer:                    observability.NewAttemptObserver(observability.NewJSONLogger(os.Stdout)),
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "启动失败：%v\n", err)
		return exitFailure
	}
	defer gw.Close()

	// 用 ListenConfig 而不是裸 net.Listen：监听也接受 context，
	// 使启动阶段的取消与超时有一条统一路径。
	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "启动失败：监听 %s 失败：%v\n", cfg.Listen, err)
		return exitFailure
	}

	server := &http.Server{
		Handler:           gw.handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	// 信号只决定「何时开始关」，退出码由 runServer 的返回值统一决定。
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runServer(sigCtx, server, ln); err != nil {
		_, _ = fmt.Fprintf(stderr, "运行失败：%v\n", err)
		return exitFailure
	}
	return exitOK
}

// runServer 在给定 listener 上提供服务，ctx 取消时优雅关闭并排空在途请求。
//
// 与 http.Server.ListenAndServe 的差别是 listener 由调用方提供：
// 监听失败（端口被占等）因此在启动路径上同步报出，而不是藏在一个后台 goroutine 的返回值里；
// 测试也能用临时端口走同一条启动与关闭路径。
func runServer(ctx context.Context, server *http.Server, ln net.Listener) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(ln) }()

	select {
	case err := <-serveErr:
		// 自己关掉时会以 http.ErrServerClosed 退出，那不是故障。
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		// 关闭用的 context 从 ctx 派生但不继承取消：ctx 此刻已取消，
		// 直接以它作父 context 会让 Shutdown 立即超时，在途请求得不到排空机会。
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		// 排空：Shutdown 返回后 Serve 必然已退出，收下它的结果避免 goroutine 悬空。
		<-serveErr
		return nil
	}
}
