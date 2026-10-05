package admin

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件收敛管理面的文本解析与脱敏。
//
// 两者都属「展示与输入口径」，放在服务层而不是 cmd：脱敏必须与 secret 同处一个包，
// 才能保证列出的每一个入口都走同一条规则；组件规格与日历行的语法是管理面的输入契约，
// 放在这里可以被单测直接覆盖。

// visiblePrefixLen 是脱敏后保留的明文字符数。
//
// 前缀足以让人在列表里区分「换的是哪一条凭据」，又不足以还原明文；
// 长度不足该值的取值整体打码，避免短 secret 被完整展示。
const visiblePrefixLen = 8

// maskedFull 是无法展示前缀时的整段掩码。
const maskedFull = "********"

// credentialSecret 是 upstream_credential.secret 里管理面认识的字段。
type credentialSecret struct {
	APIKey string `json:"api_key"`
}

// maskSecret 从凭据 JSON 里取出 api_key 并脱敏成前缀。
//
// 解析失败时返回固定掩码而不是原文：宁可看不出这条凭据是什么，也不能把
// 结构不确定的 JSON 片段当明文漏出去。
func maskSecret(secret []byte) string {
	var parsed credentialSecret
	if err := json.Unmarshal(secret, &parsed); err != nil {
		return maskedFull
	}
	return maskPrefix(parsed.APIKey)
}

// maskPrefix 保留取值的前 visiblePrefixLen 个字符，其余以省略号代替。
func maskPrefix(value string) string {
	if strings.TrimSpace(value) == "" {
		return maskedFull
	}
	if len(value) <= visiblePrefixLen {
		return maskedFull
	}
	return value[:visiblePrefixLen] + "…"
}

// CredentialView 是凭据列表的一行：只含前缀，不含 secret。
type CredentialView struct {
	ID         uint64 `json:"id"`
	MerchantID uint64 `json:"merchant_id"`
	CredGroup  string `json:"cred_group"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	Enabled    bool   `json:"enabled"`
}

// parseDecimal 把定点小数字符串解析为 decimal。
func parseDecimal(raw string) (decimal.Decimal, error) {
	return decimal.NewFromString(strings.TrimSpace(raw))
}

// componentSpecParts 是组件规格的字段个数：metric:price:unit_settle:qty。
const componentSpecParts = 4

// ParseComponentSpec 解析一条 `--component metric:price:unit_settle:qty` 规格。
//
// metric 与 unit_settle 复用 billing 白名单；price 与 qty 只校验可解析，
// 数值口径（除零、负价）由定价结算函数按业务语义处理。
func ParseComponentSpec(spec string) (store.PriceComponent, error) {
	parts := strings.Split(spec, ":")
	if len(parts) != componentSpecParts {
		return store.PriceComponent{}, fmt.Errorf("admin: 组件规格 %q 必须形如 metric:price:unit_settle:qty", spec)
	}
	metric := billing.Metric(strings.TrimSpace(parts[0]))
	if err := billing.ValidateMetric(metric); err != nil {
		return store.PriceComponent{}, err
	}
	unitSettle := billing.UnitSettle(strings.TrimSpace(parts[2]))
	if err := billing.ValidateUnitSettle(unitSettle); err != nil {
		return store.PriceComponent{}, err
	}
	price := strings.TrimSpace(parts[1])
	if err := requireDecimal("组件单价", price); err != nil {
		return store.PriceComponent{}, err
	}
	basisQty := strings.TrimSpace(parts[3])
	if err := requireDecimal("组件基准量", basisQty); err != nil {
		return store.PriceComponent{}, err
	}
	return store.PriceComponent{
		Metric:     metric,
		UnitSettle: unitSettle,
		UnitPrice:  price,
		BasisQty:   basisQty,
	}, nil
}

// calendarLineParts 是日历行的字段个数：日期,day_kind。
const calendarLineParts = 2

// calendarDateLayout 是日历行的日期格式，与 sys_calendar 的 DATE 列一致。
const calendarDateLayout = "2006-01-02"

// ParseCalendarImport 解析 `日期,day_kind` 行文本。
//
// 空行与以 # 开头的注释行跳过；行号写进错误信息，方便定位写坏的那一行。
func ParseCalendarImport(text string) ([]store.CalendarDay, error) {
	var days []store.CalendarDay
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) != calendarLineParts {
			return nil, fmt.Errorf("admin: 第 %d 行必须形如 日期,day_kind: %q", i+1, line)
		}
		date := strings.TrimSpace(parts[0])
		if _, err := time.Parse(calendarDateLayout, date); err != nil {
			return nil, fmt.Errorf("admin: 第 %d 行日期 %q 不是合法日期", i+1, date)
		}
		kind := billing.DayKind(strings.TrimSpace(parts[1]))
		if err := billing.ValidateDayKind(kind); err != nil {
			return nil, fmt.Errorf("admin: 第 %d 行: %w", i+1, err)
		}
		days = append(days, store.CalendarDay{Date: date, DayKind: kind})
	}
	return days, nil
}
