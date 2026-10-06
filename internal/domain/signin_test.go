package domain

import (
	"net/http"
	"testing"
)

// signinTestHeader 是用例共用的信标头配置：头名与取值全部来自配置而非硬编码。
func signinTestHeader() SigninHeader {
	return SigninHeader{Name: "x-login-state", Expired: "expired", Kept: "kept", Renewed: "renewed"}
}

// TestSigninHeaderVerdict 覆盖信标头取值到结论的映射口径。
func TestSigninHeaderVerdict(t *testing.T) {
	tests := []struct {
		name   string
		header SigninHeader
		values []string
		want   SigninVerdict
	}{
		{name: "未声明信标头", header: SigninHeader{}, values: []string{"expired"}, want: SigninUnknown},
		{name: "响应未带信标头", header: signinTestHeader(), want: SigninUnknown},
		{name: "expired 命中", header: signinTestHeader(), values: []string{"expired"}, want: SigninExpired},
		{name: "kept 命中", header: signinTestHeader(), values: []string{"kept"}, want: SigninKept},
		{name: "renewed 命中", header: signinTestHeader(), values: []string{"renewed"}, want: SigninRenewed},
		{name: "取值大小写与空白不敏感", header: signinTestHeader(), values: []string{" Expired "}, want: SigninExpired},
		{name: "未登记的取值", header: signinTestHeader(), values: []string{"unknown"}, want: SigninUnknown},
		{name: "空取值", header: signinTestHeader(), values: []string{""}, want: SigninUnknown},
		{
			name:   "取值重复时失效结论优先",
			header: SigninHeader{Name: "x-login-state", Expired: "same", Kept: "same", Renewed: "same"},
			values: []string{"same"},
			want:   SigninExpired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			for _, value := range tt.values {
				header.Set("x-login-state", value)
			}
			if got := tt.header.Verdict(header); got != tt.want {
				t.Fatalf("Verdict = %v，期望 %v", got, tt.want)
			}
		})
	}
}

// TestSigninHeaderConfigured 断言只有声明了头名才算已配置。
func TestSigninHeaderConfigured(t *testing.T) {
	if (SigninHeader{}).Configured() {
		t.Error("零值不应算已配置")
	}
	if (SigninHeader{Name: "   "}).Configured() {
		t.Error("空白头名不应算已配置")
	}
	if !signinTestHeader().Configured() {
		t.Error("声明了头名的配置应算已配置")
	}
}

// TestSigninHeaderStrip 断言信标头在消费后从响应头里剥除。
func TestSigninHeaderStrip(t *testing.T) {
	header := http.Header{}
	header.Set("X-Login-State", "expired")
	header.Set("X-Other", "kept")

	signinTestHeader().Strip(header)

	if got := header.Get("X-Login-State"); got != "" {
		t.Errorf("信标头应被剥除，实际仍为 %q", got)
	}
	if got := header.Get("X-Other"); got != "kept" {
		t.Errorf("其它响应头不应被改动，实际 %q", got)
	}
}

// TestSigninHeaderStripNoop 断言未声明信标头时剥除为空操作。
func TestSigninHeaderStripNoop(t *testing.T) {
	header := http.Header{"X-Login-State": []string{"expired"}}
	(SigninHeader{}).Strip(header)
	if got := header.Get("X-Login-State"); got != "expired" {
		t.Errorf("未声明信标头时不应改动响应头，实际 %q", got)
	}
	// nil 头不得 panic。
	signinTestHeader().Strip(nil)
}
