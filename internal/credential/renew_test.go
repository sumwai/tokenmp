package credential

import (
	"context"
	"testing"
	"time"
)

// TestRotatorRenewClearsCooling 断言上游声明续期后解除本次尝试所用凭据的冷却，
// 且只解除该凭据。
func TestRotatorRenewClearsCooling(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	r := newTestRotator(t, twoKeyGroup(), clock, time.Minute)
	route := rotationRoute()

	ctx := r.Begin(context.Background(), route)
	if _, err := r.Resolve(ctx, route); err != nil {
		t.Fatalf("解析第一条失败：%v", err)
	}
	if _, switched := r.Advance(ctx, route, credentialRejectedError{}); !switched {
		t.Fatal("凭据类失败应触发切换")
	}
	if !r.coolingUntil("merchant-1", "first", clock.Now()) {
		t.Fatal("凭据类失败后 first 应处于冷却")
	}
	// 另一把凭据的冷却不应被续期动作连带解除。
	r.markCooling("merchant-1", "second")

	r.Renew(ctx, route)

	if r.coolingUntil("merchant-1", "first", clock.Now()) {
		t.Error("续期后本次尝试所用凭据不应仍在冷却")
	}
	if !r.coolingUntil("merchant-1", "second", clock.Now()) {
		t.Error("续期动作不得解除其它凭据的冷却")
	}
}

// TestRotatorRenewRestoresRotation 断言解除冷却后该凭据重新进入轮换顺序。
func TestRotatorRenewRestoresRotation(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	r := newTestRotator(t, twoKeyGroup(), clock, time.Minute)
	route := rotationRoute()

	ctx := r.Begin(context.Background(), route)
	if _, err := r.Resolve(ctx, route); err != nil {
		t.Fatalf("解析第一条失败：%v", err)
	}
	if _, switched := r.Advance(ctx, route, credentialRejectedError{}); !switched {
		t.Fatal("凭据类失败应触发切换")
	}
	// 冷却期内 first 被跳过。
	if got := resolveOne(t, r, route).APIKey; got != "sk-second" {
		t.Fatalf("冷却期内取到 %q，期望 sk-second", got)
	}

	r.Renew(ctx, route)

	// 连续两次解析应重新覆盖两把凭据，说明 first 已回到轮换顺序。
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		seen[resolveOne(t, r, route).APIKey] = true
	}
	if !seen["sk-first"] {
		t.Fatalf("续期后 first 未回到轮换顺序，实际取到 %v", seen)
	}
}

// TestRotatorRenewWithoutAttemptState 断言没有尝试级状态时续期为空操作，不 panic。
func TestRotatorRenewWithoutAttemptState(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	r := newTestRotator(t, twoKeyGroup(), clock, time.Minute)
	r.Renew(context.Background(), rotationRoute())
}
