// Package config 从环境变量读取各子系统的运行配置。
//
// 刻意不引入配置文件框架：当前需要的字段很少，配置文件会带来格式、加载顺序
// 与热更新等一整套约定，而环境变量是容器化部署下的通用载体。
// 配置读取集中在此，业务包只接收构造好的结构体。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
)

// 环境变量名。集中声明，避免字符串散落在读取逻辑里。
const (
	envDSN             = "TOKENMP_MYSQL_DSN"
	envMaxOpenConns    = "TOKENMP_MYSQL_MAX_OPEN_CONNS"
	envMaxIdleConns    = "TOKENMP_MYSQL_MAX_IDLE_CONNS"
	envConnMaxLifetime = "TOKENMP_MYSQL_CONN_MAX_LIFETIME"
	envListen          = "TOKENMP_LISTEN"

	// 上游超时分级与连接池。
	envCompleteTimeout             = "TOKENMP_UPSTREAM_COMPLETE_TIMEOUT"
	envStreamFirstByteTimeout      = "TOKENMP_UPSTREAM_STREAM_FIRST_BYTE_TIMEOUT"
	envStreamIdleTimeout           = "TOKENMP_UPSTREAM_STREAM_IDLE_TIMEOUT"
	envUpstreamMaxIdleConns        = "TOKENMP_UPSTREAM_MAX_IDLE_CONNS"
	envUpstreamMaxIdleConnsPerHost = "TOKENMP_UPSTREAM_MAX_IDLE_CONNS_PER_HOST"
	envUpstreamIdleConnTimeout     = "TOKENMP_UPSTREAM_IDLE_CONN_TIMEOUT"

	// 上游凭据轮换。命中 G101 是因为常量名里有 CREDENTIAL，取值只是环境变量名。
	envCredentialCooldown = "TOKENMP_UPSTREAM_CREDENTIAL_COOLDOWN" //nolint:gosec // G101：环境变量名，不是凭据。

	// 订阅型上游的 OAuth 惰性续期。
	envOAuthRefreshWindow  = "TOKENMP_UPSTREAM_OAUTH_REFRESH_WINDOW"
	envOAuthRefreshTimeout = "TOKENMP_UPSTREAM_OAUTH_REFRESH_TIMEOUT"

	// 流水落库。
	envUsageWriteTimeout = "TOKENMP_USAGE_WRITE_TIMEOUT"

	// 渠道限流：等待令牌的最长时间。
	envRateLimitWait = "TOKENMP_UPSTREAM_RATE_LIMIT_WAIT"

	// 渠道熔断：连续失败阈值、冷却期与半开探测并发。
	envBreakerThreshold = "TOKENMP_UPSTREAM_BREAKER_THRESHOLD"
	envBreakerCooldown  = "TOKENMP_UPSTREAM_BREAKER_COOLDOWN"
	envBreakerProbes    = "TOKENMP_UPSTREAM_BREAKER_PROBE_CONCURRENCY"

	// 上游套餐探针的采集周期。
	envProbeInterval = "TOKENMP_UPSTREAM_PROBE_INTERVAL"

	// 网关中间件插件：逗号分隔的文件或包目录列表，空值表示禁用插件层。
	envPluginFiles = "TOKENMP_PLUGIN_FILES"

	// 页面注册入口开关与反代信任：见 Serve 对应字段注释。
	envWebSignupEnabled = "TOKENMP_WEB_SIGNUP_ENABLED"
	envWebTrustProxy    = "TOKENMP_WEB_TRUST_PROXY"

	// 邮件通道：验证码（重置密码、注销）的投递；Addr 为空视为未配置。
	envSMTPAddr     = "TOKENMP_SMTP_ADDR"
	envSMTPFrom     = "TOKENMP_SMTP_FROM"
	envSMTPUser     = "TOKENMP_SMTP_USER"
	envSMTPPassword = "TOKENMP_SMTP_PASSWORD" //nolint:gosec // G101：这是环境变量名，不是凭据值
)

// defaultListen 是未配置监听地址时的默认值。
//
// 故意给一个具体取值而不是空串：空串在 net.Listen 里等价于绑全部接口的随机端口，
// 那既不能作为服务地址被访问，也掩盖了「没配监听」这个事实。
const defaultListen = ":8080"

// serve 子命令未显式配置时的默认值。
const (
	defaultCompleteTimeout             = 120 * time.Second
	defaultStreamFirstByteTimeout      = 30 * time.Second
	defaultStreamIdleTimeout           = 60 * time.Second
	defaultUpstreamMaxIdleConns        = 100
	defaultUpstreamMaxIdleConnsPerHost = 32
	defaultUpstreamIdleConnTimeout     = 90 * time.Second
	defaultUsageWriteTimeout           = 5 * time.Second
	// defaultRateLimitWait 是进入渠道前等待令牌的最长时间；
	// 与 internal/ratelimit 的兜底值一致，配置层不导入那个包。
	defaultRateLimitWait = 2 * time.Second
	// defaultCredentialCooldown 与 internal/credential 的兜底值一致：
	// 配置层不导入那个包（它依赖 domain 与存储事实），故两处各写一份常量。
	defaultCredentialCooldown = 60 * time.Second
	// 订阅型凭据的提前续期窗口与单次续期上限，与 internal/credential 的兜底值一致。
	defaultOAuthRefreshWindow  = 5 * time.Minute
	defaultOAuthRefreshTimeout = 15 * time.Second
	// 熔断的默认阈值与冷却期与 internal/circuit 的兜底值一致：
	// 配置层不导入那个包，故两处各写一份常量，取值必须一致。
	defaultBreakerThreshold = 5
	defaultBreakerCooldown  = 30 * time.Second
	// defaultBreakerProbes 是半开态同时放行的探测条数默认值，与 internal/circuit 一致。
	defaultBreakerProbes = 1
	// defaultProbeInterval 是上游套餐探针的默认采集周期。
	// 快照新鲜度按它的两倍判定，路由与采集器都不再各写一份取值。
	defaultProbeInterval = 5 * time.Minute
)

// Serve 是 serve 子命令的运行配置。
//
// 监听地址、存储连接与上游调用参数放在一起：都是进程启动的必需事实，
// 缺失或非法都应在启动前统一报出。
type Serve struct {
	// Listen 是网关监听地址，形如 host:port 或 :port。
	Listen string
	// Store 是存储层连接配置。
	Store store.Config
	// CompleteTimeout 是非流式转发的整段超时（handler 侧整体 deadline）。
	// 流式不适用：长生成的耗时不可预估，用首字节与空闲读两个分级超时代替。
	CompleteTimeout time.Duration
	// StreamFirstByteTimeout 是流式转发等待上游第一个字节的最长时间。
	StreamFirstByteTimeout time.Duration
	// StreamIdleTimeout 是流式转发中两个字节/帧之间的最大间隔。
	StreamIdleTimeout time.Duration
	// UpstreamMaxIdleConns、UpstreamMaxIdleConnsPerHost 与 UpstreamIdleConnTimeout
	// 是上游 HTTP 连接池的容量与空闲回收参数。
	UpstreamMaxIdleConns        int
	UpstreamMaxIdleConnsPerHost int
	UpstreamIdleConnTimeout     time.Duration
	// UsageWriteTimeout 是写用量流水的耗时上限。
	UsageWriteTimeout time.Duration
	// CredentialCooldown 是上游凭据遭遇凭据类失败后的冷却时长；冷却期内该凭据被跳过。
	CredentialCooldown time.Duration
	// OAuthRefreshWindow 是订阅型凭据的提前续期窗口：访问令牌剩余有效期不足该值时先续期。
	OAuthRefreshWindow time.Duration
	// OAuthRefreshTimeout 是单次 OAuth 续期与等待续期的上限。
	OAuthRefreshTimeout time.Duration
	// RateLimitWait 是渠道限流下等待令牌的最长时间；超时按可重试的上游失败换下一条候选。
	RateLimitWait time.Duration
	// BreakerThreshold 是渠道连续失败多少次后熔断打开。
	BreakerThreshold int
	// BreakerCooldown 是渠道熔断打开后多久允许一笔探测。
	BreakerCooldown time.Duration
	// BreakerProbes 是半开态同时放行的探测条数。
	BreakerProbes int
	// ProbeInterval 是上游套餐探针的采集周期；快照超过它的两倍视为未知。
	ProbeInterval time.Duration
	// PluginFiles 是网关中间件插件的文件或包目录列表；空表示禁用插件层。
	// 列表内每一项的合法性（存在性、扩展名、能否编译）在插件层加载时校验，
	// 非法项在启动期报错而不是拖到第一个请求。
	PluginFiles []string
	// WebSignupEnabled 是页面注册入口开关；关闭时 signup 回 403，默认开。
	WebSignupEnabled bool
	// WebTrustProxy 为真时页面认证按 X-Forwarded-For 首段做频率限制；
	// 默认关：直连暴露时该头可被客户端伪造，仅在可信反代之后打开。
	WebTrustProxy bool
	// SMTPAddr 是邮件服务器地址（host:port）；空表示不配置邮件通道，
	// 验证码发送入口回 500。其余 SMTP 字段仅在 Addr 非空时有意义。
	SMTPAddr     string
	SMTPFrom     string
	SMTPUser     string
	SMTPPassword string
}

// Load 从进程环境读出存储层配置。
func Load() (store.Config, error) {
	return load(os.LookupEnv)
}

// LoadServe 从进程环境读出 serve 子命令的运行配置。
func LoadServe() (Serve, error) {
	return loadServe(os.LookupEnv)
}

// LoadPluginFiles 从进程环境读出网关中间件插件列表。
//
// 单独提供一个入口：管理面的 `plugin list` 只读这项配置，不应为了列插件而
// 要求 MySQL 连接配置已就绪。
func LoadPluginFiles() []string {
	return lookupPluginFiles(os.LookupEnv)
}

// loadServe 在存储配置之上补出监听、超时分级与连接池参数；缺失项用默认值。
func loadServe(lookup func(string) (string, bool)) (Serve, error) {
	storeCfg, err := load(lookup)
	if err != nil {
		return Serve{}, err
	}
	listen := defaultListen
	if raw, ok := lookup(envListen); ok && strings.TrimSpace(raw) != "" {
		listen = strings.TrimSpace(raw)
	}
	complete, err := lookupPositiveDuration(lookup, envCompleteTimeout, defaultCompleteTimeout)
	if err != nil {
		return Serve{}, err
	}
	firstByte, err := lookupPositiveDuration(lookup, envStreamFirstByteTimeout, defaultStreamFirstByteTimeout)
	if err != nil {
		return Serve{}, err
	}
	idle, err := lookupPositiveDuration(lookup, envStreamIdleTimeout, defaultStreamIdleTimeout)
	if err != nil {
		return Serve{}, err
	}
	maxIdle, err := lookupPositiveInt(lookup, envUpstreamMaxIdleConns, defaultUpstreamMaxIdleConns)
	if err != nil {
		return Serve{}, err
	}
	maxIdlePerHost, err := lookupPositiveInt(lookup, envUpstreamMaxIdleConnsPerHost, defaultUpstreamMaxIdleConnsPerHost)
	if err != nil {
		return Serve{}, err
	}
	idleConnTimeout, err := lookupPositiveDuration(lookup, envUpstreamIdleConnTimeout, defaultUpstreamIdleConnTimeout)
	if err != nil {
		return Serve{}, err
	}
	usageWrite, err := lookupPositiveDuration(lookup, envUsageWriteTimeout, defaultUsageWriteTimeout)
	if err != nil {
		return Serve{}, err
	}
	credentialCooldown, err := lookupPositiveDuration(lookup, envCredentialCooldown, defaultCredentialCooldown)
	if err != nil {
		return Serve{}, err
	}
	oauthRefreshWindow, err := lookupPositiveDuration(lookup, envOAuthRefreshWindow, defaultOAuthRefreshWindow)
	if err != nil {
		return Serve{}, err
	}
	oauthRefreshTimeout, err := lookupPositiveDuration(lookup, envOAuthRefreshTimeout, defaultOAuthRefreshTimeout)
	if err != nil {
		return Serve{}, err
	}
	rateLimitWait, err := lookupPositiveDuration(lookup, envRateLimitWait, defaultRateLimitWait)
	if err != nil {
		return Serve{}, err
	}
	breakerThreshold, err := lookupPositiveInt(lookup, envBreakerThreshold, defaultBreakerThreshold)
	if err != nil {
		return Serve{}, err
	}
	breakerCooldown, err := lookupPositiveDuration(lookup, envBreakerCooldown, defaultBreakerCooldown)
	if err != nil {
		return Serve{}, err
	}
	breakerProbes, err := lookupPositiveInt(lookup, envBreakerProbes, defaultBreakerProbes)
	if err != nil {
		return Serve{}, err
	}
	probeInterval, err := lookupPositiveDuration(lookup, envProbeInterval, defaultProbeInterval)
	if err != nil {
		return Serve{}, err
	}
	// 注册默认开、反代信任默认关：两个默认值都取「不配就安全/可用」的一侧。
	webSignupEnabled, err := lookupBool(lookup, envWebSignupEnabled, true)
	if err != nil {
		return Serve{}, err
	}
	webTrustProxy, err := lookupBool(lookup, envWebTrustProxy, false)
	if err != nil {
		return Serve{}, err
	}
	smtpAddr, _ := lookup(envSMTPAddr)
	smtpFrom, _ := lookup(envSMTPFrom)
	smtpUser, _ := lookup(envSMTPUser)
	smtpPassword, _ := lookup(envSMTPPassword)
	return Serve{
		Listen:                      listen,
		Store:                       storeCfg,
		CompleteTimeout:             complete,
		StreamFirstByteTimeout:      firstByte,
		StreamIdleTimeout:           idle,
		UpstreamMaxIdleConns:        maxIdle,
		UpstreamMaxIdleConnsPerHost: maxIdlePerHost,
		UpstreamIdleConnTimeout:     idleConnTimeout,
		UsageWriteTimeout:           usageWrite,
		CredentialCooldown:          credentialCooldown,
		OAuthRefreshWindow:          oauthRefreshWindow,
		OAuthRefreshTimeout:         oauthRefreshTimeout,
		RateLimitWait:               rateLimitWait,
		BreakerThreshold:            breakerThreshold,
		BreakerCooldown:             breakerCooldown,
		BreakerProbes:               breakerProbes,
		ProbeInterval:               probeInterval,
		PluginFiles:                 lookupPluginFiles(lookup),
		WebSignupEnabled:            webSignupEnabled,
		WebTrustProxy:               webTrustProxy,
		SMTPAddr:                    strings.TrimSpace(smtpAddr),
		SMTPFrom:                    strings.TrimSpace(smtpFrom),
		SMTPUser:                    strings.TrimSpace(smtpUser),
		SMTPPassword:                smtpPassword,
	}, nil
}

// lookupBool 读取布尔环境变量；缺失或空值取 fallback，其余取值必须可辨识。
//
// 拼错的值静默回落默认值，会让「明明配了却不生效」变成难排查的问题，
// 因此无法识别的取值直接报错，与其它环境变量「非法即退出」的口径一致。
func lookupBool(lookup func(string) (string, bool), key string, fallback bool) (bool, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s 取值 %q 无法识别：只接受 true/false（及 1/0、yes/no、on/off）", key, raw)
	}
}

// lookupPluginFiles 读取逗号分隔的插件文件列表；缺失、空值或全空白项返回 nil，
// 表示禁用插件层。列表项只做去空白，存在性与扩展名由插件层校验。
func lookupPluginFiles(lookup func(string) (string, bool)) []string {
	raw, ok := lookup(envPluginFiles)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	fields := strings.Split(raw, ",")
	files := make([]string, 0, len(fields))
	for _, field := range fields {
		trimmed := strings.TrimSpace(field)
		if trimmed != "" {
			files = append(files, trimmed)
		}
	}
	if len(files) == 0 {
		return nil
	}
	return files
}

// load 接受一个查找函数而不是直接读 os，便于测试构造各种环境。
//
// 缺失的数值项用 store 的默认值（留零即可），只有 DSN 缺失与数值非法返回错误：
// 缺失与非法是两回事 —— 前者有合理默认，后者说明配置写错了，静默兜底会掩盖错误。
func load(lookup func(string) (string, bool)) (store.Config, error) {
	cfg := store.Config{}

	dsn, ok := lookup(envDSN)
	if !ok || strings.TrimSpace(dsn) == "" {
		return store.Config{}, fmt.Errorf("配置缺失：环境变量 %s 未设置", envDSN)
	}
	cfg.DSN = dsn

	maxOpen, err := lookupInt(lookup, envMaxOpenConns)
	if err != nil {
		return store.Config{}, err
	}
	cfg.MaxOpenConns = maxOpen

	maxIdle, err := lookupInt(lookup, envMaxIdleConns)
	if err != nil {
		return store.Config{}, err
	}
	cfg.MaxIdleConns = maxIdle

	lifetime, err := lookupDuration(lookup, envConnMaxLifetime)
	if err != nil {
		return store.Config{}, err
	}
	cfg.ConnMaxLifetime = lifetime

	// 复用 store 的校验，保证环境变量入口与直接构造走同一套规则。
	if err := cfg.Validate(); err != nil {
		return store.Config{}, fmt.Errorf("配置非法：%w", err)
	}
	return cfg, nil
}

// lookupInt 读取一个整数字段，缺失或为空返回零值（由上层填默认）。
func lookupInt(lookup func(string) (string, bool), key string) (int, error) {
	raw, ok := lookup(key)
	if !ok || raw == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("配置非法：环境变量 %s=%q 不是整数", key, raw)
	}
	return v, nil
}

// lookupDuration 读取一个时长字段，格式遵循 time.ParseDuration（如 5m、30s）。
func lookupDuration(lookup func(string) (string, bool), key string) (time.Duration, error) {
	raw, ok := lookup(key)
	if !ok || raw == "" {
		return 0, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("配置非法：环境变量 %s=%q 不是合法时长", key, raw)
	}
	return v, nil
}

// lookupPositiveDuration 读取一个必须为正的时长字段；缺失或为空时返回默认值。
//
// 显式写成非正数是配置错误：超时为零或负数会让调用方立即超时或以「无超时」运行，
// 两者都与「没配」不同，静默兵底会掩盖错误，故直接报错。
func lookupPositiveDuration(lookup func(string) (string, bool), key string, def time.Duration) (time.Duration, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def, nil
	}
	v, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("配置非法：环境变量 %s=%q 不是合法时长", key, raw)
	}
	if v <= 0 {
		return 0, fmt.Errorf("配置非法：环境变量 %s=%q 必须为正时长", key, raw)
	}
	return v, nil
}

// lookupPositiveInt 读取一个必须为正的整数字段；缺失或为空时返回默认值。
func lookupPositiveInt(lookup func(string) (string, bool), key string, def int) (int, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def, nil
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("配置非法：环境变量 %s=%q 不是整数", key, raw)
	}
	if v <= 0 {
		return 0, fmt.Errorf("配置非法：环境变量 %s=%q 必须为正整数", key, raw)
	}
	return v, nil
}
