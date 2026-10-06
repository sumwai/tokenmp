package config

import (
	"testing"
	"time"
)

// fakeEnv 用 map 构造查找函数，测试覆盖各种环境，不碰真实进程环境。
func fakeEnv(kv map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := kv[key]
		return v, ok
	}
}

func TestLoadServe(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		wantErr    bool
		wantListen string
	}{
		{
			name:       "未配监听地址时用默认值",
			env:        map[string]string{envDSN: "dsn"},
			wantListen: defaultListen,
		},
		{
			name:       "显式监听地址原样生效",
			env:        map[string]string{envDSN: "dsn", envListen: " 127.0.0.1:9000 "},
			wantListen: "127.0.0.1:9000",
		},
		{
			name:    "缺 DSN 时连带报错",
			env:     map[string]string{envListen: ":9000"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadServe(fakeEnv(tt.env))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际通过，结果为 %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if got.Listen != tt.wantListen {
				t.Errorf("Listen = %q，期望 %q", got.Listen, tt.wantListen)
			}
		})
	}
}

// TestLoadServeDefaults 断言未配置超时与连接池时全部取建议默认值。
func TestLoadServeDefaults(t *testing.T) {
	got, err := loadServe(fakeEnv(map[string]string{envDSN: "dsn"}))
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if got.CompleteTimeout != defaultCompleteTimeout {
		t.Errorf("CompleteTimeout = %s，期望 %s", got.CompleteTimeout, defaultCompleteTimeout)
	}
	if got.StreamFirstByteTimeout != defaultStreamFirstByteTimeout {
		t.Errorf("StreamFirstByteTimeout = %s，期望 %s", got.StreamFirstByteTimeout, defaultStreamFirstByteTimeout)
	}
	if got.StreamIdleTimeout != defaultStreamIdleTimeout {
		t.Errorf("StreamIdleTimeout = %s，期望 %s", got.StreamIdleTimeout, defaultStreamIdleTimeout)
	}
	if got.UpstreamMaxIdleConns != defaultUpstreamMaxIdleConns {
		t.Errorf("UpstreamMaxIdleConns = %d，期望 %d", got.UpstreamMaxIdleConns, defaultUpstreamMaxIdleConns)
	}
	if got.UpstreamMaxIdleConnsPerHost != defaultUpstreamMaxIdleConnsPerHost {
		t.Errorf("UpstreamMaxIdleConnsPerHost = %d，期望 %d", got.UpstreamMaxIdleConnsPerHost, defaultUpstreamMaxIdleConnsPerHost)
	}
	if got.UpstreamIdleConnTimeout != defaultUpstreamIdleConnTimeout {
		t.Errorf("UpstreamIdleConnTimeout = %s，期望 %s", got.UpstreamIdleConnTimeout, defaultUpstreamIdleConnTimeout)
	}
	if got.UsageWriteTimeout != defaultUsageWriteTimeout {
		t.Errorf("UsageWriteTimeout = %s，期望 %s", got.UsageWriteTimeout, defaultUsageWriteTimeout)
	}
	if got.CredentialCooldown != defaultCredentialCooldown {
		t.Errorf("CredentialCooldown = %s，期望 %s", got.CredentialCooldown, defaultCredentialCooldown)
	}
	if got.RateLimitWait != defaultRateLimitWait {
		t.Errorf("RateLimitWait = %s，期望 %s", got.RateLimitWait, defaultRateLimitWait)
	}
	if got.BreakerThreshold != defaultBreakerThreshold {
		t.Errorf("BreakerThreshold = %d，期望 %d", got.BreakerThreshold, defaultBreakerThreshold)
	}
	if got.BreakerCooldown != defaultBreakerCooldown {
		t.Errorf("BreakerCooldown = %s，期望 %s", got.BreakerCooldown, defaultBreakerCooldown)
	}
	if got.BreakerProbes != defaultBreakerProbes {
		t.Errorf("BreakerProbes = %d，期望 %d", got.BreakerProbes, defaultBreakerProbes)
	}
	if got.ProbeInterval != defaultProbeInterval {
		t.Errorf("ProbeInterval = %s，期望 %s", got.ProbeInterval, defaultProbeInterval)
	}
}

// TestLoadServeExplicitTimeoutsAndPool 断言显式给出的超时与连接池取值原样生效。
func TestLoadServeExplicitTimeoutsAndPool(t *testing.T) {
	got, err := loadServe(fakeEnv(map[string]string{
		envDSN:                         "dsn",
		envCompleteTimeout:             "90s",
		envStreamFirstByteTimeout:      "10s",
		envStreamIdleTimeout:           "20s",
		envUpstreamMaxIdleConns:        "50",
		envUpstreamMaxIdleConnsPerHost: "7",
		envUpstreamIdleConnTimeout:     "45s",
		envUsageWriteTimeout:           "3s",
		envCredentialCooldown:          "45s",
		envRateLimitWait:               "750ms",
		envBreakerThreshold:            "9",
		envBreakerCooldown:             "1m",
		envBreakerProbes:               "3",
		envProbeInterval:               "30s",
	}))
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if got.CompleteTimeout != 90*time.Second {
		t.Errorf("CompleteTimeout = %s，期望 90s", got.CompleteTimeout)
	}
	if got.StreamFirstByteTimeout != 10*time.Second {
		t.Errorf("StreamFirstByteTimeout = %s，期望 10s", got.StreamFirstByteTimeout)
	}
	if got.StreamIdleTimeout != 20*time.Second {
		t.Errorf("StreamIdleTimeout = %s，期望 20s", got.StreamIdleTimeout)
	}
	if got.UpstreamMaxIdleConns != 50 || got.UpstreamMaxIdleConnsPerHost != 7 {
		t.Errorf("连接池 = %d/%d，期望 50/7", got.UpstreamMaxIdleConns, got.UpstreamMaxIdleConnsPerHost)
	}
	if got.UpstreamIdleConnTimeout != 45*time.Second {
		t.Errorf("UpstreamIdleConnTimeout = %s，期望 45s", got.UpstreamIdleConnTimeout)
	}
	if got.UsageWriteTimeout != 3*time.Second {
		t.Errorf("UsageWriteTimeout = %s，期望 3s", got.UsageWriteTimeout)
	}
	if got.CredentialCooldown != 45*time.Second {
		t.Errorf("CredentialCooldown = %s，期望 45s", got.CredentialCooldown)
	}
	if got.RateLimitWait != 750*time.Millisecond {
		t.Errorf("RateLimitWait = %s，期望 750ms", got.RateLimitWait)
	}
	if got.BreakerThreshold != 9 {
		t.Errorf("BreakerThreshold = %d，期望 9", got.BreakerThreshold)
	}
	if got.BreakerCooldown != time.Minute {
		t.Errorf("BreakerCooldown = %s，期望 1m", got.BreakerCooldown)
	}
	if got.BreakerProbes != 3 {
		t.Errorf("BreakerProbes = %d，期望 3", got.BreakerProbes)
	}
	if got.ProbeInterval != 30*time.Second {
		t.Errorf("ProbeInterval = %s，期望 30s", got.ProbeInterval)
	}
}

// TestLoadServeRejectsInvalidTimeoutsAndPool 断言非正数与非法时长在启动前报错。
func TestLoadServeRejectsInvalidTimeoutsAndPool(t *testing.T) {
	tests := []struct {
		name string
		key  string
		val  string
	}{
		{name: "整体超时为零", key: envCompleteTimeout, val: "0s"},
		{name: "整体超时为负", key: envCompleteTimeout, val: "-1s"},
		{name: "整体超时格式错", key: envCompleteTimeout, val: "2 minutes"},
		{name: "首字节超时为零", key: envStreamFirstByteTimeout, val: "0s"},
		{name: "空闲读超时为零", key: envStreamIdleTimeout, val: "0s"},
		{name: "空闲连接数为零", key: envUpstreamMaxIdleConns, val: "0"},
		{name: "单主机空闲连接数为负", key: envUpstreamMaxIdleConnsPerHost, val: "-2"},
		{name: "空闲回收时长为零", key: envUpstreamIdleConnTimeout, val: "0s"},
		{name: "落库超时格式错", key: envUsageWriteTimeout, val: "5"},
		{name: "凭据冷却为零", key: envCredentialCooldown, val: "0s"},
		{name: "凭据冷却格式错", key: envCredentialCooldown, val: "1 minute"},
		{name: "限流等待为零", key: envRateLimitWait, val: "0s"},
		{name: "限流等待为负", key: envRateLimitWait, val: "-1s"},
		{name: "限流等待格式错", key: envRateLimitWait, val: "2"},
		{name: "熔断阈值为零", key: envBreakerThreshold, val: "0"},
		{name: "熔断阈值为负", key: envBreakerThreshold, val: "-1"},
		{name: "探针周期为零", key: envProbeInterval, val: "0s"},
		{name: "探针周期格式错", key: envProbeInterval, val: "5"},
		{name: "熔断阈值格式错", key: envBreakerThreshold, val: "five"},
		{name: "熔断冷却为零", key: envBreakerCooldown, val: "0s"},
		{name: "熔断冷却格式错", key: envBreakerCooldown, val: "30"},
		{name: "熔断探测并发为零", key: envBreakerProbes, val: "0"},
		{name: "熔断探测并发为负", key: envBreakerProbes, val: "-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadServe(fakeEnv(map[string]string{envDSN: "dsn", tt.key: tt.val}))
			if err == nil {
				t.Fatalf("%s=%q 应当被拒绝", tt.key, tt.val)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name             string
		env              map[string]string
		wantErr          bool
		wantMaxOpen      int
		wantMaxIdle      int
		wantConnLifetime time.Duration
	}{
		{
			name:             "只给 DSN 时数值项留空用默认",
			env:              map[string]string{envDSN: "user:pass@tcp(localhost:3306)/tokenmp"},
			wantMaxOpen:      0,
			wantMaxIdle:      0,
			wantConnLifetime: 0,
		},
		{
			name: "全部显式给出",
			env: map[string]string{
				envDSN:             "dsn",
				envMaxOpenConns:    "30",
				envMaxIdleConns:    "8",
				envConnMaxLifetime: "90s",
			},
			wantMaxOpen:      30,
			wantMaxIdle:      8,
			wantConnLifetime: 90 * time.Second,
		},
		{
			name:    "缺少 DSN 报错",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name:    "DSN 为空白报错",
			env:     map[string]string{envDSN: "  "},
			wantErr: true,
		},
		{
			name:    "连接数不是整数报错",
			env:     map[string]string{envDSN: "dsn", envMaxOpenConns: "many"},
			wantErr: true,
		},
		{
			name:    "时长格式非法报错",
			env:     map[string]string{envDSN: "dsn", envConnMaxLifetime: "5 minutes"},
			wantErr: true,
		},
		{
			name:    "负数连接数报错",
			env:     map[string]string{envDSN: "dsn", envMaxOpenConns: "-3"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := load(fakeEnv(tt.env))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际通过，结果为 %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if got.MaxOpenConns != tt.wantMaxOpen {
				t.Errorf("MaxOpenConns = %d，期望 %d", got.MaxOpenConns, tt.wantMaxOpen)
			}
			if got.MaxIdleConns != tt.wantMaxIdle {
				t.Errorf("MaxIdleConns = %d，期望 %d", got.MaxIdleConns, tt.wantMaxIdle)
			}
			if got.ConnMaxLifetime != tt.wantConnLifetime {
				t.Errorf("ConnMaxLifetime = %s，期望 %s", got.ConnMaxLifetime, tt.wantConnLifetime)
			}
		})
	}
}
