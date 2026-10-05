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

	// 流水落库。
	envUsageWriteTimeout = "TOKENMP_USAGE_WRITE_TIMEOUT"

	// 渠道限流：等待令牌的最长时间。
	envRateLimitWait = "TOKENMP_UPSTREAM_RATE_LIMIT_WAIT"

	// 渠道熔断：连续失败阈值、冷却期与半开探测并发。
	envBreakerThreshold = "TOKENMP_UPSTREAM_BREAKER_THRESHOLD"
	envBreakerCooldown  = "TOKENMP_UPSTREAM_BREAKER_COOLDOWN"
	envBreakerProbes    = "TOKENMP_UPSTREAM_BREAKER_PROBE_CONCURRENCY"
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
	// 熔断的默认阈值与冷却期与 internal/circuit 的兜底值一致：
	// 配置层不导入那个包，故两处各写一份常量，取值必须一致。
	defaultBreakerThreshold = 5
	defaultBreakerCooldown  = 30 * time.Second
	// defaultBreakerProbes 是半开态同时放行的探测条数默认值，与 internal/circuit 一致。
	defaultBreakerProbes = 1
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
	// RateLimitWait 是渠道限流下等待令牌的最长时间；超时按可重试的上游失败换下一条候选。
	RateLimitWait time.Duration
	// BreakerThreshold 是渠道连续失败多少次后熔断打开。
	BreakerThreshold int
	// BreakerCooldown 是渠道熔断打开后多久允许一笔探测。
	BreakerCooldown time.Duration
	// BreakerProbes 是半开态同时放行的探测条数。
	BreakerProbes int
}

// Load 从进程环境读出存储层配置。
func Load() (store.Config, error) {
	return load(os.LookupEnv)
}

// LoadServe 从进程环境读出 serve 子命令的运行配置。
func LoadServe() (Serve, error) {
	return loadServe(os.LookupEnv)
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
		RateLimitWait:               rateLimitWait,
		BreakerThreshold:            breakerThreshold,
		BreakerCooldown:             breakerCooldown,
		BreakerProbes:               breakerProbes,
	}, nil
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
