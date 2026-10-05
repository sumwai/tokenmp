package billing

import "testing"

// TestKnownEnums 验证每个白名单取值都被 Known 与 Validate 接受。
func TestKnownEnums(t *testing.T) {
	t.Run("Metric", func(t *testing.T) {
		for _, v := range []Metric{
			MetricInputToken,
			MetricOutputToken,
			MetricCacheReadToken,
			MetricCacheWriteToken,
			MetricCacheWrite5m,
			MetricCacheWrite1h,
			MetricReasoningToken,
			MetricRequest,
		} {
			if !v.Known() {
				t.Errorf("Metric %q 应在白名单内", v)
			}
			if err := ValidateMetric(v); err != nil {
				t.Errorf("Metric %q 校验失败：%v", v, err)
			}
		}
	})

	t.Run("UnitSettle", func(t *testing.T) {
		for _, v := range []UnitSettle{UnitSettleCurrency, UnitSettleToken, UnitSettleCredit} {
			if !v.Known() {
				t.Errorf("UnitSettle %q 应在白名单内", v)
			}
			if err := ValidateUnitSettle(v); err != nil {
				t.Errorf("UnitSettle %q 校验失败：%v", v, err)
			}
		}
	})

	t.Run("WindowKind", func(t *testing.T) {
		for _, v := range []WindowKind{WindowKindRolling, WindowKindCalendar} {
			if !v.Known() {
				t.Errorf("WindowKind %q 应在白名单内", v)
			}
			if err := ValidateWindowKind(v); err != nil {
				t.Errorf("WindowKind %q 校验失败：%v", v, err)
			}
		}
	})

	t.Run("Period", func(t *testing.T) {
		for _, v := range []Period{Period5h, PeriodDay, PeriodWeek, PeriodMonth, PeriodTotal} {
			if !v.Known() {
				t.Errorf("Period %q 应在白名单内", v)
			}
			if err := ValidatePeriod(v); err != nil {
				t.Errorf("Period %q 校验失败：%v", v, err)
			}
		}
	})

	t.Run("DayKind", func(t *testing.T) {
		for _, v := range []DayKind{DayKindWorkday, DayKindWeekend, DayKindHoliday, DayKindMakeupWorkday} {
			if !v.Known() {
				t.Errorf("DayKind %q 应在白名单内", v)
			}
			if err := ValidateDayKind(v); err != nil {
				t.Errorf("DayKind %q 校验失败：%v", v, err)
			}
		}
	})

	t.Run("Scope", func(t *testing.T) {
		for _, v := range []Scope{ScopePricing, ScopeModelMap, ScopePlan, ScopeAccount, ScopeAPIKey, ScopeChannel} {
			if !v.Known() {
				t.Errorf("Scope %q 应在白名单内", v)
			}
			if err := ValidateScope(v); err != nil {
				t.Errorf("Scope %q 校验失败：%v", v, err)
			}
		}
	})

	t.Run("Action", func(t *testing.T) {
		for _, v := range []Action{ActionReject, ActionThrottle} {
			if !v.Known() {
				t.Errorf("Action %q 应在白名单内", v)
			}
			if err := ValidateAction(v); err != nil {
				t.Errorf("Action %q 校验失败：%v", v, err)
			}
		}
	})

	t.Run("Fallback", func(t *testing.T) {
		for _, v := range []Fallback{FallbackChargeBalance, FallbackReject} {
			if !v.Known() {
				t.Errorf("Fallback %q 应在白名单内", v)
			}
			if err := ValidateFallback(v); err != nil {
				t.Errorf("Fallback %q 校验失败：%v", v, err)
			}
		}
	})

	t.Run("QuotaEvent", func(t *testing.T) {
		if !QuotaEventReset.Known() {
			t.Errorf("QuotaEvent %q 应在白名单内", QuotaEventReset)
		}
		if err := ValidateQuotaEvent(QuotaEventReset); err != nil {
			t.Errorf("QuotaEvent %q 校验失败：%v", QuotaEventReset, err)
		}
	})
}

// TestValidateRejectsUnknown 验证写入方向严格：未知值必须被拒绝。
func TestValidateRejectsUnknown(t *testing.T) {
	tests := []struct {
		name     string
		validate func() error
	}{
		{name: "未知 Metric", validate: func() error { return ValidateMetric(Metric("not_a_metric")) }},
		{name: "空 Metric", validate: func() error { return ValidateMetric(Metric("")) }},
		{name: "未知 UnitSettle", validate: func() error { return ValidateUnitSettle(UnitSettle("usd")) }},
		{name: "未知 WindowKind", validate: func() error { return ValidateWindowKind(WindowKind("sliding")) }},
		{name: "未知 Period", validate: func() error { return ValidatePeriod(Period("year")) }},
		{name: "未知 DayKind", validate: func() error { return ValidateDayKind(DayKind("vacation")) }},
		{name: "未知 Scope", validate: func() error { return ValidateScope(Scope("tenant")) }},
		{name: "未知 Action", validate: func() error { return ValidateAction(Action("warn")) }},
		{name: "未知 Fallback", validate: func() error { return ValidateFallback(Fallback("retry")) }},
		{name: "未知 QuotaEvent", validate: func() error { return ValidateQuotaEvent(QuotaEvent("grant")) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.validate(); err == nil {
				t.Error("未知值应当被拒绝")
			}
		})
	}
}

// TestFromDBIsLenient 验证读取方向宽容：未知值原样返回，不报错也不被改写。
//
// 版本回滚后旧代码会遇到新版本写入的取值，读取报错会把一条不认识的记录
// 放大成整批查询失败，因此这里只做类型转换。
func TestFromDBIsLenient(t *testing.T) {
	if got := MetricFromDB("cache_write_2h"); got != Metric("cache_write_2h") {
		t.Errorf("MetricFromDB 应原样返回，得到 %q", got)
	}
	if got := UnitSettleFromDB("usd"); got != UnitSettle("usd") {
		t.Errorf("UnitSettleFromDB 应原样返回，得到 %q", got)
	}
	if got := WindowKindFromDB("sliding"); got != WindowKind("sliding") {
		t.Errorf("WindowKindFromDB 应原样返回，得到 %q", got)
	}
	if got := PeriodFromDB("year"); got != Period("year") {
		t.Errorf("PeriodFromDB 应原样返回，得到 %q", got)
	}
	if got := DayKindFromDB("vacation"); got != DayKind("vacation") {
		t.Errorf("DayKindFromDB 应原样返回，得到 %q", got)
	}
	if got := ScopeFromDB("tenant"); got != Scope("tenant") {
		t.Errorf("ScopeFromDB 应原样返回，得到 %q", got)
	}
	if got := ActionFromDB("warn"); got != Action("warn") {
		t.Errorf("ActionFromDB 应原样返回，得到 %q", got)
	}
	if got := FallbackFromDB("retry"); got != Fallback("retry") {
		t.Errorf("FallbackFromDB 应原样返回，得到 %q", got)
	}
	if got := QuotaEventFromDB("grant"); got != QuotaEvent("grant") {
		t.Errorf("QuotaEventFromDB 应原样返回，得到 %q", got)
	}
	if got := MetricFromDB("input_token"); got != MetricInputToken {
		t.Errorf("MetricFromDB 已知值应等于常量，得到 %q", got)
	}
}

// TestDayKindBit 验证 day_kind 与位掩码的映射稳定，且未知性质不静默给位。
func TestDayKindBit(t *testing.T) {
	tests := []struct {
		kind DayKind
		want uint16
	}{
		{kind: DayKindWorkday, want: DayKindBitWorkday},
		{kind: DayKindWeekend, want: DayKindBitWeekend},
		{kind: DayKindHoliday, want: DayKindBitHoliday},
		{kind: DayKindMakeupWorkday, want: DayKindBitMakeupWorkday},
	}
	seen := make(map[uint16]DayKind, len(tests))
	for _, tt := range tests {
		got, err := tt.kind.Bit()
		if err != nil {
			t.Fatalf("DayKind %q 应有位掩码：%v", tt.kind, err)
		}
		if got != tt.want {
			t.Errorf("DayKind %q 的位 = %d，期望 %d", tt.kind, got, tt.want)
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("位 %d 被 %q 与 %q 共用，规则掩码会失去区分度", got, prev, tt.kind)
		}
		seen[got] = tt.kind
	}

	if _, err := DayKind("vacation").Bit(); err == nil {
		t.Error("未知日期性质不应静默给位")
	}
}
