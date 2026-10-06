package me

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/quota"
	"github.com/sumwai/tokenmp/internal/settlement"
	"github.com/sumwai/tokenmp/internal/store"
)

// fakeStore 是自助查询的数据面替身；只记下被查询的参数，返回值由用例给出。
type fakeStore struct {
	account *store.Account
	buckets []settlement.Bucket
	recent  []store.UsageListRow
	quotas  []quota.Limit
	used    map[uint64]decimal.Decimal

	recentLimit int
	queries     []quota.UsageQuery
}

func (f *fakeStore) Account(_ context.Context, _ uint64) (*store.Account, error) {
	return f.account, nil
}

func (f *fakeStore) AccountBuckets(_ context.Context, _ uint64) ([]settlement.Bucket, error) {
	return f.buckets, nil
}

func (f *fakeStore) RecentUsage(_ context.Context, _ uint64, limit int) ([]store.UsageListRow, error) {
	f.recentLimit = limit
	if limit < len(f.recent) {
		return f.recent[:limit], nil
	}
	return f.recent, nil
}

func (f *fakeStore) Quotas(_ context.Context, scope billing.Scope, scopeID uint64) ([]quota.Limit, error) {
	var out []quota.Limit
	for _, limit := range f.quotas {
		if limit.Scope == scope && limit.ScopeID == scopeID {
			out = append(out, limit)
		}
	}
	return out, nil
}

func (f *fakeStore) Usage(_ context.Context, q quota.UsageQuery) (decimal.Decimal, error) {
	f.queries = append(f.queries, q)
	return f.used[q.QuotaID], nil
}

// fixedNow 是所有单测共用的当前时刻。
var fixedNow = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)

// TestSummaryAvailableBuckets 覆盖可用包的过滤、分组与组内扣减序。
func TestSummaryAvailableBuckets(t *testing.T) {
	expired := fixedNow.Add(-time.Hour)
	future := fixedNow.Add(48 * time.Hour)
	// 输入按存储层给出的扣减序：不过期的在前，其余按到期时间。
	st := &fakeStore{
		account: &store.Account{ID: 2, Code: "acct"},
		buckets: []settlement.Bucket{
			{ID: 1, Unit: billing.UnitSettleToken, Remaining: decimal.NewFromInt(100), Fallback: billing.FallbackReject},
			{ID: 2, Unit: billing.UnitSettleToken, Remaining: decimal.NewFromInt(50), Fallback: billing.FallbackChargeBalance, ExpiresAt: &future},
			{ID: 3, Unit: billing.UnitSettleToken, Remaining: decimal.Zero, Fallback: billing.FallbackReject},
			{ID: 4, Unit: billing.UnitSettleToken, Remaining: decimal.NewFromInt(30), Fallback: billing.FallbackReject, ExpiresAt: &expired},
			{ID: 5, Unit: billing.UnitSettleCurrency, Remaining: decimal.NewFromInt(1000), Fallback: billing.FallbackChargeBalance},
			{ID: 6, Unit: billing.UnitSettleCurrency, Remaining: decimal.NewFromInt(-5), Fallback: billing.FallbackChargeBalance},
		},
	}
	summary, err := New(st, func() time.Time { return fixedNow }).Summary(context.Background(), 2, 0, 0)
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if len(summary.Buckets) != 3 {
		t.Fatalf("可用包条数 = %d，期望 3（排除耗尽、过期与透支）", len(summary.Buckets))
	}
	// 按 unit 归拢：currency 全在 token 之前；组内保持输入顺序。
	want := []struct {
		unit      billing.UnitSettle
		remaining string
	}{
		{billing.UnitSettleCurrency, "1000"},
		{billing.UnitSettleToken, "100"},
		{billing.UnitSettleToken, "50"},
	}
	for i, w := range want {
		got := summary.Buckets[i]
		if got.Unit != w.unit || got.Remaining != w.remaining {
			t.Errorf("第 %d 条 = %s/%s，期望 %s/%s", i, got.Unit, got.Remaining, w.unit, w.remaining)
		}
	}
	if summary.Buckets[1].ExpiresAt != nil {
		t.Errorf("第 1 条不应带到期时间，得到 %v", summary.Buckets[1].ExpiresAt)
	}
	if summary.Buckets[2].ExpiresAt == nil || !summary.Buckets[2].ExpiresAt.Equal(future) {
		t.Errorf("第 2 条到期时间 = %v，期望 %v", summary.Buckets[2].ExpiresAt, future)
	}
}

// TestSummaryQuotaWindowReuse 覆盖限额展示复用判定路径的窗口与聚合口径：
// 聚合下界等于 quota.WindowStart 的输出，重置时间等于窗口结束。
func TestSummaryQuotaWindowReuse(t *testing.T) {
	limits := []quota.Limit{
		{ID: 1, Scope: billing.ScopeAccount, ScopeID: 2, Metric: billing.MetricRequest,
			WindowKind: billing.WindowKindCalendar, Period: billing.PeriodDay,
			LimitAmount: decimal.NewFromInt(100), Action: billing.ActionReject},
		{ID: 2, Scope: billing.ScopeAccount, ScopeID: 2, Metric: billing.MetricOutputToken,
			WindowKind: billing.WindowKindCalendar, Period: billing.PeriodTotal,
			LimitAmount: decimal.NewFromInt(999), Action: billing.ActionReject},
		{ID: 3, Scope: billing.ScopeAPIKey, ScopeID: 7, Metric: billing.MetricInputToken,
			WindowKind: billing.WindowKindRolling, Period: billing.Period5h,
			LimitAmount: decimal.NewFromInt(12), Action: billing.ActionThrottle},
	}
	st := &fakeStore{
		account: &store.Account{ID: 2, Code: "acct"},
		quotas:  limits,
		used: map[uint64]decimal.Decimal{
			1: decimal.NewFromInt(42),
			2: decimal.NewFromInt(5),
			3: decimal.NewFromInt(12),
		},
	}
	summary, err := New(st, func() time.Time { return fixedNow }).Summary(context.Background(), 2, 7, 0)
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if len(summary.Quotas) != 3 {
		t.Fatalf("限额条数 = %d，期望 3", len(summary.Quotas))
	}

	// 账户维度在前、密钥维度在后；各维度内按限额 id 升序。
	day := summary.Quotas[0]
	if day.Scope != billing.ScopeAccount || day.Metric != billing.MetricRequest {
		t.Errorf("第 1 条 = %s/%s，期望 account/request", day.Scope, day.Metric)
	}
	if day.Used != "42" || day.Limit != "100" || day.Action != billing.ActionReject {
		t.Errorf("第 1 条 = used %s limit %s action %s", day.Used, day.Limit, day.Action)
	}
	wantReset := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	if day.ResetsAt == nil || !day.ResetsAt.Equal(wantReset) {
		t.Errorf("日窗口重置时间 = %v，期望 %v", day.ResetsAt, wantReset)
	}

	if total := summary.Quotas[1]; total.ResetsAt != nil {
		t.Errorf("总量限额不应带重置时间，得到 %v", total.ResetsAt)
	}

	rolling := summary.Quotas[2]
	if rolling.ResetsAt == nil || !rolling.ResetsAt.Equal(fixedNow.Add(5*time.Hour)) {
		t.Errorf("滚动窗口重置时间 = %v，期望 %v", rolling.ResetsAt, fixedNow.Add(5*time.Hour))
	}

	// 三条聚合下界都必须来自 quota.WindowStart：日窗口取当日零点，总量取零值，
	// 滚动窗口取「当前时刻减一个周期」。
	wantSince := []time.Time{
		mustWindowStart(t, billing.WindowKindCalendar, billing.PeriodDay, fixedNow),
		mustWindowStart(t, billing.WindowKindCalendar, billing.PeriodTotal, fixedNow),
		mustWindowStart(t, billing.WindowKindRolling, billing.Period5h, fixedNow),
	}
	if len(st.queries) != len(wantSince) {
		t.Fatalf("聚合查询次数 = %d，期望 %d", len(st.queries), len(wantSince))
	}
	for i, want := range wantSince {
		if !st.queries[i].Since.Equal(want) {
			t.Errorf("第 %d 次聚合下界 = %v，期望 %v", i, st.queries[i].Since, want)
		}
	}
}

// mustWindowStart 直接取 quota.WindowStart 的结果；组合不可判定时让用例失败，
// 使「展示口径等于判定口径」的断言不会因为组合写错而变成空断言。
func mustWindowStart(t *testing.T, kind billing.WindowKind, period billing.Period, now time.Time) time.Time {
	t.Helper()
	start, ok := quota.WindowStart(kind, period, now)
	if !ok {
		t.Fatalf("窗口组合 %s/%s 不可判定", kind, period)
	}
	return start
}

// TestSummaryRecentChargedAmount 覆盖最近流水的折算与条数传递。
func TestSummaryRecentChargedAmount(t *testing.T) {
	st := &fakeStore{
		account: &store.Account{ID: 2, Code: "acct"},
		recent: []store.UsageListRow{
			{ID: 3, Model: "up-latest", GrossAmount: "26.00000000", Multiplier: "1.5000", CreatedAt: fixedNow},
			{ID: 2, Model: "up-earlier", GrossAmount: "10.00000000", Multiplier: "2.0000", CreatedAt: fixedNow.Add(-time.Minute)},
		},
	}
	summary, err := New(st, func() time.Time { return fixedNow }).Summary(context.Background(), 2, 0, 5)
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if st.recentLimit != 5 {
		t.Errorf("传给存储层的条数 = %d，期望 5", st.recentLimit)
	}
	if len(summary.Recent) != 2 {
		t.Fatalf("流水条数 = %d，期望 2", len(summary.Recent))
	}
	if summary.Recent[0].Model != "up-latest" || summary.Recent[0].ChargedAmount != "39" {
		t.Errorf("第一条 = %s/%s，期望 up-latest/39", summary.Recent[0].Model, summary.Recent[0].ChargedAmount)
	}
	if summary.Recent[1].ChargedAmount != "20" {
		t.Errorf("第二条付费金额 = %s，期望 20", summary.Recent[1].ChargedAmount)
	}
}

// TestSummaryJSONHasNoSensitiveFields 守护响应字段集合：
// 不含密钥、上游凭据、商家与渠道等内部标识。
func TestSummaryJSONHasNoSensitiveFields(t *testing.T) {
	st := &fakeStore{
		account: &store.Account{ID: 2, Code: "acct"},
		buckets: []settlement.Bucket{{ID: 1, Unit: billing.UnitSettleToken, Remaining: decimal.NewFromInt(5), Fallback: billing.FallbackReject}},
		quotas: []quota.Limit{{ID: 1, Scope: billing.ScopeAccount, ScopeID: 2, Metric: billing.MetricRequest,
			WindowKind: billing.WindowKindCalendar, Period: billing.PeriodDay,
			LimitAmount: decimal.NewFromInt(10), Action: billing.ActionReject}},
		used:   map[uint64]decimal.Decimal{1: decimal.NewFromInt(1)},
		recent: []store.UsageListRow{{ID: 1, Model: "m", GrossAmount: "1", Multiplier: "1", CreatedAt: fixedNow}},
	}
	summary, err := New(st, func() time.Time { return fixedNow }).Summary(context.Background(), 2, 7, 10)
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	body := string(raw)
	for _, forbidden := range []string{"merchant", "secret", "channel", "api_key", "password", "key_hash", "key_prefix"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("响应体出现敏感字段 %q：%s", forbidden, body)
		}
	}
	for _, want := range []string{`"account"`, `"buckets"`, `"quotas"`, `"recent"`, `"charged_amount"`, `"resets_at"`} {
		if !strings.Contains(body, want) {
			t.Errorf("响应体缺少字段 %s：%s", want, body)
		}
	}
}

// TestSummaryRejectsZeroAccount 覆盖归属缺失时拒绝查询。
func TestSummaryRejectsZeroAccount(t *testing.T) {
	if _, err := New(&fakeStore{}, nil).Summary(context.Background(), 0, 0, 0); err == nil {
		t.Fatal("account_id 为 0 时应报错")
	}
}
