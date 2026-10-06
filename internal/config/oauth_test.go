package config

import (
	"testing"
	"time"
)

// 本文件覆盖订阅型上游 OAuth 续期的两个配置项：
// 默认提前续期窗口与单次续期上限、显式取值，以及非正数与非法时长的拒绝。

// TestLoadServeOAuthRefreshDefaults 断言未配置时取 5 分钟窗口与 15 秒上限。
func TestLoadServeOAuthRefreshDefaults(t *testing.T) {
	got, err := loadServe(fakeEnv(map[string]string{envDSN: "dsn"}))
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if got.OAuthRefreshWindow != defaultOAuthRefreshWindow {
		t.Errorf("OAuthRefreshWindow = %s，期望 %s", got.OAuthRefreshWindow, defaultOAuthRefreshWindow)
	}
	if got.OAuthRefreshTimeout != defaultOAuthRefreshTimeout {
		t.Errorf("OAuthRefreshTimeout = %s，期望 %s", got.OAuthRefreshTimeout, defaultOAuthRefreshTimeout)
	}
}

// TestLoadServeOAuthRefreshExplicit 断言显式给出的窗口与上限原样生效。
func TestLoadServeOAuthRefreshExplicit(t *testing.T) {
	got, err := loadServe(fakeEnv(map[string]string{
		envDSN:                 "dsn",
		envOAuthRefreshWindow:  "2m",
		envOAuthRefreshTimeout: "8s",
	}))
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if got.OAuthRefreshWindow != 2*time.Minute {
		t.Errorf("OAuthRefreshWindow = %s，期望 2m", got.OAuthRefreshWindow)
	}
	if got.OAuthRefreshTimeout != 8*time.Second {
		t.Errorf("OAuthRefreshTimeout = %s，期望 8s", got.OAuthRefreshTimeout)
	}
}

// TestLoadServeOAuthRefreshRejectsInvalid 断言非正数与非法时长在启动前报错。
func TestLoadServeOAuthRefreshRejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		key  string
		val  string
	}{
		{name: "续期窗口为零", key: envOAuthRefreshWindow, val: "0s"},
		{name: "续期窗口为负", key: envOAuthRefreshWindow, val: "-1s"},
		{name: "续期窗口格式错", key: envOAuthRefreshWindow, val: "5 minutes"},
		{name: "续期上限为零", key: envOAuthRefreshTimeout, val: "0s"},
		{name: "续期上限格式错", key: envOAuthRefreshTimeout, val: "15"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := loadServe(fakeEnv(map[string]string{envDSN: "dsn", tt.key: tt.val})); err == nil {
				t.Fatalf("%s=%q 应当被拒绝", tt.key, tt.val)
			}
		})
	}
}
