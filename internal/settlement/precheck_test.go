package settlement

import (
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
)

func TestFundable(t *testing.T) {
	expired := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	tests := []struct {
		name    string
		buckets []Bucket
		want    bool
	}{
		{name: "从未充值", buckets: nil, want: false},
		{
			name:    "耗尽的 reject 包",
			buckets: []Bucket{{ID: 1, Unit: billing.UnitSettleToken, Remaining: dec(t, "0"), Fallback: billing.FallbackReject}},
			want:    false,
		},
		{
			name:    "仍有存量",
			buckets: []Bucket{{ID: 1, Unit: billing.UnitSettleToken, Remaining: dec(t, "1")}},
			want:    true,
		},
		{
			name:    "currency 可透支",
			buckets: []Bucket{{ID: 1, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "0"), Fallback: billing.FallbackChargeBalance}},
			want:    true,
		},
		{
			name:    "currency reject 耗尽",
			buckets: []Bucket{{ID: 1, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "0"), Fallback: billing.FallbackReject}},
			want:    false,
		},
		{
			name:    "已过期的存量不算",
			buckets: []Bucket{{ID: 1, Unit: billing.UnitSettleToken, Remaining: dec(t, "10"), ExpiresAt: &expired}},
			want:    false,
		},
		{
			name:    "未过期的存量算",
			buckets: []Bucket{{ID: 1, Unit: billing.UnitSettleToken, Remaining: dec(t, "10"), ExpiresAt: &future}},
			want:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Fundable(tt.buckets, now); got != tt.want {
				t.Errorf("Fundable = %v，期望 %v", got, tt.want)
			}
		})
	}
}
