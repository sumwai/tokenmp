package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// 本文件覆盖验证码守护的三个动作：重置密码、修改密码、注销账号，
// 以及发送入口的两条防枚举口径（mailer 未配置一律 500、配置后一律 200）。

// fakeMailer 记录投递内容，供测试从正文提取验证码。
type fakeMailer struct {
	mu   sync.Mutex
	sent []sentMail
}

type sentMail struct {
	to      string
	subject string
	body    string
}

func (f *fakeMailer) Send(_ context.Context, to, subject, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMail{to: to, subject: subject, body: body})
	return nil
}

// lastOTP 取最近一封邮件里的 6 位验证码。
func (f *fakeMailer) lastOTP(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		t.Fatal("没有投递任何邮件")
	}
	m := regexp.MustCompile(`\b\d{6}\b`).FindString(f.sent[len(f.sent)-1].body)
	if m == "" {
		t.Fatalf("邮件正文里没有验证码: %q", f.sent[len(f.sent)-1].body)
	}
	return m
}

// newOTPEnv 构造带假邮件通道的测试环境。
func newOTPEnv(t *testing.T) (*testEnv, *fakeMailer) {
	t.Helper()
	mailer := &fakeMailer{}
	return newTestEnv(t, Options{Mailer: mailer}), mailer
}

// requestOtp 经 HTTP 端点请求一枚验证码并返回其中的码。
func requestOtp(t *testing.T, e *testEnv, mailer *fakeMailer, purpose string) string {
	t.Helper()
	status, env := e.do(t, http.MethodPost, PathOTP, map[string]string{
		"email":   "user@example.com",
		"purpose": purpose,
	}, "")
	if status != http.StatusOK || codeOf(t, env) != codeOK {
		t.Fatalf("发送验证码失败: %d %s", status, env)
	}
	return mailer.lastOTP(t)
}

// TestSendOtpWithoutMailer 断言邮件通道未配置时发送入口一律 500，
// 且该判定先于任何账号查询 —— 不存在与存在的邮箱得到相同响应。
func TestSendOtpWithoutMailer(t *testing.T) {
	e := newTestEnv(t, Options{}) // 无 Mailer
	for _, email := range []string{"exists@example.com", "ghost@example.com"} {
		status, env := e.do(t, http.MethodPost, PathOTP, map[string]string{
			"email":   email,
			"purpose": "reset",
		}, "")
		if status != http.StatusInternalServerError || codeOf(t, env) != codeInternal {
			t.Fatalf("%s 应回 500 信封: %d %s", email, status, env)
		}
	}
}

// TestSendOtpAntiEnum 断言配置邮件通道后，存在与不存在的邮箱都回 200，
// 且只有存在的邮箱真的收到信。
func TestSendOtpAntiEnum(t *testing.T) {
	e, mailer := newOTPEnv(t)
	_, _ = e.signup(t, "fp-anti-enum-01") // 先有账号

	for _, email := range []string{"ghost@example.com", "user@example.com"} {
		status, env := e.do(t, http.MethodPost, PathOTP, map[string]string{
			"email":   email,
			"purpose": "reset",
		}, "")
		if status != http.StatusOK || codeOf(t, env) != codeOK {
			t.Fatalf("%s 应回 200: %d %s", email, status, env)
		}
	}
	mailer.mu.Lock()
	defer mailer.mu.Unlock()
	if len(mailer.sent) != 1 || mailer.sent[0].to != "user@example.com" {
		t.Fatalf("应只向存在的邮箱投递一封: %+v", mailer.sent)
	}
}

// TestResetWithOTP 走完「发码 → 重置 → 旧会话失效 → 新密码登录」。
func TestResetWithOTP(t *testing.T) {
	e, mailer := newOTPEnv(t)
	access, _ := e.signup(t, "fp-reset-00001")
	otp := requestOtp(t, e, mailer, "reset")

	pub := e.challengeKey(t, "fp-reset-00002")
	status, env := e.do(t, http.MethodPost, PathReset, map[string]string{
		"email":       "user@example.com",
		"otp":         otp,
		"password":    encryptPassword(t, pub, "brand-new-pass"),
		"fingerprint": "fp-reset-00002",
	}, "")
	if status != http.StatusOK || codeOf(t, env) != codeOK {
		t.Fatalf("重置失败: %d %s", status, env)
	}

	// 重置吊销全部会话。
	status, _ = e.do(t, http.MethodGet, PathSession, nil, access)
	if status != http.StatusUnauthorized {
		t.Fatalf("重置后旧会话应失效: %d", status)
	}

	// 新密码可登录，旧密码被拒。
	pub2 := e.challengeKey(t, "fp-reset-00003")
	status, _ = e.do(t, http.MethodPost, PathSignin, map[string]string{
		"username":    "tester",
		"password":    encryptPassword(t, pub2, "brand-new-pass"),
		"fingerprint": "fp-reset-00003",
	}, "")
	if status != http.StatusOK {
		t.Fatalf("新密码应可登录: %d", status)
	}
	pub3 := e.challengeKey(t, "fp-reset-00004")
	status, _ = e.do(t, http.MethodPost, PathSignin, map[string]string{
		"username":    "tester",
		"password":    encryptPassword(t, pub3, "s3cret-pass"),
		"fingerprint": "fp-reset-00004",
	}, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("旧密码应被拒: %d", status)
	}
}

// TestResetWrongOTP 断言错误验证码回 400，且与「邮箱不存在」的响应不可区分。
func TestResetWrongOTP(t *testing.T) {
	e, _ := newOTPEnv(t)
	_, _ = e.signup(t, "fp-badotp-0001")

	for i, email := range []string{"user@example.com", "ghost@example.com"} {
		// 每次循环各自 challenge：私钥一次性，共用会在第二轮先回 410。
		fp := fmt.Sprintf("fp-badotp-%03d", i+2)
		pub := e.challengeKey(t, fp)
		status, env := e.do(t, http.MethodPost, PathReset, map[string]string{
			"email":       email,
			"otp":         "000000",
			"password":    encryptPassword(t, pub, "whatever-123"),
			"fingerprint": fp,
		}, "")
		if status != http.StatusBadRequest || codeOf(t, env) != codeBadRequest {
			t.Fatalf("%s 应回 400: %d %s", email, status, env)
		}
		var msg string
		_ = json.Unmarshal(env["message"], &msg)
		if msg != "验证码无效或已过期" {
			t.Fatalf("文案应一致: %q", msg)
		}
	}
}

// TestChangePasswordKeepsCurrentSession 断言改密保留当前会话、
// 吊销其余会话，且新旧密码的登录结果随之翻转。
func TestChangePasswordKeepsCurrentSession(t *testing.T) {
	e := newTestEnv(t, Options{})
	access, _ := e.signup(t, "fp-change-0001")

	// 第二个会话（另一台设备）。
	pubOther := e.challengeKey(t, "fp-change-0002")
	status, env := e.do(t, http.MethodPost, PathSignin, map[string]string{
		"username":    "tester",
		"password":    encryptPassword(t, pubOther, "s3cret-pass"),
		"fingerprint": "fp-change-0002",
	}, "")
	if status != http.StatusOK {
		t.Fatalf("第二会话登录失败: %d %s", status, env)
	}
	var other sessionTokens
	if err := json.Unmarshal(env["data"], &other); err != nil {
		t.Fatalf("解析令牌: %v", err)
	}

	// 当前会话改密：两把密钥各自 challenge。
	pubCurrent := e.challengeKey(t, "fp-change-0003")
	pubNew := e.challengeKey(t, "fp-change-0004")
	status, env = e.do(t, http.MethodPut, PathPassword, map[string]string{
		"current_password":    encryptPassword(t, pubCurrent, "s3cret-pass"),
		"current_fingerprint": "fp-change-0003",
		"new_password":        encryptPassword(t, pubNew, "brand-new-pass"),
		"new_fingerprint":     "fp-change-0004",
	}, access)
	if status != http.StatusOK || codeOf(t, env) != codeOK {
		t.Fatalf("改密失败: %d %s", status, env)
	}

	// 当前会话保留。
	status, _ = e.do(t, http.MethodGet, PathSession, nil, access)
	if status != http.StatusOK {
		t.Fatalf("当前会话应保留: %d", status)
	}
	// 其他会话吊销。
	status, _ = e.do(t, http.MethodGet, PathSession, nil, other.AccessToken)
	if status != http.StatusUnauthorized {
		t.Fatalf("其余会话应被吊销: %d", status)
	}
}

// TestChangePasswordWrongCurrent 断言当前密码错误回 401。
func TestChangePasswordWrongCurrent(t *testing.T) {
	e := newTestEnv(t, Options{})
	access, _ := e.signup(t, "fp-wrongcur-01")

	pubCurrent := e.challengeKey(t, "fp-wrongcur-02")
	pubNew := e.challengeKey(t, "fp-wrongcur-03")
	status, env := e.do(t, http.MethodPut, PathPassword, map[string]string{
		"current_password":    encryptPassword(t, pubCurrent, "not-the-pass"),
		"current_fingerprint": "fp-wrongcur-02",
		"new_password":        encryptPassword(t, pubNew, "brand-new-pass"),
		"new_fingerprint":     "fp-wrongcur-03",
	}, access)
	if status != http.StatusUnauthorized || codeOf(t, env) != codeUnauthorized {
		t.Fatalf("当前密码错误应回 401: %d %s", status, env)
	}
}

// TestEraseAccount 走完「发码（erase）→ 注销 → 无法登录」。
func TestEraseAccount(t *testing.T) {
	e, mailer := newOTPEnv(t)
	access, _ := e.signup(t, "fp-erase-00001")
	otp := requestOtp(t, e, mailer, "erase")

	status, env := e.do(t, http.MethodPost, PathErase, map[string]string{"otp": otp}, access)
	if status != http.StatusOK || codeOf(t, env) != codeOK {
		t.Fatalf("注销失败: %d %s", status, env)
	}

	// 会话失效、无法再登录。
	status, _ = e.do(t, http.MethodGet, PathSession, nil, access)
	if status != http.StatusUnauthorized {
		t.Fatalf("注销后会话应失效: %d", status)
	}
	pub := e.challengeKey(t, "fp-erase-00002")
	status, _ = e.do(t, http.MethodPost, PathSignin, map[string]string{
		"username":    "tester",
		"password":    encryptPassword(t, pub, "s3cret-pass"),
		"fingerprint": "fp-erase-00002",
	}, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("注销后登录应被拒: %d", status)
	}
}

// TestOTPPurposeIsolated 断言 reset 用途的验证码不能用于 erase。
func TestOTPPurposeIsolated(t *testing.T) {
	e, mailer := newOTPEnv(t)
	access, _ := e.signup(t, "fp-purpose-001")
	otp := requestOtp(t, e, mailer, "reset")

	status, env := e.do(t, http.MethodPost, PathErase, map[string]string{"otp": otp}, access)
	if status != http.StatusBadRequest || codeOf(t, env) != codeBadRequest {
		t.Fatalf("跨用途验证码应回 400: %d %s", status, env)
	}
	if !strings.Contains(string(env["message"]), "验证码") {
		t.Fatalf("文案不符: %s", env["message"])
	}
}
