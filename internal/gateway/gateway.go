// Package gateway 装配转发网关：把适配器、凭据、上游、流水线、入口与鉴权接起来，
// 并提供在给定 listener 上运行与优雅退出的入口。
//
// 从 cmd 下沉到本包：装配与运行属网关职责，留在命令包会让业务规则的装配细节
// 只能随命令包一起测试，且能力包之间没有可复用的装配入口。命令包只负责读配置、
// 开存储、调用本包并把退出码翻译出来。
//
// 装配顺序与依赖方向一致（适配器 → 凭据 → 上游 → 选路 → 流水线 → 入口 → 鉴权），
// 任一步失败都在启动期报出，不把装配缺陷推到第一个请求。
// 能力包不反向依赖本包：依赖方向是「本包组装能力包」，见 internal/arch_test.go。
package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/sumwai/tokenmp/internal/access"
	"github.com/sumwai/tokenmp/internal/adapters/anthropic"
	"github.com/sumwai/tokenmp/internal/adapters/gemini"
	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/adapters/openairesponses"
	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/adminapi"
	webauth "github.com/sumwai/tokenmp/internal/auth"
	"github.com/sumwai/tokenmp/internal/circuit"
	"github.com/sumwai/tokenmp/internal/credential"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/me"
	"github.com/sumwai/tokenmp/internal/partner"
	"github.com/sumwai/tokenmp/internal/pipeline"
	"github.com/sumwai/tokenmp/internal/plan"
	"github.com/sumwai/tokenmp/internal/plugin"
	"github.com/sumwai/tokenmp/internal/quota"
	"github.com/sumwai/tokenmp/internal/ratelimit"
	"github.com/sumwai/tokenmp/internal/requestlog"
	"github.com/sumwai/tokenmp/internal/route"
	"github.com/sumwai/tokenmp/internal/settlement"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/transport"
	"github.com/sumwai/tokenmp/internal/upstream"
	"github.com/sumwai/tokenmp/internal/usage"
	"github.com/sumwai/tokenmp/internal/user"
)

const (
	// HealthzPath 是健康检查的固定路径。
	HealthzPath = "/healthz"
	// defaultCompleteTimeout 是未配置非流式整体超时时的兜底值。
	defaultCompleteTimeout = 120 * time.Second
	// 上游连接池里与 DefaultTransport 对齐的基本超时。
	upstreamDialTimeout           = 30 * time.Second
	upstreamKeepAlive             = 30 * time.Second
	upstreamTLSHandshakeTimeout   = 10 * time.Second
	upstreamExpectContinueTimeout = time.Second
	// ReadHeaderTimeout 是读取客户端请求头的最长等待时间，
	// 用来拦住只发一半请求头的连接占住服务端连接（Slowloris）。
	ReadHeaderTimeout = 30 * time.Second
	// shutdownTimeout 是收到退出信号后等待在途请求完成的最长时间。
	shutdownTimeout = 15 * time.Second
	// defaultProbeInterval 是未配置上游套餐探针周期时的兜底值，
	// 与 internal/config 的默认值一致；快照新鲜度按它的两倍判定。
	defaultProbeInterval = 5 * time.Minute
	// snapshotStaleFactor 是快照过期判定的采集周期倍数：
	// 超过两个采集周期未采集成功的快照视为未知，路由放行而不是拿旧窗口数据误杀渠道。
	snapshotStaleFactor = 2
)

// supportedProtocols 是网关暴露的四种协议；顺序即端点的声明顺序。
//
// 端点前缀只有一处来源：固定端点路径取 domain.Protocol.EndpointPath，
// 路径含模型名或流式形态的协议取适配器声明的 domain.EndpointFormat.ClientPathPrefix。
// mux 注册与路径到适配器的映射共用该前缀，两处不会分叉。
var supportedProtocols = []domain.Protocol{
	domain.ProtocolOpenAIChat,
	domain.ProtocolOpenAIResponses,
	domain.ProtocolAnthropicMessages,
	domain.ProtocolGeminiGenerate,
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
	// UpdateCredentialSecret 覆盖一行凭据的 secret，供 OAuth 惰性续期写回。
	UpdateCredentialSecret(ctx context.Context, id uint64, secret []byte) error
	// InsertUsage 写一条 billing_usage 流水（占位口径，结算失败时使用），返回新行 id。
	InsertUsage(ctx context.Context, row store.UsageRow) (uint64, error)
	// Account 与 RecentUsage 供自助查询端点回账户编码与最近流水。
	Account(ctx context.Context, id uint64) (*store.Account, error)
	RecentUsage(ctx context.Context, accountID uint64, limit int) ([]store.UsageListRow, error)
	// AccountByOwner 供用户级端点把会话推导为作用域。
	AccountByOwner(ctx context.Context, userID uint64) (*store.Account, error)
	// InsertAPIKey、ListAPIKeysByAccount、APIKeyByID 与 SetAPIKeyEnabled
	// 供用户级端点的密钥自助管理使用。
	InsertAPIKey(ctx context.Context, k store.APIKey) (uint64, error)
	ListAPIKeysByAccount(ctx context.Context, accountID uint64, enabled *bool, limit, offset int) ([]store.APIKey, int, error)
	APIKeyByID(ctx context.Context, id uint64) (*store.APIKey, error)
	SetAPIKeyEnabled(ctx context.Context, id uint64, enabled bool) error
	// ListAccountUsage 与 ListAccountModels 供用户级端点的用量流水与模型目录使用。
	ListAccountUsage(ctx context.Context, f store.AccountUsageFilter) ([]store.AccountUsageRow, int, error)
	// AccountUsageStats 供用户级端点的用量聚合使用。
	AccountUsageStats(ctx context.Context, q store.UsageStatsQuery) ([]store.UsageStatsItem, error)
	ListAccountModels(ctx context.Context, accountID uint64) ([]store.AccountModel, error)
	// 请求记录相关方法供用户级端点的请求记录列表、详情与聚合使用。
	ListRequestLogs(ctx context.Context, f store.RequestLogFilter) ([]store.RequestLogRow, int, error)
	RequestLogByRequestID(ctx context.Context, accountID uint64, requestID string) (*store.RequestLogRow, error)
	RequestAttempts(ctx context.Context, requestID string) ([]store.RequestAttempt, error)
	RequestStats(ctx context.Context, q store.RequestStatsQuery) ([]store.RequestStatsItem, error)
	// 商品与订单：供用户级端点的商品目录与下单 / 订单列表使用。
	ListProducts(ctx context.Context) ([]store.Product, error)
	Product(ctx context.Context, id uint64) (*store.Product, error)
	CreateOrder(ctx context.Context, p store.Purchase, b store.BucketRow) (store.OrderWrite, error)
	ListOrdersByAccount(ctx context.Context, accountID uint64, limit, offset int) ([]store.OrderRow, int, error)
	// 商家域（页面 /api/v1/partner/*）相关方法：作用域由会话推导出的商家给定，
	// 每一处读写都按商家收敛；登记动作也落在这里，页面与管理面写同一张表。
	MerchantByOwner(ctx context.Context, userID uint64) (*store.Merchant, error)
	ChannelsByMerchant(ctx context.Context, merchantID uint64, enabled *bool, limit, offset int) ([]store.Channel, int, error)
	CredentialsByMerchant(ctx context.Context, merchantID uint64, limit, offset int) ([]store.CredentialRow, int, error)
	InsertChannel(ctx context.Context, c store.Channel) (uint64, error)
	InsertCredential(ctx context.Context, c store.CredentialRow) (uint64, error)
	SetChannelEnabledForMerchant(ctx context.Context, id, merchantID uint64, enabled bool) (bool, error)
	SetCredentialEnabledForMerchant(ctx context.Context, id, merchantID uint64, enabled bool) (bool, error)
	MerchantUsageStats(ctx context.Context, q store.MerchantUsageStatsQuery) ([]store.UsageStatsItem, error)
	// settlement.Repo 提供结算事务、账本查询与额度预检所需的账户账本读取。
	settlement.Repo
	// quota.Repo 提供窗口限额判定所需的限额定义与窗口用量聚合。
	quota.Repo
	// plan.Reader 提供上游套餐与限额行，供候选阶段跳过配额耗尽渠道。
	plan.Reader
	// plan.Repo 提供采集器所需的数据面（探针目标读取与采集结果写回）。
	plan.Repo
	// webauth.Store 提供页面认证所需的账号、会话与身份读写。
	webauth.Store
}

// 编译期断言：真实存储层满足装配层的依赖面。
var _ gatewayStore = (*store.Store)(nil)

// 编译期断言：真实存储层满足管理面的数据面（管理面清单的派生与脱敏由 internal/admin
// 的 Service 承担，其构造要求整张管理面数据面）。缺一个方法时在这里编译失败，
// 而不是等到运行期发现管理面子树没挂上。
var _ admin.Store = (*store.Store)(nil)

// 编译期断言：熔断器同时满足流水线的熔断端口与「全部候选被拒时放行探测」的兜底能力。
// 兜底能力经类型断言检测，缺失时不会编译报错，因此在这里固定住。
var (
	_ pipeline.Breaker       = (*circuit.Breaker)(nil)
	_ pipeline.BreakerProber = (*circuit.Breaker)(nil)
)

// Options 是装配的可调参数；零值字段取对应默认值。
type Options struct {
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
	// ProbeInterval 是上游套餐探针的采集周期；<= 0 时取 defaultProbeInterval。
	// 快照超过它的两倍视为未知，路由据此放行而不是拿过期快照误杀渠道。
	ProbeInterval time.Duration
	// BreakerLogger 记录熔断状态迁移；nil 时不记录。
	BreakerLogger *slog.Logger
	// CredentialLogger 记录凭据冷却与切换；nil 时不记录。
	CredentialLogger *slog.Logger
	// ProbeLogger 记录上游套餐采集结果；nil 时不记录。
	ProbeLogger *slog.Logger
	// OAuthRefreshWindow 是订阅型凭据的提前续期窗口；非正时取凭据包的默认值。
	OAuthRefreshWindow time.Duration
	// OAuthRefreshTimeout 是单次 OAuth 续期与等待续期的上限；非正时取凭据包的默认值。
	OAuthRefreshTimeout time.Duration
	// Now 取当前时刻，用于密钥过期判定；为 nil 时取系统时钟。
	Now func() time.Time
	// UsageWriteTimeout 是写用量流水的耗时上限；非正时取默认值。
	UsageWriteTimeout time.Duration
	// Logger 是结构化请求日志实现；nil 时不记录。
	Logger transport.AccessLogger
	// RequestLogLogger 记录请求记录落库失败；nil 时用默认 logger。
	RequestLogLogger *slog.Logger
	// Observer 记录每次上游尝试；nil 时不记录尝试级日志。
	Observer domain.Observer
	// PluginStateFile 是本机插件清单的位置（见 internal/plugin 的注册表）；
	// 空或清单不存在都表示不装配插件层。
	PluginStateFile string
	// PluginLogger 记录插件装载、钩子失败与控制台输出；nil 时不记录。
	PluginLogger *slog.Logger
	// WebSignupEnabled 是页面注册入口开关；关闭时 signup 回 403。
	WebSignupEnabled bool
	// WebTrustProxy 为真时页面认证按 X-Forwarded-For 首段做频率限制；
	// 仅在可信反代之后打开：直连暴露时该头可被客户端伪造。
	WebTrustProxy bool
	// WebAuthLogger 记录页面认证的安全事件（刷新令牌重放等）；nil 时不记录。
	WebAuthLogger *slog.Logger
	// WebMailer 是验证码投递端口；nil 表示邮件通道未配置，发送入口回 500。
	WebMailer webauth.Mailer
	// WebOAuthProviders 是已配置的第三方登录提供方；空表示不对外列出。
	WebOAuthProviders []webauth.OAuthProvider
}

// Gateway 是一次装配的产物：HTTP 入口与它持有的连接资源。
type Gateway struct {
	handler  http.Handler
	upstream *http.Client
	probes   *plan.Collector
	// plugins 是按本机清单装配的中间件集合，支持运行期换装（见 ReloadPlugins）。
	plugins *plugin.Live
	// scopeCheck 是「重载后重跑作用域漂移检查」的单飞闸。
	scopeCheck atomic.Bool
}

// Handler 返回网关的 HTTP 入口，供命令包构造 http.Server。
func (g *Gateway) Handler() http.Handler { return g.handler }

// StartProbes 启动上游套餐的周期采集，ctx 取消时退出；未装配采集器时为空操作。
//
// 采集在后台跑：探针不可达或配置写坏只记结构化日志，不影响任何转发路径。
func (g *Gateway) StartProbes(ctx context.Context) {
	if g == nil || g.probes == nil {
		return
	}
	go g.probes.Run(ctx)
}

// ReloadPlugins 重读本机清单并换装：新增、停用、删除与文件修好后的重新装配都在这里生效。
//
// 供装配层在收到外部触发（例如 SIGHUP）时调用。清单读不出来时保留当前集合，
// 单个插件装不上时只记 ERROR 并跳过它。
func (g *Gateway) ReloadPlugins() {
	if g == nil {
		return
	}
	g.plugins.Reload()
}

// Close 释放装配持有的上游连接资源。
func (g *Gateway) Close() {
	if g != nil && g.upstream != nil {
		g.upstream.CloseIdleConnections()
	}
}

// New 完成一次装配。
func New(st gatewayStore, opts Options) (*Gateway, error) {
	if st == nil {
		return nil, domain.NewError(domain.CodeInternal, "缺少存储层")
	}
	completeTimeout := opts.CompleteTimeout
	if completeTimeout <= 0 {
		completeTimeout = defaultCompleteTimeout
	}
	// 中间件插件按本机清单装配：清单读不出来是配置错误（报错退出）；清单里装不上的
	// 单个插件只记 ERROR 并跳过，不拦住进程启动（见 plugin.NewLive）。
	middleware, err := plugin.NewLive(opts.PluginStateFile, plugin.Options{Logger: opts.PluginLogger})
	if err != nil {
		return nil, err
	}
	// 作用域里的名字必须在当前配置里存在，否则插件会静默失效（见 warnScopeDrift）。
	// 存储层不满足读取面时跳过检查，不拦启动；换装后再查一遍。
	scopeReader, hasScopeReader := st.(scopeConfigReader)
	if hasScopeReader {
		warnScopeDrift(context.Background(), opts.PluginLogger, middleware, scopeReader)
	}

	// 协议适配器做成单例：不持有跨请求业务状态（流式状态由 NewStream 派生），可按协议共享。
	adapters := map[domain.Protocol]domain.Adapter{
		domain.ProtocolOpenAIChat:        openaichat.New(),
		domain.ProtocolOpenAIResponses:   openairesponses.New(),
		domain.ProtocolAnthropicMessages: anthropic.New(),
		domain.ProtocolGeminiGenerate:    gemini.New(),
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
			adapter, ok := adapters[protocol]
			if !ok {
				continue
			}
			// 端点格式由适配器声明：路径含模型名或流式后缀的协议经 MatchClientPath 判定，
			// 其余协议比对固定端点路径。
			if format, isFormatted := adapter.(domain.EndpointFormat); isFormatted {
				if _, _, matched := format.MatchClientPath(path); matched {
					return adapter, true
				}
				continue
			}
			if protocol.EndpointPath() == path {
				return adapter, true
			}
		}
		return nil, false
	}

	rotation, err := credential.NewRotator(credential.RotationOptions{
		Loader:   storeCredentialGroupLoader{store: st},
		Cooldown: opts.CredentialCooldown,
		Logger:   opts.CredentialLogger,
		Renewal: &credential.RenewalOptions{
			Window:    opts.OAuthRefreshWindow,
			Timeout:   opts.OAuthRefreshTimeout,
			Refresher: newOAuthCredentialRefresher(st, opts.CredentialLogger),
		},
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

	probeInterval := opts.ProbeInterval
	if probeInterval <= 0 {
		probeInterval = defaultProbeInterval
	}

	// 请求记录：把尝试、用量与访问三条事实落成可回溯的记录（见 internal/requestlog）。
	//
	// 存储能力就地断言成一个小接口，不加进 gatewayStore：缺失时只是不落请求记录，
	// 转发与计费照常，因此不把它变成装配期的硬依赖（与 pluginscope 同一取法）。
	// 三条出口用 tee 与另两条观测出口配对，任一出口换成别的实现都不影响对方。
	observer := opts.Observer
	var usageRecorder domain.UsageRecorder = usage.NewRecorder(st, settlement.New(st, slog.Warn), opts.UsageWriteTimeout, nil)
	accessLogger := opts.Logger
	if logStore, ok := st.(requestlog.Store); ok {
		// 未配置 logger 时留空：requestlog 自带默认出口，且 logger 的取值方法在
		// nil 接收者上会踩空，不能直接取 opts.RequestLogLogger.Warn。
		var logf func(msg string, args ...any)
		if opts.RequestLogLogger != nil {
			logf = opts.RequestLogLogger.Warn
		}
		requestLog := requestlog.New(requestlog.Options{
			Store: logStore,
			Now:   opts.Now,
			Logf:  logf,
		})
		observer = requestlog.ObserverTee{Primary: observer, Secondary: requestLog}
		usageRecorder = requestlog.UsageTee{Primary: usageRecorder, Secondary: requestLog}
		accessLogger = requestlog.AccessTee{Primary: accessLogger, Secondary: requestLog}
	}

	forwarder, err := pipeline.New(pipeline.Options{
		Adapters:    lookupAdapter,
		Upstream:    upstreamClient,
		Routes:      storeRouteResolver{store: st, now: opts.Now, probeInterval: probeInterval},
		Observer:    observer,
		Usage:       usageRecorder,
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
		// 端口注入的是 Live 而不是某一代集合：换装后新请求立即用到新集合，
		// 空的判断在 Live 里是一次原子读。
		Stream:   middleware,
		Response: middleware,
		// 中间件未介入某个透传帧是插件层的缺口，日志与插件层同一路：运维看一处。
		MiddlewareLogger: opts.PluginLogger,
	})
	if err != nil {
		return nil, err
	}

	// 请求改写必须发生在路由解析之前，因此由转发装饰器在流水线之外完成；
	// 逐事件与非流式响应改写经流水线端口注入，两者共用同一代集与同一请求状态。
	var forward transport.Forwarder = &pluginForwarder{inner: forwarder, plugins: middleware}

	handler, err := transport.New(transport.Options{
		Forwarder:       forward,
		Adapters:        resolveAdapter,
		CompleteTimeout: completeTimeout,
		Logger:          accessLogger,
	})
	if err != nil {
		return nil, err
	}

	// 采集器随装配创建，但不在 New 里启动：启动时机由命令包按进程生命周期决定，
	// 装配测试也不会因为起了一个后台循环而变得不确定。
	collector, err := plan.NewCollector(st, plan.CollectorOptions{
		Interval: probeInterval,
		Logger:   opts.ProbeLogger,
	})
	if err != nil {
		return nil, err
	}

	auth := access.NewAuthenticator(st, opts.Now)
	mux := http.NewServeMux()
	// 健康检查独立于转发与鉴权：探活只关心进程是否在线，不应因密钥配置而失败。
	mux.HandleFunc(HealthzPath, healthz)
	// 四条端点各自注册前缀并套上鉴权；其余路径一律走下面的 JSON 404。
	// 固定端点用完整路径精确匹配，含模型名的端点用前缀匹配子树。
	// 插件层始终套一层：把请求路径与鉴权归属写进上下文，供插件钩子消费。
	// 集合为空时不写上下文（见 pluginContextMiddleware），所以没装插件时没有额外分配。
	protocolHandler := pluginContextMiddleware(middleware, handler)
	for _, protocol := range supportedProtocols {
		mux.Handle(clientEndpointPrefix(adapters[protocol]), auth.Middleware(protocolHandler))
	}
	// 自助查询共用数据面同一鉴权中间件：头格式、密钥失效与账户停用三类失败
	// 与转发端点回同一种 401，不另写一套鉴权口径。
	meService := me.New(st, opts.Now)
	mux.Handle(me.AccountPath, auth.Middleware(me.NewHandler(meService)))
	// 页面认证挂在自己的子树：与数据面共用一个 mux，但走独立信封、独立错误码
	// 与独立凭据，子树内未声明的路径也回页面信封 404 而不是数据面错误体。
	webAuth := webauth.New(st, webauth.Options{
		SignupDisabled: !opts.WebSignupEnabled,
		TrustProxy:     opts.WebTrustProxy,
		Logger:         opts.WebAuthLogger,
		Mailer:         opts.WebMailer,
		OAuthProviders: opts.WebOAuthProviders,
	})
	mux.Handle(webauth.PathPrefix, webauth.NewHandler(webAuth))
	// 用户级业务数据挂在页面会话之上：作用域由会话推导，账户摘要复用自助查询的
	// 同一份实现，展示与判定不会各算一套。
	mux.Handle(user.PathPrefix, user.NewHandler(user.Options{
		Sessions: webAuth,
		Store:    st,
		Summary:  meService,
	}))
	// 管理面只读清单与用户面同一套会话令牌，差别只在权限判定（要求管理面身份）。
	// 清单的派生与脱敏复用 internal/admin 的 Service，因此需要存储层满足整张管理面
	// 数据面：真实存储层满足（见上面的编译期断言），测试替身不满足时不挂载该子树。
	if adminStore, ok := st.(admin.Store); ok {
		mux.Handle(adminapi.PathPrefix, adminapi.NewHandler(adminapi.Options{
			Sessions: webAuth,
			Lister:   admin.New(adminStore),
		}))
	}
	// 商家域挂在同一页面会话之上：归属由会话推导出的商家给定，读与写都按商家收敛，
	// 页面登记出来的行与管理面登记出来的行写同一张表。
	mux.Handle(partner.PathPrefix, partner.NewHandler(partner.Options{
		Sessions: webAuth,
		Store:    st,
	}))
	mux.HandleFunc("/", notFoundJSON)

	gw := &Gateway{handler: mux, upstream: upstreamHTTP, probes: collector, plugins: middleware}
	// 换装后重跑作用域漂移检查：运行中把 scope 里的名字改错不会有别的信号。
	// 注册在返回之前，换装只会在开始服务之后发生。
	if hasScopeReader {
		middleware.SetOnSwap(func() {
			gw.scheduleScopeDriftCheck(opts.PluginLogger, middleware, scopeReader)
		})
	}
	return gw, nil
}

// newUpstreamTransport 按配置构造上游连接池。
//
// 不复用 http.DefaultTransport：它是进程级共享的全局对象，改其连接池参数会影响同进程里
// 所有 HTTP 客户端，关闭空闲连接也会连带关掉别人的。自建一份，附带上与 DefaultTransport
// 相同的基本超时；代理取自环境变量，与标准库一致。
func newUpstreamTransport(opts Options) *http.Transport {
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
	access.WriteJSONError(w, http.StatusNotFound, domain.CodeNotFound, "路径不存在")
}

// clientEndpointPrefix 返回协议在客户端侧的注册前缀。
//
// 端点路径含模型名或流式后缀的协议由适配器声明（domain.EndpointFormat.ClientPathPrefix）；
// 固定端点路径的协议回退到 Protocol.EndpointPath。注册前缀与解析用的判定必须同源，
// 否则会出现「注册了路径却解析不出协议」或反之。
func clientEndpointPrefix(adapter domain.Adapter) string {
	if format, ok := adapter.(domain.EndpointFormat); ok {
		return format.ClientPathPrefix()
	}
	return adapter.Protocol().EndpointPath()
}

// storeRouteResolver 是按数据库选路的 domain.RouteResolver。
//
// 商家来自鉴权上下文，不来自请求体：候选渠道必须与密钥所属商家一致，
// 否则一个商家的密钥可以打到另一个商家的渠道。
type storeRouteResolver struct {
	store gatewayStore
	// intN 是加权随机的随机源，返回 [0, n) 内的整数；nil 时用 route 包的默认实现。
	// 注入后同优先级候选的首选可被测试固定。
	intN func(int) int
	// now 取当前时刻，用于快照新鲜度判定；nil 时取系统时钟。
	now func() time.Time
	// probeInterval 是采集周期；快照超过它的两倍视为未知并放行。
	probeInterval time.Duration
}

// Candidates 返回本次请求按降级顺序排列的候选渠道。
//
// 分两级取候选：同协议查询先成段，不限协议的跨协议查询补段，两段都交给 route.RouteChain 拼成
// 唯一回退链。两次查询都不做加权随机（那是选路策略，见 route.RouteChain），只回答有哪些候选。
//
// 在拼链之前按上游套餐跳过「配额耗尽且快照新鲜」的渠道：让它进候选只会被上游 429 挡回，
// 白白消耗一次尝试预算与一段延迟。与熔断、限流、凭据冷却正交叠加，各自独立判定。
func (r storeRouteResolver) Candidates(ctx context.Context, req *domain.Request) ([]domain.Route, error) {
	if req == nil {
		return nil, nil
	}
	id, ok := access.IdentityFromContext(ctx)
	if !ok {
		// 未鉴权不应走到这里；没有生效商家时按无候选处理，由流水线统一回「无可用渠道」。
		return nil, nil
	}
	sameProtocol, err := r.store.RouteCandidates(ctx, store.ChannelType(req.Protocol), req.Model, id.MerchantID)
	if err != nil {
		return nil, err
	}
	crossProtocol, err := r.store.RouteCandidatesAnyType(ctx, req.Model, id.MerchantID)
	if err != nil {
		return nil, err
	}
	exhausted := r.exhaustedCredGroups(ctx, id.MerchantID)
	sameProtocol = dropExhaustedCandidates(sameProtocol, exhausted)
	crossProtocol = dropExhaustedCandidates(crossProtocol, exhausted)
	routes := route.RouteChain(req.Protocol, sameProtocol, crossProtocol, r.intN)
	// 选路结果上的静态请求头还是渠道 config 的默认值，客户端带了的同名头在这里换成
	// 客户端的取值。放在选路之后是因为只有这里的 route 才知道本渠道声明了哪些头名。
	return route.WithClientHeaderOverrides(routes, req.Headers), nil
}

// exhaustedCredGroups 读出该商家下全部套餐并算出配额耗尽的凭据分组集合。
//
// 读取失败按「无法判定」处理：不阻断选路，也不拿一份读不到的状态去过滤候选。
// 这与判定本身的宽容语义一致 —— 套餐数据不该成为转发链路上的新单点。
func (r storeRouteResolver) exhaustedCredGroups(ctx context.Context, merchantID uint64) map[string]struct{} {
	if r.store == nil {
		return nil
	}
	plans, err := r.store.Plans(ctx, merchantID)
	if err != nil {
		slog.Warn("上游套餐读取失败，配额判定按未知放行", "merchant_id", merchantID, "error", err)
		return nil
	}
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	return plan.ExhaustedCredGroups(plans, now, snapshotStaleFactor*r.probeInterval)
}

// dropExhaustedCandidates 去掉凭据分组落在耗尽集合里的候选。
//
// 集合为空时原样返回，不分配新切片。返回的切片可能为空：全部候选都被跳过时由流水线
// 统一回「没有可用渠道」，不在这里另造退路 —— 拿已知耗尽的渠道去试与配额判定自相矛盾。
func dropExhaustedCandidates(candidates []store.RouteCandidate, exhausted map[string]struct{}) []store.RouteCandidate {
	if len(exhausted) == 0 {
		return candidates
	}
	kept := make([]store.RouteCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, blocked := exhausted[candidate.CredGroup]; blocked {
			continue
		}
		kept = append(kept, candidate)
	}
	return kept
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
	id, ok := access.IdentityFromContext(ctx)
	if !ok {
		return credential.Group{}, domain.NewError(domain.CodeInternal, "缺少鉴权上下文，无法读取上游凭据")
	}
	rows, err := l.store.CredentialsByGroup(ctx, route.CredentialRef, id.MerchantID)
	if err != nil {
		return credential.Group{}, err
	}
	entries := make([]credential.NamedCredential, 0, len(rows))
	for _, row := range rows {
		entry, err := credentialEntry(row)
		if err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return credential.Group{Scope: strconv.FormatUint(id.MerchantID, 10), Entries: entries}, nil
}

// credentialEntry 把一行凭据 secret 解析成轮换条目。
//
// 不返回空密钥：空密钥会把「凭据行写坏了」推迟成上游的 401，
// 排查从一个与配置无关的症状出发很难回到真正原因。
func credentialEntry(row store.Credential) (credential.NamedCredential, error) {
	parsed, err := credential.ParseSecret(row.Secret)
	if err != nil {
		return credential.NamedCredential{}, err
	}
	entry := credential.NamedCredential{ID: row.ID, Name: row.Name}
	if parsed.Kind == credential.KindOAuth {
		entry.APIKey = parsed.Access
		entry.OAuth = &credential.OAuthCredential{
			Access:  parsed.Access,
			Refresh: parsed.Refresh,
			Expires: parsed.Expires,
			Account: parsed.Account,
		}
		return entry, nil
	}
	entry.APIKey = parsed.APIKey
	return entry, nil
}

// Run 在给定 listener 上提供服务，ctx 取消时优雅关闭并排空在途请求。
//
// 与 http.Server.ListenAndServe 的差别是 listener 由调用方提供：
// 监听失败（端口被占等）因此在启动路径上同步报出，而不是藏在一个后台 goroutine 的返回值里；
// 测试也能用临时端口走同一条启动与关闭路径。
func Run(ctx context.Context, server *http.Server, ln net.Listener) error {
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
