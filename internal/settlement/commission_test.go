package settlement

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件覆盖分佣口径：settle_info 的解析与编码、对账恒等式、账期边界与对外形态。
//
// 金额断言一律比较 decimal 取值而不是字符串：库里读到的是 DECIMAL 文本，
// 标度补零（0.1 与 0.1000）是同一笔钱，按字符串比会把等价取值判成不等。

func mustDecimal(t *testing.T, raw string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(raw)
	if err != nil {
		t.Fatalf("解析 %q 失败：%v", raw, err)
	}
	return d
}

func TestParseSettleInfo(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantRate   string
		wantPeriod billing.Period
		wantErr    bool
	}{
		{name: "NULL 走默认", raw: "", wantRate: "0", wantPeriod: billing.PeriodMonth},
		{name: "字面 null 走默认", raw: "null", wantRate: "0", wantPeriod: billing.PeriodMonth},
		{name: "空对象走默认", raw: "{}", wantRate: "0", wantPeriod: billing.PeriodMonth},
		{name: "只配了抽成率时账期取默认", raw: `{"commission_rate":"0.25"}`, wantRate: "0.25", wantPeriod: billing.PeriodMonth},
		{name: "只配了账期时抽成率为 0", raw: `{"period":"week"}`, wantRate: "0", wantPeriod: billing.PeriodWeek},
		{name: "完整配置", raw: `{"commission_rate":"0.15","period":"day"}`, wantRate: "0.15", wantPeriod: billing.PeriodDay},
		{name: "非法 JSON", raw: `{`, wantErr: true},
		{name: "抽成率不是数字", raw: `{"commission_rate":"一成"}`, wantErr: true},
		{name: "抽成率为负", raw: `{"commission_rate":"-0.1"}`, wantErr: true},
		{name: "抽成率等于 1", raw: `{"commission_rate":"1"}`, wantErr: true},
		{name: "账期是限额周期不是账期", raw: `{"period":"5h"}`, wantErr: true},
		{name: "账期是总量口径", raw: `{"period":"total"}`, wantErr: true},
		{name: "抽成率小数位多于倍率标度", raw: `{"commission_rate":"0.12345"}`, wantErr: true},
		{name: "抽成率四位小数可用", raw: `{"commission_rate":"0.1234"}`, wantRate: "0.1234", wantPeriod: billing.PeriodMonth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseSettleInfo([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("%q 应当被拒绝，得到 %+v", tc.raw, info)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if !info.CommissionRate.Equal(mustDecimal(t, tc.wantRate)) {
				t.Errorf("抽成率 = %s，期望 %s", info.CommissionRate, tc.wantRate)
			}
			if info.Period != tc.wantPeriod {
				t.Errorf("账期 = %q，期望 %q", info.Period, tc.wantPeriod)
			}
		})
	}
}

func TestEncodeSettleInfoNormalizes(t *testing.T) {
	encoded, err := SettleInfo{CommissionRate: mustDecimal(t, "0.1"), Period: billing.PeriodMonth}.Encode()
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	want := `{"commission_rate":"0.1000","period":"month"}`
	if string(encoded) != want {
		t.Fatalf("编码结果 = %s，期望 %s", encoded, want)
	}
	// 往返：归一化后的形态必须能被同一套解析读回同一取值。
	back, err := ParseSettleInfo(encoded)
	if err != nil {
		t.Fatalf("回读失败：%v", err)
	}
	if !back.CommissionRate.Equal(mustDecimal(t, "0.1")) || back.Period != billing.PeriodMonth {
		t.Errorf("往返后取值变化：%+v", back)
	}
}

func TestEncodeSettleInfoRejectsOutOfRange(t *testing.T) {
	if _, err := (SettleInfo{CommissionRate: mustDecimal(t, "1.5"), Period: billing.PeriodMonth}).Encode(); err == nil {
		t.Error("越界抽成率应当被拒绝")
	}
	if _, err := (SettleInfo{CommissionRate: mustDecimal(t, "0.1"), Period: billing.Period5h}).Encode(); err == nil {
		t.Error("非账期周期应当被拒绝")
	}
}

// TestSettleKeepsIdentity 是验收条件 1 的断言：同一账期内
// 「商家收益 + 平台抽成 == 卖出总额 − 上游成本」。
func TestSettleKeepsIdentity(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	cases := []struct {
		name         string
		gross        string
		cost         string
		rate         string
		wantComm     string
		wantPayout   string
		wantBalanced bool
	}{
		{
			name: "平台自营不抽成", gross: "100", cost: "30", rate: "0",
			wantComm: "0", wantPayout: "70", wantBalanced: true,
		},
		{
			name: "一成抽成", gross: "100", cost: "30", rate: "0.1",
			wantComm: "10", wantPayout: "60", wantBalanced: true,
		},
		{
			name: "抽成按金额标度舍入", gross: "0.005", cost: "0", rate: "0.1",
			wantComm: "0.0005", wantPayout: "0.0045", wantBalanced: true,
		},
		{
			name: "上游成本超过卖出时收益为负", gross: "10", cost: "12", rate: "0.1",
			wantComm: "1", wantPayout: "-3", wantBalanced: true,
		},
		{
			name: "账期无成交", gross: "0", cost: "0", rate: "0.2",
			wantComm: "0", wantPayout: "0", wantBalanced: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := SettleInfo{CommissionRate: mustDecimal(t, tc.rate), Period: billing.PeriodMonth}
			bill := Settle(7, info, from, to, Summary{
				Trades:       3,
				GrossSales:   mustDecimal(t, tc.gross),
				UpstreamCost: mustDecimal(t, tc.cost),
			})
			if !bill.Commission.Equal(mustDecimal(t, tc.wantComm)) {
				t.Errorf("平台抽成 = %s，期望 %s", bill.Commission, tc.wantComm)
			}
			if !bill.Payout.Equal(mustDecimal(t, tc.wantPayout)) {
				t.Errorf("商家收益 = %s，期望 %s", bill.Payout, tc.wantPayout)
			}
			if bill.Balanced() != tc.wantBalanced {
				t.Errorf("Balanced() = %v，期望 %v", bill.Balanced(), tc.wantBalanced)
			}
			if bill.MerchantID != 7 || bill.Trades != 3 {
				t.Errorf("归属与笔数应原样带上：%+v", bill)
			}
		})
	}
}

// TestBillViewAmountsAreStrings 是验收条件 2 的断言：对外形态的金额是十进制字符串。
func TestBillViewAmountsAreStrings(t *testing.T) {
	info := SettleInfo{CommissionRate: mustDecimal(t, "0.1"), Period: billing.PeriodMonth}
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	bill := Settle(1, info, from, from.AddDate(0, 1, 0), Summary{
		Trades:       2,
		GrossSales:   mustDecimal(t, "100"),
		UpstreamCost: mustDecimal(t, "30"),
	})
	view := bill.View()

	for name, got := range map[string]string{
		"卖出总额": view.GrossSales,
		"平台抽成": view.Commission,
		"上游成本": view.UpstreamCost,
		"商家收益": view.Payout,
		"抽成率":  view.CommissionRate,
	} {
		if _, err := decimal.NewFromString(got); err != nil {
			t.Errorf("%s 的对外形态 %q 不是十进制字符串", name, got)
		}
	}
	if view.From != "2026-09-01T00:00:00Z" || view.To != "2026-10-01T00:00:00Z" {
		t.Errorf("账期边界应带时区输出：%s ~ %s", view.From, view.To)
	}
	if view.Period != string(billing.PeriodMonth) {
		t.Errorf("账期 = %q", view.Period)
	}
	// 对外形态读回来同样自洽，页面与对账单文件据此自检。
	payout := mustDecimal(t, view.Payout)
	commission := mustDecimal(t, view.Commission)
	if !payout.Add(commission).Equal(mustDecimal(t, view.GrossSales).Sub(mustDecimal(t, view.UpstreamCost))) {
		t.Error("对外形态不满足分账恒等式")
	}
}

func TestSettleInfoView(t *testing.T) {
	view := SettleInfo{CommissionRate: mustDecimal(t, "0.15"), Period: billing.PeriodWeek}.View(9)
	if view.MerchantID != 9 || view.CommissionRate != "0.1500" || view.Period != "week" {
		t.Errorf("口径对外形态不符：%+v", view)
	}
}

func TestLastPeriod(t *testing.T) {
	cases := []struct {
		name     string
		period   billing.Period
		now      time.Time
		wantFrom string
		wantTo   string
		wantErr  bool
	}{
		{
			name:     "上一自然日",
			period:   billing.PeriodDay,
			now:      time.Date(2026, 10, 9, 15, 30, 0, 0, time.UTC),
			wantFrom: "2026-10-08T00:00:00Z", wantTo: "2026-10-09T00:00:00Z",
		},
		{
			name:     "跨月的上一自然日",
			period:   billing.PeriodDay,
			now:      time.Date(2026, 3, 1, 1, 0, 0, 0, time.UTC),
			wantFrom: "2026-02-28T00:00:00Z", wantTo: "2026-03-01T00:00:00Z",
		},
		{
			name:     "上一自然周（周五看，取上周一到周日）",
			period:   billing.PeriodWeek,
			now:      time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC),
			wantFrom: "2026-09-28T00:00:00Z", wantTo: "2026-10-05T00:00:00Z",
		},
		{
			name:     "周日看同一周时取上周",
			period:   billing.PeriodWeek,
			now:      time.Date(2026, 10, 11, 23, 0, 0, 0, time.UTC),
			wantFrom: "2026-09-28T00:00:00Z", wantTo: "2026-10-05T00:00:00Z",
		},
		{
			name:     "跨年的上一自然月",
			period:   billing.PeriodMonth,
			now:      time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
			wantFrom: "2025-12-01T00:00:00Z", wantTo: "2026-01-01T00:00:00Z",
		},
		{
			name:     "本地时区的时刻先折成 UTC",
			period:   billing.PeriodMonth,
			now:      time.Date(2026, 10, 1, 3, 0, 0, 0, time.FixedZone("CST", 8*3600)),
			wantFrom: "2026-08-01T00:00:00Z", wantTo: "2026-09-01T00:00:00Z",
		},
		{name: "滚动窗口不是账期", period: billing.Period5h, now: time.Now(), wantErr: true},
		{name: "总量口径不是账期", period: billing.PeriodTotal, now: time.Now(), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, to, err := LastPeriod(tc.period, tc.now)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("应当被拒绝，得到 %s ~ %s", from, to)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if got := from.UTC().Format(time.RFC3339); got != tc.wantFrom {
				t.Errorf("账期起点 = %s，期望 %s", got, tc.wantFrom)
			}
			if got := to.UTC().Format(time.RFC3339); got != tc.wantTo {
				t.Errorf("账期终点 = %s，期望 %s", got, tc.wantTo)
			}
		})
	}
}
