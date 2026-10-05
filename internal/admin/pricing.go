package admin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是定价版本、条件倍率规则与日历的管理动作。

// PublishPricingInput 是发布定价版本的输入。
type PublishPricingInput struct {
	MerchantID  uint64
	Model       string
	EffectiveAt time.Time
	Components  []store.PriceComponent
}

// PublishPricing 发布一个定价版本。
//
// 改价 = 新版本行 + 旧版本 retired_at 置位，不 update 旧价格；版本号分配与置位
// 由 store 在单个事务里完成。effective 留空时取当前时刻。
func (s *Service) PublishPricing(ctx context.Context, in PublishPricingInput) (*store.Pricing, error) {
	if err := requireID("定价 merchant", in.MerchantID); err != nil {
		return nil, err
	}
	if err := requireString("定价 model", in.Model); err != nil {
		return nil, err
	}
	if len(in.Components) == 0 {
		return nil, fmt.Errorf("admin: 定价至少需要一个 --component 分量")
	}
	for i, component := range in.Components {
		if err := billing.ValidateMetric(component.Metric); err != nil {
			return nil, fmt.Errorf("admin: 第 %d 个分量: %w", i+1, err)
		}
		if err := billing.ValidateUnitSettle(component.UnitSettle); err != nil {
			return nil, fmt.Errorf("admin: 第 %d 个分量: %w", i+1, err)
		}
		if err := requireDecimal("分量单价", component.UnitPrice); err != nil {
			return nil, err
		}
		if err := requireDecimal("分量基准量", component.BasisQty); err != nil {
			return nil, err
		}
	}
	effective := in.EffectiveAt
	if effective.IsZero() {
		effective = s.now()
	}
	return s.store.PublishPricing(ctx, in.MerchantID, in.Model, effective, in.Components)
}

// ListPricing 列出定价版本；merchantID 或 model 为空时不做该维度过滤。
func (s *Service) ListPricing(ctx context.Context, merchantID uint64, model string) ([]store.Pricing, error) {
	return s.store.ListPricing(ctx, merchantID, model)
}

// defaultRulePriority 是规则优先级的默认值，与列的 DEFAULT 100 一致。
const defaultRulePriority = 100

// 位掩码的合法上界：星期 7 位、日期性质 4 位。
const (
	maxWeekdayMask  = 0x7F
	maxDayKindMask  = 0x0F
	weekdayMaskBits = 7
)

// PriceRuleInput 是新增条件倍率规则的输入。
type PriceRuleInput struct {
	Scope       billing.Scope
	ScopeID     uint64
	Metric      billing.Metric
	Multiplier  string
	ValidFrom   *time.Time
	ValidTo     *time.Time
	TimeFrom    string
	TimeTo      string
	WeekdayMask *uint8
	DayKindMask *uint16
	Calendar    string
	Priority    int
}

// AddRule 新增一条条件倍率规则。
//
// 位掩码在进库前校验上界：超出位宽的取值会被数据库按列宽截断，截断后的规则会
// 匹配到设计之外的日期，必须在写入口拒绝。
func (s *Service) AddRule(ctx context.Context, in PriceRuleInput) (uint64, error) {
	if err := billing.ValidateScope(in.Scope); err != nil {
		return 0, err
	}
	if err := requireID("规则 scope-id", in.ScopeID); err != nil {
		return 0, err
	}
	if in.Metric != "" {
		if err := billing.ValidateMetric(in.Metric); err != nil {
			return 0, err
		}
	}
	if err := requirePositiveDecimal("规则 multiplier", in.Multiplier); err != nil {
		return 0, err
	}
	timeFrom, err := normalizeTimeOfDay(in.TimeFrom)
	if err != nil {
		return 0, err
	}
	timeTo, err := normalizeTimeOfDay(in.TimeTo)
	if err != nil {
		return 0, err
	}
	if in.WeekdayMask != nil && *in.WeekdayMask > maxWeekdayMask {
		return 0, fmt.Errorf("admin: 规则 weekday-mask 超出 %d 位，得到 %d", weekdayMaskBits, *in.WeekdayMask)
	}
	if in.DayKindMask != nil && *in.DayKindMask > maxDayKindMask {
		return 0, fmt.Errorf("admin: 规则 day-kind-mask 超出 4 位，得到 %d", *in.DayKindMask)
	}
	if in.ValidFrom != nil && in.ValidTo != nil && in.ValidTo.Before(*in.ValidFrom) {
		return 0, fmt.Errorf("admin: 规则 valid-to 早于 valid-from")
	}
	if in.Priority == 0 {
		in.Priority = defaultRulePriority
	}
	return s.store.InsertPriceRule(ctx, store.PriceRule{
		Scope:       in.Scope,
		ScopeID:     in.ScopeID,
		Metric:      in.Metric,
		Multiplier:  in.Multiplier,
		ValidFrom:   in.ValidFrom,
		ValidTo:     in.ValidTo,
		TimeFrom:    timeFrom,
		TimeTo:      timeTo,
		WeekdayMask: in.WeekdayMask,
		DayKindMask: in.DayKindMask,
		Calendar:    in.Calendar,
		Priority:    in.Priority,
	})
}

// ListRules 列出某个范围内的全部规则。
func (s *Service) ListRules(ctx context.Context, scope billing.Scope, scopeID uint64) ([]store.PriceRule, error) {
	if err := billing.ValidateScope(scope); err != nil {
		return nil, err
	}
	if err := requireID("规则 scope-id", scopeID); err != nil {
		return nil, err
	}
	return s.store.PriceRulesByScope(ctx, scope, scopeID)
}

// DeleteRule 按 id 删除一条规则。
func (s *Service) DeleteRule(ctx context.Context, id uint64) error {
	if err := requireID("规则 id", id); err != nil {
		return err
	}
	return s.store.DeletePriceRule(ctx, id)
}

// normalizeTimeOfDay 把 "HH:MM" 或 "HH:MM:SS" 归一为 "15:04:05"；空串保持为空。
func normalizeTimeOfDay(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	for _, layout := range []string{"15:04:05", "15:04"} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.Format("15:04:05"), nil
		}
	}
	return "", fmt.Errorf("admin: 时段 %q 必须形如 HH:MM 或 HH:MM:SS", value)
}

// ImportCalendar 把解析好的日历行 upsert 进指定日历。
//
// 空输入直接报错而不是静默成功：import 的语义是「导入一段放假通知」，
// 没有任何日期通常意味着文件路径或标准输入写错了。
func (s *Service) ImportCalendar(ctx context.Context, calendar string, days []store.CalendarDay) error {
	if err := requireString("日历名", calendar); err != nil {
		return err
	}
	if len(days) == 0 {
		return fmt.Errorf("admin: 没有可导入的日期行")
	}
	return s.store.UpsertCalendarDays(ctx, calendar, days)
}

// ListCalendarDays 列出某日历的全部日期。
func (s *Service) ListCalendarDays(ctx context.Context, calendar string) ([]store.CalendarDay, error) {
	if err := requireString("日历名", calendar); err != nil {
		return nil, err
	}
	return s.store.ListCalendarDays(ctx, calendar)
}
