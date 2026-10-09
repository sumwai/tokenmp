package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/settlement"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件覆盖商家分佣的管理面动作：口径的部分更新与按账期出账。
//
// 断言口径与四处金额之间的恒等式，而不是只比某一项：出账的价值在于四个数能相互
// 对上，单看某一项全绿也可能算的是一笔对不上的账。

// settleMerchantsFixture 是出账用的一组商家：一个按周出账、一个未配口径（按月）。
func settleMerchantsFixture() []store.Merchant {
	return []store.Merchant{
		{ID: 1, Code: "partner-1", Name: "按周结算的商家", Kind: store.MerchantKindPartner},
		{ID: 2, Code: "platform", Name: "平台自营", Kind: store.MerchantKindPlatform},
	}
}

func TestMerchantSettleInfoDefaultsWhenUnset(t *testing.T) {
	fake := &fakeStore{}
	svc := newService(fake)
	info, err := svc.MerchantSettleInfo(context.Background(), 1)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if !info.CommissionRate.IsZero() || info.Period != billing.PeriodMonth {
		t.Errorf("未配置时应给默认口径（不抽成、按月），得到 %+v", info)
	}
}

func TestMerchantSettleInfoRejectsZeroMerchant(t *testing.T) {
	fake := &fakeStore{}
	svc := newService(fake)
	if _, err := svc.MerchantSettleInfo(context.Background(), 0); err == nil {
		t.Fatal("商家 id 为 0 应当被拒绝")
	}
	if fake.called("MerchantSettleInfo") {
		t.Error("非法输入不应触达存储层")
	}
}

func TestSetMerchantSettleInfoKeepsUnspecifiedFields(t *testing.T) {
	var written []byte
	fake := &fakeStore{
		merchantSettleInfo: func(context.Context, uint64) ([]byte, error) {
			return []byte(`{"commission_rate":"0.2000","period":"week"}`), nil
		},
		setMerchantSettle: func(_ context.Context, _ uint64, raw []byte) error {
			written = raw
			return nil
		},
	}
	svc := newService(fake)
	info, err := svc.SetMerchantSettleInfo(context.Background(), SettleInfoInput{
		MerchantID:     1,
		CommissionRate: "0.1",
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	// 只改抽成率：账期保留现值，不能被重置回默认月。
	if info.Period != billing.PeriodWeek {
		t.Errorf("账期应保留现值 week，得到 %q", info.Period)
	}
	if !info.CommissionRate.Equal(decimal.RequireFromString("0.1")) {
		t.Errorf("抽成率 = %s，期望 0.1", info.CommissionRate)
	}
	if got := string(written); got != `{"commission_rate":"0.1000","period":"week"}` {
		t.Errorf("落库形态 = %s", got)
	}
}

func TestSetMerchantSettleInfoRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		in   SettleInfoInput
	}{
		{name: "抽成率为 0 号商家", in: SettleInfoInput{MerchantID: 0, CommissionRate: "0.1"}},
		{name: "抽成率不是数字", in: SettleInfoInput{MerchantID: 1, CommissionRate: "一成"}},
		{name: "抽成率为负", in: SettleInfoInput{MerchantID: 1, CommissionRate: "-0.1"}},
		{name: "抽成率达到 1", in: SettleInfoInput{MerchantID: 1, CommissionRate: "1"}},
		{name: "账期不是可出账周期", in: SettleInfoInput{MerchantID: 1, Period: "5h"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeStore{}
			svc := newService(fake)
			if _, err := svc.SetMerchantSettleInfo(context.Background(), tc.in); err == nil {
				t.Fatal("非法输入应当被拒绝")
			}
			if fake.called("SetMerchantSettleInfo") {
				t.Error("非法输入不应写入存储层")
			}
		})
	}
}

func TestSetMerchantSettleInfoPropagatesMissingMerchant(t *testing.T) {
	fake := &fakeStore{
		merchantSettleInfo: func(context.Context, uint64) ([]byte, error) {
			return nil, errors.New("store: 商家 id=9 不存在")
		},
	}
	svc := newService(fake)
	if _, err := svc.SetMerchantSettleInfo(context.Background(), SettleInfoInput{MerchantID: 9, CommissionRate: "0.1"}); err == nil {
		t.Fatal("商家不存在时应当报错")
	}
	if fake.called("SetMerchantSettleInfo") {
		t.Error("商家不存在时不应写入存储层")
	}
}

// TestSettlementBillsUsesPerMerchantPeriod 覆盖「不给区间时按各商家自己的账期取
// 上一个完整自然周期」：fixedNow 是周一（2026-10-05），因此按周的商家取上一周，
// 未配口径的商家按月取上一月。
func TestSettlementBillsUsesPerMerchantPeriod(t *testing.T) {
	type window struct {
		from time.Time
		to   time.Time
	}
	got := map[uint64]window{}
	fake := &fakeStore{
		listMerchants: func(context.Context) ([]store.Merchant, error) {
			return settleMerchantsFixture(), nil
		},
		merchantSettleInfo: func(_ context.Context, id uint64) ([]byte, error) {
			if id == 1 {
				return []byte(`{"commission_rate":"0.1000","period":"week"}`), nil
			}
			return nil, nil
		},
		settlementFacts: func(_ context.Context, merchantID uint64, from, to time.Time) (store.SettlementFacts, error) {
			got[merchantID] = window{from: from, to: to}
			return store.SettlementFacts{MerchantID: merchantID, Trades: 2, GrossSales: "100", UpstreamCost: "30"}, nil
		},
	}
	svc := newService(fake)
	views, err := svc.SettlementBills(context.Background(), SettlementQuery{})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(views) != 2 {
		t.Fatalf("对账单条数 = %d，期望 2", len(views))
	}
	wantWeek := window{
		from: time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC),
		to:   time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
	}
	wantMonth := window{
		from: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		to:   time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	}
	if got[1] != wantWeek {
		t.Errorf("按周商家的账期 = %+v，期望 %+v", got[1], wantWeek)
	}
	if got[2] != wantMonth {
		t.Errorf("未配口径商家的账期 = %+v，期望 %+v", got[2], wantMonth)
	}
	if views[0].Period != string(billing.PeriodWeek) || views[1].Period != string(billing.PeriodMonth) {
		t.Errorf("对账单应带上各自账期：%+v", views)
	}
	// 两家各自的抽成率：按周商家配了 0.1，未配口径的商家是默认的 0。
	if views[0].CommissionRate != "0.1000" || views[1].CommissionRate != "0.0000" {
		t.Errorf("抽成率应按各商家口径带上：%s / %s", views[0].CommissionRate, views[1].CommissionRate)
	}
	assertViewBalanced(t, views, "100", "30")
}

// TestSettlementBillsExplicitWindow 覆盖显式区间：一家给定时所有商家用同一区间。
func TestSettlementBillsExplicitWindow(t *testing.T) {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var seen []time.Time
	fake := &fakeStore{
		listMerchants: func(context.Context) ([]store.Merchant, error) {
			return settleMerchantsFixture(), nil
		},
		settlementFacts: func(_ context.Context, merchantID uint64, f, t2 time.Time) (store.SettlementFacts, error) {
			if !f.Equal(from) || !t2.Equal(to) {
				t.Errorf("商家 %d 的账期 = %s ~ %s，期望 %s ~ %s", merchantID, f, t2, from, to)
			}
			seen = append(seen, f)
			return store.SettlementFacts{MerchantID: merchantID, GrossSales: "100", UpstreamCost: "30"}, nil
		},
	}
	svc := newService(fake)
	if _, err := svc.SettlementBills(context.Background(), SettlementQuery{From: from, To: to}); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("两个商家都应参与出账，实际 %d 家", len(seen))
	}
}

func TestSettlementBillsUnknownMerchant(t *testing.T) {
	fake := &fakeStore{
		listMerchants: func(context.Context) ([]store.Merchant, error) {
			return settleMerchantsFixture(), nil
		},
	}
	svc := newService(fake)
	if _, err := svc.SettlementBills(context.Background(), SettlementQuery{MerchantID: 99}); err == nil {
		t.Fatal("不存在的商家应当报错")
	}
}

// TestSettlementBillsKeepsIdentity 是验收条件 1 在管理面的断言：
// 「商家收益 + 平台抽成 == 卖出总额 − 上游成本」，且金额全为十进制字符串。
func TestSettlementBillsKeepsIdentity(t *testing.T) {
	fake := &fakeStore{
		listMerchants: func(context.Context) ([]store.Merchant, error) {
			return settleMerchantsFixture()[:1], nil
		},
		merchantSettleInfo: func(context.Context, uint64) ([]byte, error) {
			return []byte(`{"commission_rate":"0.1000","period":"month"}`), nil
		},
		settlementFacts: func(_ context.Context, merchantID uint64, _, _ time.Time) (store.SettlementFacts, error) {
			return store.SettlementFacts{MerchantID: merchantID, Trades: 3, GrossSales: "100", UpstreamCost: "30"}, nil
		},
	}
	svc := newService(fake)
	views, err := svc.SettlementBills(context.Background(), SettlementQuery{})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	assertViewBalanced(t, views, "100", "30")
	if views[0].Trades != 3 {
		t.Errorf("笔数 = %d，期望 3", views[0].Trades)
	}
	if views[0].CommissionRate != "0.1000" {
		t.Errorf("抽成率 = %s，期望 0.1000", views[0].CommissionRate)
	}
}

// assertViewBalanced 断言一组对账单自洽，且金额是可解析的十进制字符串。
func assertViewBalanced(t *testing.T, views []settlement.BillView, gross, cost string) {
	t.Helper()
	for _, v := range views {
		// 金额必须是十进制字符串：写成 JSON 数字会在消费方丢精度。
		for name, value := range map[string]string{
			"抽成率": v.CommissionRate, "卖出总额": v.GrossSales,
			"平台抽成": v.Commission, "上游成本": v.UpstreamCost, "商家收益": v.Payout,
		} {
			if strings.TrimSpace(value) == "" {
				t.Errorf("商家 %d 的%s为空", v.MerchantID, name)
				continue
			}
			if _, err := decimal.NewFromString(value); err != nil {
				t.Errorf("商家 %d 的%s %q 不是十进制字符串", v.MerchantID, name, value)
			}
		}
		total := decimal.RequireFromString(v.GrossSales).Sub(decimal.RequireFromString(v.UpstreamCost))
		sum := decimal.RequireFromString(v.Payout).Add(decimal.RequireFromString(v.Commission))
		if !sum.Equal(total) {
			t.Errorf("商家 %d 不满足分账恒等式：收益+抽成 = %s，卖出−成本 = %s", v.MerchantID, sum, total)
		}
		if !decimal.RequireFromString(v.GrossSales).Equal(decimal.RequireFromString(gross)) ||
			!decimal.RequireFromString(v.UpstreamCost).Equal(decimal.RequireFromString(cost)) {
			t.Errorf("商家 %d 的合计与输入事实不符：%+v", v.MerchantID, v)
		}
	}
}
