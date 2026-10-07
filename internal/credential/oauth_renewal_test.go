package credential

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/failure"
	"github.com/sumwai/tokenmp/internal/oauth"
)

// 本文件覆盖订阅型凭据的续期：窗口判定、单飞行、invalid_grant 冷却、
// 非授权失败保留旧令牌，以及两类凭据混排轮换。

// fakeRefresher 是可编排的续期替身，记录调用次数。
type fakeRefresher struct {
	mu    sync.Mutex
	calls int
	fn    func(context.Context, OAuthRefreshInput) (OAuthRefreshOutput, error)
}

func (f *fakeRefresher) Refresh(ctx context.Context, in OAuthRefreshInput) (OAuthRefreshOutput, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(ctx, in)
	}
	return OAuthRefreshOutput{Access: "new-access", Refresh: in.Refresh, Expires: time.Now().Add(time.Hour)}, nil
}

func (f *fakeRefresher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// oauthRenewalRoute 是续期用例共用的路由：声明了可用的 token_url 与 client_id。
func oauthRenewalRoute() domain.Route {
	return domain.Route{
		CredentialRef: "group-a",
		Protocol:      domain.ProtocolOpenAIChat,
		//nolint:gosec // G101：测试用的假端点地址，不是真实凭据。
		OAuthProfile: domain.OAuthProfile{
			TokenURL: "https://oauth.example.com/token",
			ClientID: "client-1",
		},
	}
}

// expiringOAuthGroup 返回只有一条即将过期订阅凭据的分组。
func expiringOAuthGroup(expires time.Time) Group {
	return Group{Scope: "merchant-1", Entries: []NamedCredential{{
		ID:     1,
		Name:   "primary",
		APIKey: "old-access",
		OAuth: &OAuthCredential{
			Access:  "old-access",
			Refresh: "refresh-1",
			Expires: expires,
			Account: "acct-1",
		},
	}}}
}

// newRenewalRotator 构造带续期器的轮换器。
func newRenewalRotator(t *testing.T, group Group, clock *fakeClock, refresher OAuthRefresher) *Rotator {
	t.Helper()
	r, err := NewRotator(RotationOptions{
		Loader:   fakeGroupLoader{group: group},
		Clock:    clock.Now,
		Cooldown: time.Minute,
		Renewal:  &RenewalOptions{Refresher: refresher, Window: 5 * time.Minute},
	})
	if err != nil {
		t.Fatalf("构造轮换器失败：%v", err)
	}
	return r
}

// TestRenewalDefaults 断言未显式配置时的默认值：提前续期窗口 5 分钟、单次续期与等待上限 15 秒。
//
// 这两个默认值是验收口径的一部分：窗口可配置只改取值，不改变「不足窗口即续期」的语义。
func TestRenewalDefaults(t *testing.T) {
	state := newRenewalState(&RenewalOptions{Refresher: &fakeRefresher{}})
	if state == nil {
		t.Fatal("注入刷新器后应构造出续期状态")
	}
	if state.window != 5*time.Minute {
		t.Errorf("默认续期窗口 = %s，期望 5m", state.window)
	}
	if state.timeout != 15*time.Second {
		t.Errorf("默认续期上限 = %s，期望 15s", state.timeout)
	}
	if newRenewalState(nil) != nil {
		t.Error("未注入参数时应退化为不续期")
	}
	if newRenewalState(&RenewalOptions{}) != nil {
		t.Error("缺少刷新器时应退化为不续期")
	}
}

// TestRenewalWindowConfigurable 断言提前续期窗口可配置：同一过期时刻在窄窗口下不续期、
// 在宽窗口下续期。
func TestRenewalWindowConfigurable(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	expires := now.Add(3 * time.Minute)

	narrow := newRenewalState(&RenewalOptions{Refresher: &fakeRefresher{}, Window: time.Minute})
	if narrow.due(expires, now) {
		t.Error("窗口 1m 时剩余 3m 不应续期")
	}

	wide := newRenewalState(&RenewalOptions{Refresher: &fakeRefresher{}, Window: 10 * time.Minute})
	if !wide.due(expires, now) {
		t.Error("窗口 10m 时剩余 3m 应续期")
	}
}

// TestRenewalWindowDue 断言提前续期窗口的边界：剩余有效期不足窗口即续期，
// 零值（有效期未知）不续期。
func TestRenewalWindowDue(t *testing.T) {
	state := newRenewalState(&RenewalOptions{Refresher: &fakeRefresher{}, Window: 5 * time.Minute})
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name    string
		expires time.Time
		want    bool
	}{
		{name: "已过期", expires: now.Add(-time.Minute), want: true},
		{name: "窗口内", expires: now.Add(4 * time.Minute), want: true},
		{name: "恰在窗口边界", expires: now.Add(5 * time.Minute), want: true},
		{name: "窗口外", expires: now.Add(6 * time.Minute), want: false},
		{name: "有效期未知", expires: time.Time{}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := state.due(tt.expires, now); got != tt.want {
				t.Errorf("due(%s) = %t，期望 %t", tt.expires, got, tt.want)
			}
		})
	}
}

// TestRotatorRenewsExpiringCredential 断言取用临近过期的订阅凭据时先续期并用新令牌。
func TestRotatorRenewsExpiringCredential(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	refresher := &fakeRefresher{}
	r := newRenewalRotator(t, expiringOAuthGroup(clock.now.Add(2*time.Minute)), clock, refresher)
	route := oauthRenewalRoute()

	cred := resolveOne(t, r, route)
	if cred.APIKey != "new-access" {
		t.Fatalf("取到 %q，期望续期后的 new-access", cred.APIKey)
	}
	if refresher.callCount() != 1 {
		t.Fatalf("续期调用 %d 次，期望 1", refresher.callCount())
	}
}

// TestRotatorSkipsRenewalAwayFromExpiry 断言离过期还远时不打端点。
func TestRotatorSkipsRenewalAwayFromExpiry(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	refresher := &fakeRefresher{}
	r := newRenewalRotator(t, expiringOAuthGroup(clock.now.Add(time.Hour)), clock, refresher)
	route := oauthRenewalRoute()

	cred := resolveOne(t, r, route)
	if cred.APIKey != "old-access" {
		t.Fatalf("取到 %q，期望原令牌 old-access", cred.APIKey)
	}
	if refresher.callCount() != 0 {
		t.Fatalf("续期调用 %d 次，期望 0", refresher.callCount())
	}
}

// TestRotatorSkipsRenewalWithoutProfile 断言渠道未声明画像时不续期，按原样注入。
func TestRotatorSkipsRenewalWithoutProfile(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	refresher := &fakeRefresher{}
	r := newRenewalRotator(t, expiringOAuthGroup(clock.now.Add(time.Minute)), clock, refresher)
	route := domain.Route{CredentialRef: "group-a", Protocol: domain.ProtocolOpenAIChat}

	cred := resolveOne(t, r, route)
	if cred.APIKey != "old-access" {
		t.Fatalf("取到 %q，期望原令牌 old-access", cred.APIKey)
	}
	if refresher.callCount() != 0 {
		t.Fatalf("无画像时不应续期，实际调用 %d 次", refresher.callCount())
	}
}

// TestRotatorSingleFlightRefresh 断言同一凭据上的并发取用只触发一次刷新。
func TestRotatorSingleFlightRefresh(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	leaderEntered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	refresher := &fakeRefresher{fn: func(_ context.Context, in OAuthRefreshInput) (OAuthRefreshOutput, error) {
		once.Do(func() { close(leaderEntered) })
		<-release
		return OAuthRefreshOutput{Access: "new-access", Refresh: in.Refresh, Expires: clock.now.Add(time.Hour)}, nil
	}}
	state := newRenewalState(&RenewalOptions{Refresher: refresher, Window: 5 * time.Minute})
	in := OAuthRefreshInput{CredentialID: 7, Name: "primary", Refresh: "refresh-1"}

	const callers = 8
	var started, returned sync.WaitGroup
	started.Add(callers)
	returned.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer returned.Done()
			started.Done()
			_, _ = state.refresh(context.Background(), in)
		}()
	}
	started.Wait()
	// 首位调用者已经在刷新中，其余调用者据此进入等待。
	<-leaderEntered
	// 给等待者一点时间进入单飞行表；随后放行刷新。
	time.Sleep(50 * time.Millisecond)
	close(release)
	returned.Wait()

	if got := refresher.callCount(); got != 1 {
		t.Fatalf("并发续期实际刷新 %d 次，期望单飞行为 1 次", got)
	}
}

// TestRotatorInvalidGrantCoolsCredential 断言 invalid_grant 把该凭据标记为冷却并切换下一条。
func TestRotatorInvalidGrantCoolsCredential(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	refresher := &fakeRefresher{fn: func(context.Context, OAuthRefreshInput) (OAuthRefreshOutput, error) {
		return OAuthRefreshOutput{}, oauth.ErrInvalidGrant
	}}
	group := expiringOAuthGroup(clock.now.Add(time.Minute))
	group.Entries = append(group.Entries, NamedCredential{ID: 2, Name: "backup", APIKey: "sk-backup"})
	r := newRenewalRotator(t, group, clock, refresher)
	route := oauthRenewalRoute()

	ctx := r.Begin(context.Background(), route)
	_, err := r.Resolve(ctx, route)
	if err == nil {
		t.Fatal("invalid_grant 应让本次解析失败")
	}
	if !failure.ActionsOf(err).Has(failure.ActionRetryNextAccount) {
		t.Fatalf("invalid_grant 错误应带换凭据动作，得到 %v", err)
	}
	if !errors.Is(err, oauth.ErrInvalidGrant) {
		t.Fatalf("错误应包住 oauth.ErrInvalidGrant，得到 %v", err)
	}
	nextCtx, switched := r.Advance(ctx, route, err)
	if !switched {
		t.Fatal("组内还有可用凭据时应切换")
	}
	cred, resolveErr := r.Resolve(nextCtx, route)
	if resolveErr != nil {
		t.Fatalf("切换后解析失败：%v", resolveErr)
	}
	if cred.APIKey != "sk-backup" {
		t.Fatalf("切换后取到 %q，期望 sk-backup", cred.APIKey)
	}

	// 新的请求里该凭据已被冷却，轮换顺序跳过它，不再触发刷新。
	before := refresher.callCount()
	cred = resolveOne(t, r, route)
	if cred.APIKey != "sk-backup" {
		t.Fatalf("冷却后取到 %q，期望跳过失效凭据取 sk-backup", cred.APIKey)
	}
	if refresher.callCount() != before {
		t.Fatalf("冷却中的凭据不应再次触发续期，实际新增 %d 次", refresher.callCount()-before)
	}
}

// TestRotatorTransientRefreshKeepsOldToken 断言非授权类失败保留旧令牌继续发请求。
func TestRotatorTransientRefreshKeepsOldToken(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	refresher := &fakeRefresher{fn: func(context.Context, OAuthRefreshInput) (OAuthRefreshOutput, error) {
		return OAuthRefreshOutput{}, errors.New("端点 503")
	}}
	r := newRenewalRotator(t, expiringOAuthGroup(clock.now.Add(time.Minute)), clock, refresher)
	route := oauthRenewalRoute()

	cred := resolveOne(t, r, route)
	if cred.APIKey != "old-access" {
		t.Fatalf("取到 %q，期望保留旧令牌 old-access", cred.APIKey)
	}
	if refresher.callCount() != 1 {
		t.Fatalf("续期调用 %d 次，期望 1", refresher.callCount())
	}
}

// TestRotatorMixedKindsRotation 断言 API key 与 OAuth 两类凭据在同一分组内混排轮换，
// 只有订阅型那一份会触发续期。
func TestRotatorMixedKindsRotation(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	refresher := &fakeRefresher{}
	group := Group{Scope: "merchant-1", Entries: []NamedCredential{
		{ID: 1, Name: "static", APIKey: "sk-static"},
		{ID: 2, Name: "subscription", APIKey: "old-access", OAuth: &OAuthCredential{
			Access: "old-access", Refresh: "refresh-2", Expires: clock.now.Add(time.Minute),
		}},
	}}
	r := newRenewalRotator(t, group, clock, refresher)
	route := oauthRenewalRoute()

	ctx := r.Begin(context.Background(), route)
	first, err := r.Resolve(ctx, route)
	if err != nil {
		t.Fatalf("解析第一条失败：%v", err)
	}
	if first.APIKey != "sk-static" {
		t.Fatalf("第一条 = %q，期望 sk-static", first.APIKey)
	}
	if refresher.callCount() != 0 {
		t.Fatalf("静态密钥不应触发续期，实际 %d 次", refresher.callCount())
	}

	nextCtx, switched := r.Advance(ctx, route, credentialRejectedError{})
	if !switched {
		t.Fatal("凭据类失败应切换到订阅型那一条")
	}
	second, err := r.Resolve(nextCtx, route)
	if err != nil {
		t.Fatalf("解析第二条失败：%v", err)
	}
	if second.APIKey != "new-access" {
		t.Fatalf("第二条 = %q，期望续期后的 new-access", second.APIKey)
	}
	if refresher.callCount() != 1 {
		t.Fatalf("订阅型凭据应恰好续期一次，实际 %d 次", refresher.callCount())
	}
}

// TestOAuthInvalidGrantErrorRedactsTokens 断言错误文案不含任何令牌。
func TestOAuthInvalidGrantErrorRedactsTokens(t *testing.T) {
	err := &oauthInvalidGrantError{name: "primary"}
	if got := err.Error(); got != `OAuth 凭据 "primary" 的刷新令牌已失效` {
		t.Fatalf("错误文案 = %q，应为固定形态", got)
	}
}

// TestRotatorRenewalLogsDoNotLeakTokens 断言续期失败、被拒与成功三条日志路径
// 都不出现访问令牌或刷新令牌，只出现凭据名。
func TestRotatorRenewalLogsDoNotLeakTokens(t *testing.T) {
	const accessToken = "access-should-not-appear"
	const refreshToken = "refresh-should-not-appear"
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}

	var buf bytes.Buffer
	refresher := &fakeRefresher{fn: func(context.Context, OAuthRefreshInput) (OAuthRefreshOutput, error) {
		return OAuthRefreshOutput{}, errors.New("端点 503")
	}}
	r, err := NewRotator(RotationOptions{
		Loader: fakeGroupLoader{group: Group{Scope: "merchant-1", Entries: []NamedCredential{{
			ID: 1, Name: "primary", APIKey: accessToken,
			OAuth: &OAuthCredential{Access: accessToken, Refresh: refreshToken, Expires: clock.now.Add(time.Minute)},
		}}}},
		Clock:    clock.Now,
		Cooldown: time.Minute,
		Logger:   slog.New(slog.NewTextHandler(&buf, nil)),
		Renewal:  &RenewalOptions{Refresher: refresher, Window: 5 * time.Minute},
	})
	if err != nil {
		t.Fatalf("构造轮换器失败：%v", err)
	}
	route := oauthRenewalRoute()

	// 非授权失败：走到 logRenewFailed，保留旧令牌。
	if _, err := r.Resolve(r.Begin(context.Background(), route), route); err != nil {
		t.Fatalf("非授权失败不应让解析失败：%v", err)
	}
	// invalid_grant：走到 logRenewRejected。
	refresher.fn = func(context.Context, OAuthRefreshInput) (OAuthRefreshOutput, error) {
		return OAuthRefreshOutput{}, oauth.ErrInvalidGrant
	}
	if _, err := r.Resolve(r.Begin(context.Background(), route), route); err == nil {
		t.Fatal("invalid_grant 应让解析失败")
	}
	// 成功：走到 logRenewed。
	refresher.fn = nil
	if _, err := r.Resolve(r.Begin(context.Background(), route), route); err != nil {
		t.Fatalf("续期成功时解析不应失败：%v", err)
	}

	out := buf.String()
	for _, token := range []string{accessToken, refreshToken} {
		if strings.Contains(out, token) {
			t.Fatalf("续期日志泄漏令牌 %q：%s", token, out)
		}
	}
	if !strings.Contains(out, "primary") {
		t.Errorf("续期日志应含凭据名：%s", out)
	}
}

// TestRenewalWaitTimesOut 断言等待他人续期超过上限时返回非 invalid_grant 错误，
// 使调用方保留旧令牌而不是把凭据判死。
func TestRenewalWaitTimesOut(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	refresher := &fakeRefresher{fn: func(ctx context.Context, _ OAuthRefreshInput) (OAuthRefreshOutput, error) {
		select {
		case <-block:
			return OAuthRefreshOutput{Access: "new-access"}, nil
		case <-ctx.Done():
			return OAuthRefreshOutput{}, ctx.Err()
		}
	}}
	state := newRenewalState(&RenewalOptions{
		Refresher: refresher,
		Window:    5 * time.Minute,
		Timeout:   20 * time.Millisecond,
	})
	in := OAuthRefreshInput{CredentialID: 9, Refresh: "refresh"}

	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = state.refresh(context.Background(), in)
	}()
	// 等首位调用者进入刷新。
	deadline := time.Now().Add(time.Second)
	for refresher.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	_, err := state.refresh(context.Background(), in)
	if err == nil {
		t.Fatal("等待超时应返回错误")
	}
	if errors.Is(err, oauth.ErrInvalidGrant) {
		t.Fatalf("等待超时不应被判为凭据失效，得到 %v", err)
	}
	<-leaderDone
}
