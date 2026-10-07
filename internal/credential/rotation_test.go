package credential

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/failure"
)

// 本文件覆盖凭据轮换与失败切换：轮换顺序、凭据类失败切换、组内耗尽、
// 冷却跳过与到期恢复、单次尝试的试用上限。

// fakeGroupLoader 是 GroupLoader 的测试替身，固定返回一份凭据快照。
type fakeGroupLoader struct {
	group Group
	err   error
}

func (l fakeGroupLoader) LoadGroup(context.Context, domain.Route) (Group, error) {
	if l.err != nil {
		return Group{}, l.err
	}
	return l.group, nil
}

// fakeClock 是可推进的测试时钟：冷却判定不依赖真实时间，测试不做 sleep。
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// credentialRejectedError 模拟上游客户端标注「本次凭据不被接受」的错误。
//
// 动作集里没有换渠道：认证类失败只换凭据，渠道本身没问题。
type credentialRejectedError struct{}

func (credentialRejectedError) Error() string { return "凭据被拒绝" }

func (credentialRejectedError) Actions() failure.Action {
	return failure.ActionSuspendAccount | failure.ActionRetryNextAccount
}

// quotaFailure 模拟分类器给出的额度类失败：冷却时长由类别策略表给出，不写死在用例里。
func quotaFailure() error {
	return failure.NewError(domain.CodeUpstreamRejected, "额度用尽", "", failure.ClassQuota)
}

// rotationRoute 是轮换用例共用的路由：分组名与协议不影响轮换逻辑，只用作游标键。
func rotationRoute() domain.Route {
	return domain.Route{CredentialRef: "group-a", Protocol: domain.ProtocolOpenAIChat}
}

// newTestRotator 构造注入测试时钟的轮换器。
func newTestRotator(t *testing.T, group Group, clock *fakeClock, cooldown time.Duration) *Rotator {
	t.Helper()
	r, err := NewRotator(RotationOptions{
		Loader:   fakeGroupLoader{group: group},
		Clock:    clock.Now,
		Cooldown: cooldown,
	})
	if err != nil {
		t.Fatalf("构造轮换器失败：%v", err)
	}
	return r
}

// twoKeyGroup 返回含两把凭据的分组。
func twoKeyGroup() Group {
	return Group{Scope: "merchant-1", Entries: []NamedCredential{
		{Name: "first", APIKey: "sk-first"},
		{Name: "second", APIKey: "sk-second"},
	}}
}

// resolveOne 在独立的一次渠道尝试里解析一份凭据，供只需要「取一条」的用例复用。
func resolveOne(t *testing.T, r *Rotator, route domain.Route) Credential {
	t.Helper()
	ctx := r.Begin(context.Background(), route)
	cred, err := r.Resolve(ctx, route)
	if err != nil {
		t.Fatalf("解析凭据失败：%v", err)
	}
	return cred
}

// TestRotatorAdvancesRoundRobin 断言同组凭据按轮换顺序取用，游标进程内循环。
func TestRotatorAdvancesRoundRobin(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	r := newTestRotator(t, twoKeyGroup(), clock, time.Minute)
	route := rotationRoute()

	want := []string{"sk-first", "sk-second", "sk-first"}
	for i, key := range want {
		if got := resolveOne(t, r, route).APIKey; got != key {
			t.Fatalf("第 %d 次取到 %q，期望 %q", i+1, got, key)
		}
	}
}

// TestRotatorSwitchesOnCredentialFailure 断言凭据类失败后在同一次尝试内换下一条。
func TestRotatorSwitchesOnCredentialFailure(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	r := newTestRotator(t, twoKeyGroup(), clock, time.Minute)
	route := rotationRoute()

	ctx := r.Begin(context.Background(), route)
	first, err := r.Resolve(ctx, route)
	if err != nil {
		t.Fatalf("解析第一条失败：%v", err)
	}
	if first.APIKey != "sk-first" {
		t.Fatalf("第一条 = %q，期望 sk-first", first.APIKey)
	}
	nextCtx, switched := r.Advance(ctx, route, credentialRejectedError{})
	if !switched {
		t.Fatal("凭据类失败应切换到下一条")
	}
	second, err := r.Resolve(nextCtx, route)
	if err != nil {
		t.Fatalf("解析第二条失败：%v", err)
	}
	if second.APIKey != "sk-second" {
		t.Fatalf("切换后取到 %q，期望 sk-second", second.APIKey)
	}
	if _, switched := r.Advance(nextCtx, route, credentialRejectedError{}); switched {
		t.Fatal("组内两把都试过后不应再切换")
	}
}

// TestRotatorExhaustedSingleCredential 断言只有一条凭据时耗尽即失败，不原地重试。
func TestRotatorExhaustedSingleCredential(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	group := Group{Scope: "merchant-1", Entries: []NamedCredential{{Name: "only", APIKey: "sk-only"}}}
	r := newTestRotator(t, group, clock, time.Minute)
	route := rotationRoute()

	ctx := r.Begin(context.Background(), route)
	if _, err := r.Resolve(ctx, route); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if _, switched := r.Advance(ctx, route, credentialRejectedError{}); switched {
		t.Fatal("组内只有一条凭据，失败后不应再切换")
	}
	// 耗尽后再解析必须显式报错，不得返回空密钥去发一个必然 401 的请求。
	if _, err := r.Resolve(ctx, route); err == nil {
		t.Fatal("试遍后再次解析应报错")
	} else if code := domain.AsError(err).Code; code != domain.CodeInternal {
		t.Fatalf("错误码 = %q，期望 %q", code, domain.CodeInternal)
	}
}

// TestRotatorNonCredentialFailureDoesNotSwitch 断言非凭据类失败不触发切换，留给渠道回退。
func TestRotatorNonCredentialFailureDoesNotSwitch(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	r := newTestRotator(t, twoKeyGroup(), clock, time.Minute)
	route := rotationRoute()

	ctx := r.Begin(context.Background(), route)
	if _, err := r.Resolve(ctx, route); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if _, switched := r.Advance(ctx, route, errors.New("上游限流")); switched {
		t.Fatal("非凭据类失败不应切换凭据")
	}
}

// TestRotatorSkipsCoolingAndRecovers 断言失败凭据在冷却期内被跳过，到期后恢复可用。
func TestRotatorSkipsCoolingAndRecovers(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	r := newTestRotator(t, twoKeyGroup(), clock, 30*time.Second)
	route := rotationRoute()

	// 第一次尝试第一把并让它失败：first 进入冷却。
	ctx := r.Begin(context.Background(), route)
	if _, err := r.Resolve(ctx, route); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if _, switched := r.Advance(ctx, route, credentialRejectedError{}); !switched {
		t.Fatal("凭据类失败应触发切换")
	}

	// 冷却期内新请求直接跳过 first，改用 second。
	if got := resolveOne(t, r, route).APIKey; got != "sk-second" {
		t.Fatalf("冷却期内取到 %q，期望 sk-second", got)
	}

	// 到期后 first 重新进入轮换：连续两次请求应覆盖两把。
	clock.Advance(31 * time.Second)
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		seen[resolveOne(t, r, route).APIKey] = true
	}
	if !seen["sk-first"] {
		t.Fatalf("冷却到期后 first 未恢复可用，实际取到 %v", seen)
	}
}

// TestAdvanceUsesFailureClassCooldown 断言分类方给出的冷却时长覆盖配置的默认值。
//
// 额度用尽与密钥失效的恢复速度差一个数量级：前者要等窗口滚动或加额，后者换一把 key 就好。
// 分类器知道是哪种，默认冷却不该把它抹平。
func TestAdvanceUsesFailureClassCooldown(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	// 配置的默认冷却只有 30 秒，远短于额度类失败给出的 15 分钟。
	r := newTestRotator(t, twoKeyGroup(), clock, 30*time.Second)
	route := rotationRoute()

	ctx := r.Begin(context.Background(), route)
	if _, err := r.Resolve(ctx, route); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if _, switched := r.Advance(ctx, route, quotaFailure()); !switched {
		t.Fatal("凭据类失败应触发切换")
	}

	// 默认冷却早已过去，但类给出的 15 分钟未到：first 仍应被跳过。
	clock.Advance(time.Minute)
	if got := resolveOne(t, r, route).APIKey; got != "sk-second" {
		t.Fatalf("类给出的冷却期内取到 %q，期望 sk-second", got)
	}

	// 类给出的冷却到期后 first 重新进入轮换。
	clock.Advance(15 * time.Minute)
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		seen[resolveOne(t, r, route).APIKey] = true
	}
	if !seen["sk-first"] {
		t.Fatalf("类给出的冷却到期后 first 未恢复可用，实际取到 %v", seen)
	}
}

// TestAdvanceFallsBackToConfiguredCooldown 断言错误未声明冷却时长时沿用配置的默认值。
//
// 多数失败（如认证）不在分类里另立数值，回退路径必须与引入分类之前的行径一致。
func TestAdvanceFallsBackToConfiguredCooldown(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	r := newTestRotator(t, twoKeyGroup(), clock, 30*time.Second)
	route := rotationRoute()

	ctx := r.Begin(context.Background(), route)
	if _, err := r.Resolve(ctx, route); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if _, switched := r.Advance(ctx, route, credentialRejectedError{}); !switched {
		t.Fatal("凭据类失败应触发切换")
	}

	// 默认冷却未到时跳过，到期后恢复。
	if got := resolveOne(t, r, route).APIKey; got != "sk-second" {
		t.Fatalf("冷却期内取到 %q，期望 sk-second", got)
	}
	clock.Advance(31 * time.Second)
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		seen[resolveOne(t, r, route).APIKey] = true
	}
	if !seen["sk-first"] {
		t.Fatalf("默认冷却到期后 first 未恢复可用，实际取到 %v", seen)
	}
}

// TestRotatorAllCoolingFallsOpen 断言整组都在冷却时仍取用凭据，而不是让请求无凭据可用。
func TestRotatorAllCoolingFallsOpen(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	r := newTestRotator(t, twoKeyGroup(), clock, time.Minute)
	route := rotationRoute()

	for _, name := range []string{"first", "second"} {
		r.markCooling("merchant-1", name, time.Minute)
	}
	ctx := r.Begin(context.Background(), route)
	resolved, err := r.Resolve(ctx, route)
	if err != nil {
		t.Fatalf("整组冷却时解析应 fail-open，实际报错：%v", err)
	}
	if resolved.APIKey == "" {
		t.Fatal("整组冷却时应取到一条凭据")
	}
}

// TestRotatorCapsTrialsPerAttempt 断言单次尝试的试用次数被上限截断。
func TestRotatorCapsTrialsPerAttempt(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	entries := make([]NamedCredential, 0, 6)
	for i := 0; i < 6; i++ {
		entries = append(entries, NamedCredential{Name: string(rune('a' + i)), APIKey: "sk-" + string(rune('a'+i))})
	}
	r := newTestRotator(t, Group{Scope: "merchant-1", Entries: entries}, clock, time.Minute)
	route := rotationRoute()

	ctx := r.Begin(context.Background(), route)
	trials := 0
	for {
		if _, err := r.Resolve(ctx, route); err != nil {
			break
		}
		trials++
		if _, switched := r.Advance(ctx, route, credentialRejectedError{}); !switched {
			break
		}
	}
	if trials != maxTrialsPerAttempt {
		t.Fatalf("试用次数 = %d，期望上限 %d", trials, maxTrialsPerAttempt)
	}
}

// TestRotatorEmptyGroupIsError 断言组内无启用凭据时按统一错误失败。
func TestRotatorEmptyGroupIsError(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	r := newTestRotator(t, Group{Scope: "merchant-1"}, clock, time.Minute)
	route := rotationRoute()

	if _, err := r.Resolve(r.Begin(context.Background(), route), route); err == nil {
		t.Fatal("空分组应报错")
	} else if code := domain.AsError(err).Code; code != domain.CodeInternal {
		t.Fatalf("错误码 = %q，期望 %q", code, domain.CodeInternal)
	}
}

// TestRotatorResolveWithoutBegin 断言未经 Begin 的解析仍按轮换顺序取第一条可用凭据。
func TestRotatorResolveWithoutBegin(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	r := newTestRotator(t, twoKeyGroup(), clock, time.Minute)
	route := rotationRoute()

	first, err := r.Resolve(context.Background(), route)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	second, err := r.Resolve(context.Background(), route)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if first.APIKey != "sk-first" || second.APIKey != "sk-second" {
		t.Fatalf("无状态解析未按轮换顺序：%q、%q", first.APIKey, second.APIKey)
	}
}

// TestNewRotatorRequiresLoader 断言缺少加载器在构造期报错。
func TestNewRotatorRequiresLoader(t *testing.T) {
	if _, err := NewRotator(RotationOptions{}); err == nil {
		t.Fatal("缺少加载器时应报错")
	}
}

// TestRotatorLogsDoNotLeakSecret 断言切换与冷却日志只出现凭据名与前缀，不出现完整密钥。
func TestRotatorLogsDoNotLeakSecret(t *testing.T) {
	const leaked = "not-a-real-secret-value"
	var buf bytes.Buffer
	r, err := NewRotator(RotationOptions{
		Loader: fakeGroupLoader{group: Group{Scope: "merchant-1", Entries: []NamedCredential{
			{Name: "old", APIKey: leaked},
			{Name: "new", APIKey: "second-secret"},
		}}},
		Clock:    (&fakeClock{now: time.Unix(1_700_000_000, 0)}).Now,
		Cooldown: time.Minute,
		Logger:   slog.New(slog.NewTextHandler(&buf, nil)),
	})
	if err != nil {
		t.Fatalf("构造轮换器失败：%v", err)
	}
	route := rotationRoute()

	ctx := r.Begin(context.Background(), route)
	if _, err := r.Resolve(ctx, route); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if _, switched := r.Advance(ctx, route, credentialRejectedError{}); !switched {
		t.Fatal("凭据类失败应触发切换")
	}
	// 冷却期内再解析一次，让「跳过」这条日志路径也被覆盖。
	if _, err := r.Resolve(r.Begin(context.Background(), route), route); err != nil {
		t.Fatalf("冷却期解析失败：%v", err)
	}

	out := buf.String()
	if strings.Contains(out, leaked) {
		t.Fatalf("日志泄漏完整密钥：%s", out)
	}
	if !strings.Contains(out, "old") {
		t.Errorf("日志应含凭据名：%s", out)
	}
	if !strings.Contains(out, keyPrefix(leaked)) {
		t.Errorf("日志应含密钥前缀 %q：%s", keyPrefix(leaked), out)
	}
}
