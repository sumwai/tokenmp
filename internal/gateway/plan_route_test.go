package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/access"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/plan"
	"github.com/sumwai/tokenmp/internal/store"
)

// planRouteNow 是候选过滤用例的固定当前时刻。
var planRouteNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// planRouteCandidate 构造一条同协议候选。
func planRouteCandidate(channelID uint64, credGroup string) store.RouteCandidate {
	return store.RouteCandidate{
		ChannelID:   channelID,
		ChannelType: store.ChannelTypeOpenAIChat,
		BaseURL:     "http://up",
		CredGroup:   credGroup,
		Priority:    100,
		Weight:      100,
	}
}

// planChannelIDs 取候选链里的渠道 id 序列。
func planChannelIDs(routes []domain.Route) []uint64 {
	ids := make([]uint64, 0, len(routes))
	for _, r := range routes {
		ids = append(ids, r.ChannelID)
	}
	return ids
}

func TestCandidatesDropExhaustedCredGroups(t *testing.T) {
	st := &fakeGatewayStore{
		routes: []store.RouteCandidate{
			planRouteCandidate(1, "full"),
			planRouteCandidate(2, "ok"),
		},
		plans: []plan.UpstreamPlan{{
			ID: 7, MerchantID: 3, CredGroup: "full",
			LastCheckedAt: planRouteNow.Add(-time.Minute),
			Quotas: []plan.Quota{{
				ID:          11,
				Metric:      billing.MetricInputToken,
				WindowKind:  billing.WindowKindRolling,
				Period:      billing.Period5h,
				LimitAmount: decimal.RequireFromString("100"),
				LastUsed:    decimal.RequireFromString("100"),
			}},
		}},
	}
	resolver := storeRouteResolver{
		store:         st,
		intN:          func(int) int { return 0 },
		now:           func() time.Time { return planRouteNow },
		probeInterval: 5 * time.Minute,
	}
	ctx := access.WithIdentity(context.Background(), access.Identity{MerchantID: 3})
	routes, err := resolver.Candidates(ctx, &domain.Request{Protocol: domain.ProtocolOpenAIChat, Model: "m"})
	if err != nil {
		t.Fatalf("选路失败：%v", err)
	}
	if ids := planChannelIDs(routes); len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("候选 = %v，期望只剩渠道 2", ids)
	}
}

func TestCandidatesKeepStaleSnapshotChannels(t *testing.T) {
	st := &fakeGatewayStore{
		routes: []store.RouteCandidate{
			planRouteCandidate(1, "full"),
			planRouteCandidate(2, "ok"),
		},
		plans: []plan.UpstreamPlan{{
			ID: 7, MerchantID: 3, CredGroup: "full",
			// 超过两个采集周期：按未知放行。
			LastCheckedAt: planRouteNow.Add(-time.Hour),
			Quotas: []plan.Quota{{
				ID:          11,
				Metric:      billing.MetricInputToken,
				WindowKind:  billing.WindowKindRolling,
				Period:      billing.Period5h,
				LimitAmount: decimal.RequireFromString("100"),
				LastUsed:    decimal.RequireFromString("100"),
			}},
		}},
	}
	resolver := storeRouteResolver{
		store:         st,
		intN:          func(int) int { return 0 },
		now:           func() time.Time { return planRouteNow },
		probeInterval: 5 * time.Minute,
	}
	ctx := access.WithIdentity(context.Background(), access.Identity{MerchantID: 3})
	routes, err := resolver.Candidates(ctx, &domain.Request{Protocol: domain.ProtocolOpenAIChat, Model: "m"})
	if err != nil {
		t.Fatalf("选路失败：%v", err)
	}
	if ids := planChannelIDs(routes); len(ids) != 2 {
		t.Fatalf("过期快照应全部放行，得到 %v", ids)
	}
}

func TestCandidatesFailOpenOnPlanReadError(t *testing.T) {
	st := &fakeGatewayStore{
		routes:   []store.RouteCandidate{planRouteCandidate(1, "full")},
		plansErr: context.DeadlineExceeded,
	}
	resolver := storeRouteResolver{
		store:         st,
		intN:          func(int) int { return 0 },
		now:           func() time.Time { return planRouteNow },
		probeInterval: 5 * time.Minute,
	}
	ctx := access.WithIdentity(context.Background(), access.Identity{MerchantID: 3})
	routes, err := resolver.Candidates(ctx, &domain.Request{Protocol: domain.ProtocolOpenAIChat, Model: "m"})
	if err != nil {
		t.Fatalf("套餐读取失败不应让选路报错：%v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("套餐读取失败时应放行全部候选，得到 %v", planChannelIDs(routes))
	}
}
