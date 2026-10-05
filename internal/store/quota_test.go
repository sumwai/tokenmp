package store

import (
	"context"
	"testing"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/quota"
)

// TestQuotaScopeColumn 锁定 scope 到 billing_usage 维度列的映射：
// account 与 api_key 各自有列，plan 没有列必须报错而不是静默按别的维度聚合。
func TestQuotaScopeColumn(t *testing.T) {
	tests := []struct {
		scope      billing.Scope
		wantColumn string
		wantErr    bool
	}{
		{scope: billing.ScopeAccount, wantColumn: "account_id"},
		{scope: billing.ScopeAPIKey, wantColumn: "api_key_id"},
		{scope: billing.ScopeChannel, wantColumn: "channel_id"},
		{scope: billing.ScopePlan, wantErr: true},
		{scope: billing.ScopePricing, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(string(tt.scope), func(t *testing.T) {
			column, err := quotaScopeColumn(tt.scope)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("scope %q 应报错，得到列 %q", tt.scope, column)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if column != tt.wantColumn {
				t.Errorf("列 = %q，期望 %q", column, tt.wantColumn)
			}
		})
	}
}

// TestUsageRejectsZeroScopeID 断言维度聚合不接受 scope_id=0：0 是「无维度」的哨兵，
// 按它聚合会把 api_key_id=0 的历史行算进某条 key 限额。
//
// 这里只走校验分支，不触达数据库，因此用零值 Store 即可。
func TestUsageRejectsZeroScopeID(t *testing.T) {
	for _, scope := range []billing.Scope{billing.ScopeAccount, billing.ScopeAPIKey} {
		if _, err := (&Store{}).Usage(context.Background(), quota.UsageQuery{
			Scope: scope, ScopeID: 0, Metric: billing.MetricRequest,
		}); err == nil {
			t.Errorf("scope %q 的 scope_id=0 应被拒绝", scope)
		}
	}
}
