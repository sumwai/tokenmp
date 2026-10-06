package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// stubClassified 同时实现失败分类与冷却两个可选能力，模拟上游分类器产出的错误。
type stubClassified struct {
	class    string
	cooldown time.Duration
}

func (stubClassified) Error() string { return "上游额度用尽" }

func (s stubClassified) FailureClass() string { return s.class }

func (s stubClassified) CredentialCooldown() time.Duration { return s.cooldown }

// TestFailureClassOf 守护按类聚合的读取入口：未声明能力时返回空串，不 panic。
func TestFailureClassOf(t *testing.T) {
	if got := FailureClassOf(stubClassified{class: "quota"}); got != "quota" {
		t.Errorf("FailureClassOf = %q，期望 quota", got)
	}
	if got := FailureClassOf(errors.New("普通错误")); got != "" {
		t.Errorf("未声明分类时 = %q，期望空串", got)
	}
	// 包装链上任意一层声明即可被读到：生产端还会再套 retry-after、凭据拒绝一类包装。
	wrapped := fmt.Errorf("转发失败：%w", stubClassified{class: "credit"})
	if got := FailureClassOf(wrapped); got != "credit" {
		t.Errorf("包装后 FailureClassOf = %q，期望 credit", got)
	}
}

// TestCredentialCooldownOf 守护冷却读取入口：未声明或非正一律回落到调用方的默认冷却。
//
// 「没意见」与「零时长」对调用方是同一种含义，不必区分。
func TestCredentialCooldownOf(t *testing.T) {
	if got := CredentialCooldownOf(stubClassified{cooldown: 15 * time.Minute}); got != 15*time.Minute {
		t.Errorf("CredentialCooldownOf = %v，期望 15m", got)
	}
	cases := []struct {
		name string
		err  error
	}{
		{name: "未声明能力", err: errors.New("普通错误")},
		{name: "声明为零", err: stubClassified{cooldown: 0}},
		{name: "声明为负", err: stubClassified{cooldown: -time.Minute}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CredentialCooldownOf(tc.err); got != 0 {
				t.Errorf("CredentialCooldownOf = %v，期望 0（表示用调用方默认值）", got)
			}
		})
	}
}
