package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
)

// recordedExec 是 executor 的记录型假实现：不连数据库，只记下收到的
// SQL 与参数，供断言参数组装。返回固定自增 id。
type recordedExec struct {
	calls int
	query string
	args  []any
	id    int64
	err   error
}

func (f *recordedExec) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	f.calls++
	f.query = query
	f.args = append([]any(nil), args...)
	if f.err != nil {
		return nil, f.err
	}
	return fakeResult{id: f.id}, nil
}

type fakeResult struct{ id int64 }

func (r fakeResult) LastInsertId() (int64, error) { return r.id, nil }
func (r fakeResult) RowsAffected() (int64, error) { return 1, nil }

// errExecFailure 用于断言写入错误被包装上表名。
var errExecFailure = errors.New("模拟驱动错误")

func TestNullArgAndTimeArg(t *testing.T) {
	if got := nullArg(""); got != nil {
		t.Errorf("空字符串应转成 nil，得到 %#v", got)
	}
	if got := nullArg("09:00:00"); got != "09:00:00" {
		t.Errorf("非空字符串应原样，得到 %#v", got)
	}
	if got := timeArg(nil); got != nil {
		t.Errorf("nil 时间应转成 nil，得到 %#v", got)
	}
	at := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	if got := timeArg(&at); got != at {
		t.Errorf("非空时间应原样，得到 %#v", got)
	}
}

func TestInsertPricingArgs(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fake := &recordedExec{id: 42}

	id, err := insertPricing(context.Background(), fake, Pricing{
		MerchantID:  7,
		Model:       "gpt-x",
		Version:     2,
		EffectiveAt: at,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if id != 42 {
		t.Errorf("自增 id = %d，期望 42", id)
	}
	if fake.query != insertPricingSQL {
		t.Errorf("SQL = %q，期望 %q", fake.query, insertPricingSQL)
	}
	want := []any{uint64(7), "gpt-x", 2, at}
	if !reflect.DeepEqual(fake.args, want) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, want)
	}
}

func TestInsertPricingRejectsBadInput(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		give Pricing
	}{
		{name: "商家为空", give: Pricing{Model: "m", Version: 1, EffectiveAt: at}},
		{name: "模型为空", give: Pricing{MerchantID: 1, Version: 1, EffectiveAt: at}},
		{name: "模型仅空白", give: Pricing{MerchantID: 1, Model: "  ", Version: 1, EffectiveAt: at}},
		{name: "版本为零", give: Pricing{MerchantID: 1, Model: "m", EffectiveAt: at}},
		{name: "生效时刻为零值", give: Pricing{MerchantID: 1, Model: "m", Version: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &recordedExec{}
			if _, err := insertPricing(context.Background(), fake, tt.give); err == nil {
				t.Fatal("应当被拒绝")
			}
			if fake.calls != 0 {
				t.Errorf("校验失败不应触达驱动，实际调用 %d 次", fake.calls)
			}
		})
	}
}

func TestInsertPricingWrapsDriverError(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fake := &recordedExec{err: errExecFailure}
	_, err := insertPricing(context.Background(), fake, Pricing{
		MerchantID: 1, Model: "m", Version: 1, EffectiveAt: at,
	})
	if !errors.Is(err, errExecFailure) {
		t.Errorf("驱动错误应被包装可辨识，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "billing_pricing") {
		t.Errorf("错误信息应含表名，得到 %v", err)
	}
}

func TestInsertPriceComponentsBatch(t *testing.T) {
	fake := &recordedExec{}
	err := insertPriceComponents(context.Background(), fake, []PriceComponent{
		{
			PricingID:  5,
			Metric:     billing.MetricInputToken,
			UnitSettle: billing.UnitSettleCurrency,
			UnitPrice:  "0.27",
			BasisQty:   "1000000",
		},
		{
			PricingID:  5,
			Metric:     billing.MetricOutputToken,
			UnitSettle: billing.UnitSettleCurrency,
			UnitPrice:  "1.10",
			BasisQty:   "1000000",
			TierFrom:   "1000",
			TierTo:     "5000",
			TierBasis:  "request_input",
		},
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if got := strings.Count(fake.query, "(?, ?, ?, ?, ?, ?, ?, ?)"); got != 2 {
		t.Errorf("占位符组数 = %d，期望 2，SQL=%q", got, fake.query)
	}
	if !strings.HasPrefix(fake.query, insertPriceComponentPrefix) {
		t.Errorf("SQL 前缀不符：%q", fake.query)
	}
	want := []any{
		uint64(5), billing.MetricInputToken, billing.UnitSettleCurrency, "0.27", "1000000", nil, nil, nil,
		uint64(5), billing.MetricOutputToken, billing.UnitSettleCurrency, "1.10", "1000000", "1000", "5000", "request_input",
	}
	if !reflect.DeepEqual(fake.args, want) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, want)
	}
}

func TestInsertPriceComponentsEmptyIsNoop(t *testing.T) {
	fake := &recordedExec{}
	if err := insertPriceComponents(context.Background(), fake, nil); err != nil {
		t.Fatalf("空切片不应报错：%v", err)
	}
	if fake.calls != 0 {
		t.Errorf("空切片不应触达驱动，实际调用 %d 次", fake.calls)
	}
}

func TestInsertPriceComponentsRejectsUnknownEnum(t *testing.T) {
	tests := []struct {
		name string
		give PriceComponent
	}{
		{
			name: "未知 metric",
			give: PriceComponent{PricingID: 1, Metric: "not_a_metric", UnitSettle: billing.UnitSettleToken, UnitPrice: "1", BasisQty: "1"},
		},
		{
			name: "未知结算单位",
			give: PriceComponent{PricingID: 1, Metric: billing.MetricRequest, UnitSettle: "usd", UnitPrice: "1", BasisQty: "1"},
		},
		{
			name: "单价为空",
			give: PriceComponent{PricingID: 1, Metric: billing.MetricRequest, UnitSettle: billing.UnitSettleToken, BasisQty: "1"},
		},
		{
			name: "基准量为空",
			give: PriceComponent{PricingID: 1, Metric: billing.MetricRequest, UnitSettle: billing.UnitSettleToken, UnitPrice: "1"},
		},
		{
			name: "定价 id 为零",
			give: PriceComponent{Metric: billing.MetricRequest, UnitSettle: billing.UnitSettleToken, UnitPrice: "1", BasisQty: "1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &recordedExec{}
			if err := insertPriceComponents(context.Background(), fake, []PriceComponent{tt.give}); err == nil {
				t.Fatal("应当被拒绝")
			}
			if fake.calls != 0 {
				t.Errorf("校验失败不应触达驱动，实际调用 %d 次", fake.calls)
			}
		})
	}
}

func TestInsertPriceRuleNullableColumns(t *testing.T) {
	fake := &recordedExec{id: 9}
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id, err := insertPriceRule(context.Background(), fake, PriceRule{
		Scope:      billing.ScopeAccount,
		ScopeID:    3,
		Multiplier: "0.8",
		ValidFrom:  &from,
		TimeFrom:   "22:00:00",
		Priority:   200,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if id != 9 {
		t.Errorf("自增 id = %d，期望 9", id)
	}
	if fake.query != insertPriceRuleSQL {
		t.Errorf("SQL = %q，期望 %q", fake.query, insertPriceRuleSQL)
	}
	// 未设置的列必须是 NULL：metric / valid_to / time_to / 掩码 / calendar。
	want := []any{
		billing.ScopeAccount, uint64(3), nil, "0.8",
		from, nil, "22:00:00", nil,
		(*uint8)(nil), (*uint16)(nil), nil, 200,
	}
	if !reflect.DeepEqual(fake.args, want) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, want)
	}
}

func TestInsertPriceRuleRejectsBadScope(t *testing.T) {
	fake := &recordedExec{}
	_, err := insertPriceRule(context.Background(), fake, PriceRule{
		Scope: billing.Scope("tenant"), ScopeID: 1, Multiplier: "1",
	})
	if err == nil {
		t.Fatal("未知 scope 应当被拒绝")
	}
	if fake.calls != 0 {
		t.Errorf("校验失败不应触达驱动，实际调用 %d 次", fake.calls)
	}
}

func TestUpsertCalendarDays(t *testing.T) {
	fake := &recordedExec{}
	err := upsertCalendarDays(context.Background(), fake, "cn", []CalendarDay{
		{Date: "2026-10-01", DayKind: billing.DayKindHoliday},
		{Date: "2026-09-27", DayKind: billing.DayKindMakeupWorkday},
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if !strings.HasPrefix(fake.query, upsertCalendarPrefix) {
		t.Errorf("SQL 前缀不符：%q", fake.query)
	}
	if !strings.HasSuffix(fake.query, upsertCalendarSuffix) {
		t.Errorf("SQL 后缀不符：%q", fake.query)
	}
	want := []any{
		"cn", "2026-10-01", billing.DayKindHoliday,
		"cn", "2026-09-27", billing.DayKindMakeupWorkday,
	}
	if !reflect.DeepEqual(fake.args, want) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, want)
	}
}

func TestUpsertCalendarDaysRejectsBadInput(t *testing.T) {
	tests := []struct {
		name     string
		calendar string
		days     []CalendarDay
	}{
		{name: "日历名为空", calendar: "  ", days: []CalendarDay{{Date: "2026-10-01", DayKind: billing.DayKindHoliday}}},
		{name: "日期为空", calendar: "cn", days: []CalendarDay{{DayKind: billing.DayKindHoliday}}},
		{name: "未知日期性质", calendar: "cn", days: []CalendarDay{{Date: "2026-10-01", DayKind: "vacation"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &recordedExec{}
			if err := upsertCalendarDays(context.Background(), fake, tt.calendar, tt.days); err == nil {
				t.Fatal("应当被拒绝")
			}
			if fake.calls != 0 {
				t.Errorf("校验失败不应触达驱动，实际调用 %d 次", fake.calls)
			}
		})
	}
}

func TestUpsertCalendarDaysEmptyIsNoop(t *testing.T) {
	fake := &recordedExec{}
	if err := upsertCalendarDays(context.Background(), fake, "cn", nil); err != nil {
		t.Fatalf("空切片不应报错：%v", err)
	}
	if fake.calls != 0 {
		t.Errorf("空切片不应触达驱动，实际调用 %d 次", fake.calls)
	}
}

func TestInsertProductArgs(t *testing.T) {
	fake := &recordedExec{id: 3}
	id, err := insertProduct(context.Background(), fake, Product{
		MerchantID:   1,
		Name:         "10 元 100M",
		Unit:         billing.UnitSettleToken,
		Qty:          "100000000",
		Price:        "10",
		ModelScope:   []byte(`["gpt-x"]`),
		ValidityDays: 30,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if id != 3 {
		t.Errorf("自增 id = %d，期望 3", id)
	}
	want := []any{uint64(1), "10 元 100M", billing.UnitSettleToken, "100000000", "10", []byte(`["gpt-x"]`), 30}
	if !reflect.DeepEqual(fake.args, want) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, want)
	}
}

func TestInsertProductRejectsBadUnit(t *testing.T) {
	fake := &recordedExec{}
	_, err := insertProduct(context.Background(), fake, Product{
		MerchantID: 1, Name: "p", Unit: "usd", Qty: "1", Price: "1",
	})
	if err == nil {
		t.Fatal("未知单位应当被拒绝")
	}
	if fake.calls != 0 {
		t.Errorf("校验失败不应触达驱动，实际调用 %d 次", fake.calls)
	}
}

func TestInsertPurchaseArgs(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fake := &recordedExec{}
	if _, err := insertPurchase(context.Background(), fake, Purchase{
		AccountID: 11, MerchantID: 1, ProductID: 3, Qty: "2", PricePaid: "20", PurchasedAt: at,
	}); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	want := []any{uint64(11), uint64(1), uint64(3), "2", "20", at}
	if !reflect.DeepEqual(fake.args, want) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, want)
	}
}

func TestInsertQuotaRejectsUnknownEnum(t *testing.T) {
	base := Quota{
		Scope: billing.ScopeAccount, ScopeID: 1, Metric: billing.MetricRequest,
		WindowKind: billing.WindowKindRolling, Period: billing.Period5h,
		LimitAmount: "100", Action: billing.ActionReject,
	}
	tests := []struct {
		name string
		give Quota
	}{
		{name: "未知 scope", give: func() Quota { q := base; q.Scope = "tenant"; return q }()},
		{name: "未知 metric", give: func() Quota { q := base; q.Metric = "x"; return q }()},
		{name: "未知窗口类型", give: func() Quota { q := base; q.WindowKind = "sliding"; return q }()},
		{name: "未知周期", give: func() Quota { q := base; q.Period = "year"; return q }()},
		{name: "未知处置", give: func() Quota { q := base; q.Action = "warn"; return q }()},
		{name: "限额为空", give: func() Quota { q := base; q.LimitAmount = ""; return q }()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &recordedExec{}
			if _, err := insertQuota(context.Background(), fake, tt.give); err == nil {
				t.Fatal("应当被拒绝")
			}
			if fake.calls != 0 {
				t.Errorf("校验失败不应触达驱动，实际调用 %d 次", fake.calls)
			}
		})
	}

	fake := &recordedExec{}
	if _, err := insertQuota(context.Background(), fake, base); err != nil {
		t.Fatalf("合法 quota 不应报错：%v", err)
	}
}

func TestInsertQuotaEventRequiresAudit(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fake := &recordedExec{}
	if _, err := insertQuotaEvent(context.Background(), fake, QuotaEvent{
		QuotaID: 1, Event: billing.QuotaEventReset, BaselineAt: at,
	}); err == nil {
		t.Fatal("缺 operator 应当被拒绝")
	}
	if fake.calls != 0 {
		t.Errorf("校验失败不应触达驱动，实际调用 %d 次", fake.calls)
	}

	ok := &recordedExec{}
	if _, err := insertQuotaEvent(context.Background(), ok, QuotaEvent{
		QuotaID: 1, Event: billing.QuotaEventReset, BaselineAt: at, Operator: "ops",
	}); err != nil {
		t.Fatalf("合法事件不应报错：%v", err)
	}
}

func TestInsertAdjustmentRequiresAudit(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		give Adjustment
	}{
		{name: "账户为零", give: Adjustment{DeltaAmount: "1", Reason: "r", Operator: "ops", CreatedAt: at}},
		{name: "金额为空", give: Adjustment{AccountID: 1, Reason: "r", Operator: "ops", CreatedAt: at}},
		{name: "原因为空", give: Adjustment{AccountID: 1, DeltaAmount: "1", Operator: "ops", CreatedAt: at}},
		{name: "操作者为空", give: Adjustment{AccountID: 1, DeltaAmount: "1", Reason: "r", CreatedAt: at}},
		{name: "时间为零值", give: Adjustment{AccountID: 1, DeltaAmount: "1", Reason: "r", Operator: "ops"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &recordedExec{}
			if _, err := insertAdjustment(context.Background(), fake, tt.give); err == nil {
				t.Fatal("应当被拒绝")
			}
			if fake.calls != 0 {
				t.Errorf("校验失败不应触达驱动，实际调用 %d 次", fake.calls)
			}
		})
	}
}

func TestScanTimeAcceptsDriverForms(t *testing.T) {
	loc := time.UTC
	tests := []struct {
		name      string
		src       any
		wantValid bool
		wantTime  time.Time
		wantErr   bool
	}{
		{name: "NULL", src: nil, wantValid: false},
		{name: "驱动开启 parseTime 的 time.Time", src: time.Date(2026, 10, 1, 8, 0, 0, 0, loc), wantValid: true, wantTime: time.Date(2026, 10, 1, 8, 0, 0, 0, loc)},
		{name: "未开启 parseTime 的 DATETIME 字节", src: []byte("2026-10-01 08:00:00"), wantValid: true, wantTime: time.Date(2026, 10, 1, 8, 0, 0, 0, loc)},
		{name: "带微秒的 DATETIME", src: []byte("2026-10-01 08:00:00.123456"), wantValid: true, wantTime: time.Date(2026, 10, 1, 8, 0, 0, 123456000, loc)},
		{name: "DATE", src: "2026-10-01", wantValid: true, wantTime: time.Date(2026, 10, 1, 0, 0, 0, 0, loc)},
		{name: "TIME", src: []byte("22:30:15"), wantValid: true, wantTime: time.Date(0, 1, 1, 22, 30, 15, 0, loc)},
		{name: "无法解析", src: []byte("not-a-time"), wantErr: true},
		{name: "不支持的类型", src: int64(1), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s scanTime
			err := s.Scan(tt.src)
			if tt.wantErr {
				if err == nil {
					t.Fatal("应当报错")
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if s.Valid != tt.wantValid {
				t.Errorf("Valid = %v，期望 %v", s.Valid, tt.wantValid)
			}
			if tt.wantValid && !s.Time.Equal(tt.wantTime) {
				t.Errorf("Time = %s，期望 %s", s.Time, tt.wantTime)
			}
		})
	}
}
