package quota

import (
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
)

// TestWindowStart 覆盖各 window_kind × period 的窗口边界，含跨月与跨年周起点。
func TestWindowStart(t *testing.T) {
	tests := []struct {
		name   string
		kind   billing.WindowKind
		period billing.Period
		now    string
		want   string
		wantOK bool
	}{
		{name: "滚动 5 小时", kind: billing.WindowKindRolling, period: billing.Period5h,
			now: "2026-10-05 12:34:56", want: "2026-10-05 07:34:56", wantOK: true},
		{name: "日历日", kind: billing.WindowKindCalendar, period: billing.PeriodDay,
			now: "2026-10-05 23:59:59", want: "2026-10-05 00:00:00", wantOK: true},
		{name: "日历周跨界", kind: billing.WindowKindCalendar, period: billing.PeriodWeek,
			now: "2026-03-01 08:00:00", want: "2026-02-23 00:00:00", wantOK: true},
		{name: "日历周跨年", kind: billing.WindowKindCalendar, period: billing.PeriodWeek,
			now: "2026-01-01 08:00:00", want: "2025-12-29 00:00:00", wantOK: true},
		{name: "周一零点即起点", kind: billing.WindowKindCalendar, period: billing.PeriodWeek,
			now: "2026-02-23 00:00:00", want: "2026-02-23 00:00:00", wantOK: true},
		{name: "日历月", kind: billing.WindowKindCalendar, period: billing.PeriodMonth,
			now: "2026-10-31 23:59:59", want: "2026-10-01 00:00:00", wantOK: true},
		{name: "总量无下界", kind: billing.WindowKindRolling, period: billing.PeriodTotal,
			now: "2026-10-05 12:00:00", want: "0001-01-01 00:00:00", wantOK: true},
		{name: "滚动配日历周期非法", kind: billing.WindowKindRolling, period: billing.PeriodDay,
			now: "2026-10-05 12:00:00", wantOK: false},
		{name: "日历配 5h 非法", kind: billing.WindowKindCalendar, period: billing.Period5h,
			now: "2026-10-05 12:00:00", wantOK: false},
		{name: "未知周期非法", kind: billing.WindowKindRolling, period: billing.Period("2h"),
			now: "2026-10-05 12:00:00", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := WindowStart(tt.kind, tt.period, mustParse(t, tt.now))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v，期望 %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if want := mustParse(t, tt.want); !got.Equal(want) {
				t.Errorf("窗口起点 = %s，期望 %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

func TestValidWindow(t *testing.T) {
	valid := []struct {
		kind   billing.WindowKind
		period billing.Period
	}{
		{billing.WindowKindRolling, billing.Period5h},
		{billing.WindowKindCalendar, billing.PeriodDay},
		{billing.WindowKindCalendar, billing.PeriodWeek},
		{billing.WindowKindCalendar, billing.PeriodMonth},
		{billing.WindowKindRolling, billing.PeriodTotal},
		{billing.WindowKindCalendar, billing.PeriodTotal},
	}
	for _, tt := range valid {
		if err := ValidWindow(tt.kind, tt.period); err != nil {
			t.Errorf("组合 %s×%s 应当合法：%v", tt.kind, tt.period, err)
		}
	}
	invalid := []struct {
		kind   billing.WindowKind
		period billing.Period
	}{
		{billing.WindowKindRolling, billing.PeriodDay},
		{billing.WindowKindCalendar, billing.Period5h},
		{billing.WindowKind("sliding"), billing.Period5h},
		{billing.WindowKindRolling, billing.Period("2h")},
	}
	for _, tt := range invalid {
		if err := ValidWindow(tt.kind, tt.period); err == nil {
			t.Errorf("组合 %s×%s 应当被拒绝", tt.kind, tt.period)
		}
	}
}

// TestRetryAfter 覆盖 throttle 的退避时长：日历周期取到下一个边界，滚动周期取一个整周期。
func TestRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		kind   billing.WindowKind
		period billing.Period
		now    string
		want   time.Duration
	}{
		{name: "日周期到次日零点", kind: billing.WindowKindCalendar, period: billing.PeriodDay,
			now: "2026-10-05 23:00:00", want: time.Hour},
		{name: "周周期到下周一零点", kind: billing.WindowKindCalendar, period: billing.PeriodWeek,
			now: "2026-02-23 12:00:00", want: 6*24*time.Hour + 12*time.Hour},
		{name: "月周期到下月一日", kind: billing.WindowKindCalendar, period: billing.PeriodMonth,
			now: "2026-10-31 18:00:00", want: 6 * time.Hour},
		{name: "滚动 5 小时取整周期", kind: billing.WindowKindRolling, period: billing.Period5h,
			now: "2026-10-05 12:00:00", want: 5 * time.Hour},
		{name: "总量无自然重置", kind: billing.WindowKindRolling, period: billing.PeriodTotal,
			now: "2026-10-05 12:00:00", want: 0},
		{name: "非法组合无建议", kind: billing.WindowKindCalendar, period: billing.Period5h,
			now: "2026-10-05 12:00:00", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RetryAfter(tt.kind, tt.period, mustParse(t, tt.now)); got != tt.want {
				t.Errorf("RetryAfter = %s，期望 %s", got, tt.want)
			}
		})
	}
}

func mustParse(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.UTC)
	if err != nil {
		t.Fatalf("解析时间 %q 失败：%v", value, err)
	}
	return parsed
}
