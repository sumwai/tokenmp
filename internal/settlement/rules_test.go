package settlement

import (
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
)

// ptr8 与 ptr16 让掩码字段可以写成字面量。
func ptr8(v uint8) *uint8    { return &v }
func ptr16(v uint16) *uint16 { return &v }

// mondayMorning 是 2026-10-05 10:00，周一。
var mondayMorning = time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)

// mondayLateNight 是同日 23:30，用于跨零点时段。
var mondayLateNight = time.Date(2026, 10, 5, 23, 30, 0, 0, time.UTC)

func TestMatchRuleFilters(t *testing.T) {
	usage := map[billing.Metric]int{
		billing.MetricInputToken:  100,
		billing.MetricOutputToken: 50,
	}
	tests := []struct {
		name  string
		rule  Rule
		usage map[billing.Metric]int
		want  bool
	}{
		{name: "不限指标", rule: Rule{Metric: ""}, usage: usage, want: true},
		{name: "指标命中", rule: Rule{Metric: billing.MetricOutputToken}, usage: usage, want: true},
		{name: "指标未出现", rule: Rule{Metric: billing.MetricCacheReadToken}, usage: usage, want: false},
		{name: "有效期已过", rule: Rule{ValidTo: timePtr(mondayMorning)}, usage: usage, want: false},
		{name: "有效期未到", rule: Rule{ValidFrom: timePtr(mondayMorning.Add(time.Hour))}, usage: usage, want: false},
		{name: "时段内", rule: Rule{TimeFrom: "09:00:00", TimeTo: "12:00:00"}, usage: usage, want: true},
		{name: "时段外", rule: Rule{TimeFrom: "12:00:00", TimeTo: "18:00:00"}, usage: usage, want: false},
		{name: "星期命中", rule: Rule{WeekdayMask: ptr8(1)}, usage: usage, want: true},
		{name: "星期不符", rule: Rule{WeekdayMask: ptr8(1 << 5)}, usage: usage, want: false},
		{name: "日历缺数据按工作日", rule: Rule{DayKindMask: ptr16(billing.DayKindBitWorkday), Calendar: "cn"}, usage: usage, want: true},
		{name: "日历缺数据不匹配周末", rule: Rule{DayKindMask: ptr16(billing.DayKindBitWeekend), Calendar: "cn"}, usage: usage, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := MatchRule([]Rule{tt.rule}, tt.usage, mondayMorning, nil)
			if ok != tt.want {
				t.Errorf("命中 = %v，期望 %v", ok, tt.want)
			}
		})
	}
}

func TestMatchRulePicksHighestPriority(t *testing.T) {
	rules := []Rule{
		{ID: 1, Priority: 100, Multiplier: dec(t, "2")},
		{ID: 2, Priority: 200, Multiplier: dec(t, "3")},
		{ID: 3, Priority: 200, Multiplier: dec(t, "4")},
	}
	got, ok := MatchRule(rules, nil, mondayMorning, nil)
	if !ok {
		t.Fatal("应当命中")
	}
	// 同优先级取 id 小的那条，保证结果稳定。
	if got.ID != 2 {
		t.Errorf("命中规则 id = %d，期望 2", got.ID)
	}
}

func TestMatchRuleCrossMidnightWindow(t *testing.T) {
	rule := Rule{TimeFrom: "22:00:00", TimeTo: "06:00:00"}
	if _, ok := MatchRule([]Rule{rule}, nil, mondayLateNight, nil); !ok {
		t.Errorf("23:30 应落在跨零点窗口内")
	}
	noon := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if _, ok := MatchRule([]Rule{rule}, nil, noon, nil); ok {
		t.Errorf("12:00 不应落在 22:00–06:00 窗口内")
	}
}

func TestMatchRuleUsesCalendarDayKind(t *testing.T) {
	rule := Rule{DayKindMask: ptr16(billing.DayKindBitHoliday), Calendar: "cn"}
	dayKinds := map[string]billing.DayKind{"cn": billing.DayKindHoliday}
	if _, ok := MatchRule([]Rule{rule}, nil, mondayMorning, dayKinds); !ok {
		t.Errorf("当日为法定节假日时应命中")
	}
	if _, ok := MatchRule([]Rule{rule}, nil, mondayMorning, nil); ok {
		t.Errorf("日历无数据时按工作日处理，不应命中节假日规则")
	}
}

func TestChainMultiplier(t *testing.T) {
	rules := []Rule{
		{Multiplier: dec(t, "1.5")},
		{Multiplier: dec(t, "0.5")},
	}
	got := ChainMultiplier(dec(t, "1.2"), dec(t, "2"), rules)
	// 1.2 × 2 × 1.5 × 0.5 = 1.8
	if got.String() != "1.8" {
		t.Errorf("倍率 = %s，期望 1.8", got)
	}
}

// timePtr 返回时间指针，供可空字段使用。
func timePtr(at time.Time) *time.Time { return &at }
