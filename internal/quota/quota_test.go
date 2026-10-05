package quota

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// fakeRepo 是 Repo 的内存替身：按限额 id 给出已用量，并记下每次聚合查询。
//
// quotasByScope 非 nil 时按 scope 过滤返回，模拟真实存储层的维度过滤；为 nil 时
// 返回 limits 全量，供不关心 scope 归属的旧用例使用。
type fakeRepo struct {
	limits        []Limit
	quotasByScope map[billing.Scope][]Limit
	limitsErr     error
	used          map[uint64]decimal.Decimal
	usageErr      error
	queries       []UsageQuery
}

func (f *fakeRepo) Quotas(_ context.Context, scope billing.Scope, _ uint64) ([]Limit, error) {
	if f.limitsErr != nil {
		return nil, f.limitsErr
	}
	if f.quotasByScope != nil {
		return f.quotasByScope[scope], nil
	}
	return f.limits, nil
}

func (f *fakeRepo) Usage(_ context.Context, q UsageQuery) (decimal.Decimal, error) {
	f.queries = append(f.queries, q)
	if f.usageErr != nil {
		return decimal.Zero, f.usageErr
	}
	return f.used[q.QuotaID], nil
}

// noLog 是丢弃日志的 logf，断言不关心日志内容。
func noLog(string, ...any) {}

func newLimit(id uint64, period billing.Period, action billing.Action, amount string) Limit {
	return Limit{
		ID: id, Scope: billing.ScopeAccount, ScopeID: 2, Metric: billing.MetricRequest,
		WindowKind: billing.WindowKindCalendar, Period: period,
		LimitAmount: decimal.RequireFromString(amount), Action: action,
	}
}

func TestExceededIsInclusive(t *testing.T) {
	limit := newLimit(1, billing.PeriodDay, billing.ActionReject, "100")
	if !limit.Exceeded(decimal.RequireFromString("100")) {
		t.Error("恰好用满应当算超限")
	}
	if !limit.Exceeded(decimal.RequireFromString("101")) {
		t.Error("超过限额应当算超限")
	}
	if limit.Exceeded(decimal.RequireFromString("99.99999999")) {
		t.Error("未用满不应算超限")
	}
}

func TestCheckPassesWithoutLimits(t *testing.T) {
	repo := &fakeRepo{}
	svc := New(repo, noLog)
	if v := svc.Check(context.Background(), 2, 0, time.Now()); v != nil {
		t.Errorf("未配置限额应放行，得到 %+v", v)
	}
	if len(repo.queries) != 0 {
		t.Errorf("无限额不应发起聚合查询：%+v", repo.queries)
	}
}

func TestCheckReject(t *testing.T) {
	limit := newLimit(7, billing.PeriodDay, billing.ActionReject, "100")
	repo := &fakeRepo{limits: []Limit{limit}, used: map[uint64]decimal.Decimal{7: decimal.RequireFromString("100")}}
	svc := New(repo, noLog)
	now := mustParse(t, "2026-10-05 23:00:00")

	v := svc.Check(context.Background(), 2, 0, now)
	if v == nil {
		t.Fatal("达到限额应当超限")
	}
	if v.Limit.ID != 7 || v.Limit.Action != billing.ActionReject {
		t.Errorf("超限结论不符：%+v", v)
	}
	if v.RetryAfter != time.Hour {
		t.Errorf("reject 的 RetryAfter = %s，期望 1h", v.RetryAfter)
	}
	if len(repo.queries) != 1 {
		t.Fatalf("应发起一次聚合查询，实际 %d 次", len(repo.queries))
	}
	query := repo.queries[0]
	if query.QuotaID != 7 || query.Metric != billing.MetricRequest || query.Scope != billing.ScopeAccount || query.ScopeID != 2 {
		t.Errorf("聚合查询条件不符：%+v", query)
	}
	if want := mustParse(t, "2026-10-05 00:00:00"); !query.Since.Equal(want) {
		t.Errorf("聚合下界 = %s，期望 %s", query.Since, want)
	}
}

func TestCheckUnderLimitPasses(t *testing.T) {
	limit := newLimit(7, billing.PeriodDay, billing.ActionReject, "100")
	repo := &fakeRepo{limits: []Limit{limit}, used: map[uint64]decimal.Decimal{7: decimal.RequireFromString("99")}}
	if v := New(repo, noLog).Check(context.Background(), 2, 0, mustParse(t, "2026-10-05 12:00:00")); v != nil {
		t.Errorf("未达限额应放行，得到 %+v", v)
	}
}

func TestCheckThrottleCarriesRetryAfter(t *testing.T) {
	limit := newLimit(7, billing.PeriodDay, billing.ActionThrottle, "100")
	repo := &fakeRepo{limits: []Limit{limit}, used: map[uint64]decimal.Decimal{7: decimal.RequireFromString("120")}}
	v := New(repo, noLog).Check(context.Background(), 2, 0, mustParse(t, "2026-10-05 23:00:00"))
	if v == nil || v.Limit.Action != billing.ActionThrottle {
		t.Fatalf("throttle 超限结论不符：%+v", v)
	}
	if v.RetryAfter != time.Hour {
		t.Errorf("throttle 的 RetryAfter = %s，期望 1h", v.RetryAfter)
	}
}

// TestCheckRejectWinsOverThrottle 断言同时超限时优先返回 reject。
func TestCheckRejectWinsOverThrottle(t *testing.T) {
	throttle := newLimit(1, billing.PeriodDay, billing.ActionThrottle, "10")
	reject := newLimit(2, billing.PeriodDay, billing.ActionReject, "10")
	repo := &fakeRepo{
		limits: []Limit{throttle, reject},
		used:   map[uint64]decimal.Decimal{1: decimal.NewFromInt(20), 2: decimal.NewFromInt(20)},
	}
	v := New(repo, noLog).Check(context.Background(), 2, 0, mustParse(t, "2026-10-05 12:00:00"))
	if v == nil || v.Limit.Action != billing.ActionReject {
		t.Fatalf("应优先返回 reject，得到 %+v", v)
	}
}

// newAPIKeyLimit 构造一条 api_key 维度的限额。
func newAPIKeyLimit(id, scopeID uint64, period billing.Period, action billing.Action, amount string) Limit {
	return Limit{
		ID: id, Scope: billing.ScopeAPIKey, ScopeID: scopeID, Metric: billing.MetricRequest,
		WindowKind: billing.WindowKindCalendar, Period: period,
		LimitAmount: decimal.RequireFromString(amount), Action: action,
	}
}

// TestCheckAPIKeyScope 覆盖 api_key 维度的判定：账户与 key 两个维度各自按自己的
// scope 聚合，key 超限即返回该 key 的结论。
func TestCheckAPIKeyScope(t *testing.T) {
	account := newLimit(1, billing.PeriodDay, billing.ActionReject, "100")
	key := newAPIKeyLimit(2, 9, billing.PeriodDay, billing.ActionReject, "10")
	repo := &fakeRepo{
		quotasByScope: map[billing.Scope][]Limit{
			billing.ScopeAccount: {account},
			billing.ScopeAPIKey:  {key},
		},
		used: map[uint64]decimal.Decimal{1: decimal.NewFromInt(1), 2: decimal.NewFromInt(10)},
	}
	v := New(repo, noLog).Check(context.Background(), 2, 9, mustParse(t, "2026-10-05 12:00:00"))
	if v == nil || v.Limit.Scope != billing.ScopeAPIKey || v.Limit.ScopeID != 9 {
		t.Fatalf("key 超限应返回 api_key 结论，得到 %+v", v)
	}
	if len(repo.queries) != 2 {
		t.Fatalf("应查两个维度各一次，实际 %d 次：%+v", len(repo.queries), repo.queries)
	}
	byScope := make(map[billing.Scope]UsageQuery, len(repo.queries))
	for _, q := range repo.queries {
		byScope[q.Scope] = q
	}
	if q := byScope[billing.ScopeAPIKey]; q.ScopeID != 9 || q.QuotaID != 2 {
		t.Errorf("api_key 聚合条件不符：%+v", q)
	}
	if q := byScope[billing.ScopeAccount]; q.ScopeID != 2 || q.QuotaID != 1 {
		t.Errorf("account 聚合条件不符：%+v", q)
	}
}

// TestCheckAPIKeySharesAccountWindow 断言两个维度用同一套窗口计算：同 kind、
// 同 period 的限额聚合下界必须一致。
func TestCheckAPIKeySharesAccountWindow(t *testing.T) {
	account := newLimit(1, billing.PeriodMonth, billing.ActionReject, "100")
	key := newAPIKeyLimit(2, 9, billing.PeriodMonth, billing.ActionReject, "100")
	repo := &fakeRepo{
		quotasByScope: map[billing.Scope][]Limit{
			billing.ScopeAccount: {account},
			billing.ScopeAPIKey:  {key},
		},
	}
	New(repo, noLog).Check(context.Background(), 2, 9, mustParse(t, "2026-03-15 12:00:00"))
	if len(repo.queries) != 2 {
		t.Fatalf("应查两个维度各一次，实际 %d 次", len(repo.queries))
	}
	if !repo.queries[0].Since.Equal(repo.queries[1].Since) {
		t.Errorf("两个维度的窗口下界应一致：%s vs %s", repo.queries[0].Since, repo.queries[1].Since)
	}
	if want := mustParse(t, "2026-03-01 00:00:00"); !repo.queries[0].Since.Equal(want) {
		t.Errorf("窗口下界 = %s，期望月初 %s", repo.queries[0].Since, want)
	}
}

// TestCheckSkipsAPIKeyWithoutID 断言 apiKeyID 为 0 时不查 key 维度：流水里的
// api_key_id=0 行不会因「无 key 上下文」被计入某条 key 限额。
func TestCheckSkipsAPIKeyWithoutID(t *testing.T) {
	key := newAPIKeyLimit(2, 9, billing.PeriodDay, billing.ActionReject, "1")
	repo := &fakeRepo{
		quotasByScope: map[billing.Scope][]Limit{
			billing.ScopeAPIKey: {key},
		},
		used: map[uint64]decimal.Decimal{2: decimal.NewFromInt(100)},
	}
	if v := New(repo, noLog).Check(context.Background(), 2, 0, mustParse(t, "2026-10-05 12:00:00")); v != nil {
		t.Errorf("无 key 上下文不应判定 key 限额，得到 %+v", v)
	}
	for _, q := range repo.queries {
		if q.Scope == billing.ScopeAPIKey {
			t.Errorf("apiKeyID 为 0 时不应发起 api_key 聚合：%+v", q)
		}
	}
}

// TestCheckRejectWinsAcrossScopes 断言跨维度也优先 reject：账户维度的 throttle
// 不应盖住 key 维度的 reject。
func TestCheckRejectWinsAcrossScopes(t *testing.T) {
	account := newLimit(1, billing.PeriodDay, billing.ActionThrottle, "10")
	key := newAPIKeyLimit(2, 9, billing.PeriodDay, billing.ActionReject, "10")
	repo := &fakeRepo{
		quotasByScope: map[billing.Scope][]Limit{
			billing.ScopeAccount: {account},
			billing.ScopeAPIKey:  {key},
		},
		used: map[uint64]decimal.Decimal{1: decimal.NewFromInt(20), 2: decimal.NewFromInt(20)},
	}
	v := New(repo, noLog).Check(context.Background(), 2, 9, mustParse(t, "2026-10-05 12:00:00"))
	if v == nil || v.Limit.Action != billing.ActionReject || v.Limit.Scope != billing.ScopeAPIKey {
		t.Fatalf("应返回 key 维度的 reject，得到 %+v", v)
	}
}

// TestCheckToleratesFailures 覆盖宽容语义：定义读取失败、聚合失败、窗口组合不可判定
// 都不阻断转发。
func TestCheckToleratesFailures(t *testing.T) {
	tests := []struct {
		name string
		repo *fakeRepo
	}{
		{
			name: "定义读取失败",
			repo: &fakeRepo{limitsErr: errors.New("数据库不可用")},
		},
		{
			name: "聚合失败",
			repo: &fakeRepo{limits: []Limit{newLimit(3, billing.PeriodDay, billing.ActionReject, "1")}, usageErr: errors.New("超时")},
		},
		{
			name: "窗口组合不可判定",
			repo: &fakeRepo{limits: []Limit{{
				ID: 4, Scope: billing.ScopeAccount, ScopeID: 2, Metric: billing.MetricRequest,
				WindowKind: billing.WindowKindRolling, Period: billing.PeriodDay,
				LimitAmount: decimal.NewFromInt(1), Action: billing.ActionReject,
			}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if v := New(tt.repo, noLog).Check(context.Background(), 2, 0, mustParse(t, "2026-10-05 12:00:00")); v != nil {
				t.Errorf("应放行，得到 %+v", v)
			}
		})
	}
}

func TestUsedReportsInvalidWindow(t *testing.T) {
	repo := &fakeRepo{}
	limit := Limit{
		WindowKind: billing.WindowKindCalendar, Period: billing.Period5h,
	}
	_, ok, err := Used(context.Background(), repo, limit, time.Now())
	if ok || err != nil {
		t.Errorf("非法组合应返回 ok=false 且无错误，得到 ok=%v err=%v", ok, err)
	}
	if len(repo.queries) != 0 {
		t.Errorf("非法组合不应发起查询：%+v", repo.queries)
	}
}
