package main

import (
	"context"
	"io"
	"os"
	"strconv"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是 `admin price`、`admin rule` 与 `admin calendar` 的命令层。

func adminPrice(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("price 需要动作：publish / list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionPublish:
		return adminPricePublish(ctx, rest, env)
	case actionList:
		return adminPriceList(ctx, rest, env)
	default:
		return env.usageErrorf("price 未知动作 %q", action)
	}
}

func adminPricePublish(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin price publish")
	merchant := fs.Uint64(flagMerchant, 0, "归属商家 id")
	model := fs.String(flagModel, "", "模型名")
	effective := fs.String(flagEffective, "", "生效时刻；不填取当前时刻")
	var componentSpecs stringList
	fs.Var(&componentSpecs, flagComponent, "计价分量 metric:price:unit_settle:qty，可重复")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if len(componentSpecs) == 0 {
		return env.usageError("price publish 至少需要一个 --component")
	}
	components := make([]store.PriceComponent, 0, len(componentSpecs))
	for _, spec := range componentSpecs {
		component, err := admin.ParseComponentSpec(spec)
		if err != nil {
			return env.usageError(err.Error())
		}
		components = append(components, component)
	}
	effectiveAt, err := parseAdminTime(*effective)
	if err != nil {
		return env.usageError(err.Error())
	}
	pricing, err := env.service.PublishPricing(ctx, admin.PublishPricingInput{
		MerchantID:  *merchant,
		Model:       *model,
		EffectiveAt: effectiveAt,
		Components:  components,
	})
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已发布定价 id=%d model=%s version=%d\n", pricing.ID, pricing.Model, pricing.Version)
}

func adminPriceList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin price list")
	merchant := fs.Uint64(flagMerchant, 0, "商家 id；不填列出全部")
	model := fs.String(flagModel, "", "模型名；不填列出全部")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	versions, err := env.service.ListPricing(ctx, *merchant, *model)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(versions))
	for _, p := range versions {
		status := "active"
		if p.RetiredAt != nil {
			status = "retired"
		}
		rows = append(rows, []string{
			strconv.FormatUint(p.ID, 10), strconv.FormatUint(p.MerchantID, 10), p.Model,
			strconv.Itoa(p.Version), formatTime(p.EffectiveAt), formatTimePtr(p.RetiredAt), status,
		})
	}
	return env.emit(*asJSON,
		[]string{flagID, flagMerchant, flagModel, "version", "effective_at", "retired_at", headerStatus},
		rows, versions)
}

func adminRule(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("rule 需要动作：add / list / del")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionAdd:
		return adminRuleAdd(ctx, rest, env)
	case actionList:
		return adminRuleList(ctx, rest, env)
	case actionDel:
		return adminRuleDel(ctx, rest, env)
	default:
		return env.usageErrorf("rule 未知动作 %q", action)
	}
}

// unsetMask 是位掩码 flag 的「未提供」哨兵：掩码非负，-1 不会与合法取值冲突。
// 上界与 service 的校验一致：星期 7 位、日期性质 4 位。
const (
	unsetMask           = -1
	maxWeekdayMaskValue = 0x7F
	maxDayKindMaskValue = 0x0F
)

func adminRuleAdd(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin rule add")
	scope := fs.String(flagScope, "", "范围：pricing | model_map | plan | account")
	scopeID := fs.Uint64(flagScopeID, 0, "范围实体 id")
	metric := fs.String(flagMetric, "", "限定指标；不填表示全部指标")
	multiplier := fs.String(flagMultiplier, "", "倍率")
	validFrom := fs.String(flagValidFrom, "", "有效期起点")
	validTo := fs.String(flagValidTo, "", "有效期终点")
	timeFrom := fs.String(flagTimeFrom, "", "时段起点 HH:MM")
	timeTo := fs.String(flagTimeTo, "", "时段终点 HH:MM")
	weekdayMask := fs.Int(flagWeekdayMask, unsetMask, "星期位掩码 bit0=周一…bit6=周日")
	dayKindMask := fs.Int(flagDayKindMask, unsetMask, "日期性质位掩码 1=workday 2=weekend 4=holiday 8=makeup_workday")
	calendar := fs.String(flagCalendar, "", "日历来源")
	priority := fs.Int(flagPriority, 0, "优先级，默认 100")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	ruleScope := billing.Scope(*scope)
	if err := billing.ValidateScope(ruleScope); err != nil {
		return env.usageError(err.Error())
	}
	input := admin.PriceRuleInput{
		Scope:      ruleScope,
		ScopeID:    *scopeID,
		Multiplier: *multiplier,
		TimeFrom:   *timeFrom,
		TimeTo:     *timeTo,
		Calendar:   *calendar,
		Priority:   *priority,
	}
	if *metric != "" {
		parsedMetric := billing.Metric(*metric)
		if err := billing.ValidateMetric(parsedMetric); err != nil {
			return env.usageError(err.Error())
		}
		input.Metric = parsedMetric
	}
	from, err := parseAdminTime(*validFrom)
	if err != nil {
		return env.usageError(err.Error())
	}
	if !from.IsZero() {
		input.ValidFrom = &from
	}
	to, err := parseAdminTime(*validTo)
	if err != nil {
		return env.usageError(err.Error())
	}
	if !to.IsZero() {
		input.ValidTo = &to
	}
	if *weekdayMask != unsetMask {
		if *weekdayMask < 0 || *weekdayMask > maxWeekdayMaskValue {
			return env.usageErrorf("rule weekday-mask 超出 7 位，得到 %d", *weekdayMask)
		}
		//nolint:gosec // G115：已在上方限定 0..127，转换不丢位。
		mask := uint8(*weekdayMask)
		input.WeekdayMask = &mask
	}
	if *dayKindMask != unsetMask {
		if *dayKindMask < 0 || *dayKindMask > maxDayKindMaskValue {
			return env.usageErrorf("rule day-kind-mask 超出 4 位，得到 %d", *dayKindMask)
		}
		//nolint:gosec // G115：已在上方限定 0..15，转换不丢位。
		mask := uint16(*dayKindMask)
		input.DayKindMask = &mask
	}
	id, err := env.service.AddRule(ctx, input)
	if err != nil {
		return env.fail(err)
	}
	return env.printf("已写入规则 id=%d\n", id)
}

func adminRuleList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin rule list")
	scope := fs.String(flagScope, "", "范围：pricing | model_map | plan | account")
	scopeID := fs.Uint64(flagScopeID, 0, "范围实体 id")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	ruleScope := billing.Scope(*scope)
	if err := billing.ValidateScope(ruleScope); err != nil {
		return env.usageError(err.Error())
	}
	rules, err := env.service.ListRules(ctx, ruleScope, *scopeID)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(rules))
	for _, r := range rules {
		rows = append(rows, []string{
			strconv.FormatUint(r.ID, 10), string(r.Scope), strconv.FormatUint(r.ScopeID, 10),
			string(r.Metric), r.Multiplier, formatTimePtr(r.ValidFrom), formatTimePtr(r.ValidTo),
			r.TimeFrom, r.TimeTo, formatUint8Ptr(r.WeekdayMask), formatUint16Ptr(r.DayKindMask),
			r.Calendar, strconv.Itoa(r.Priority),
		})
	}
	return env.emit(*asJSON,
		[]string{flagID, flagScope, flagScopeID, flagMetric, flagMultiplier, flagValidFrom, flagValidTo, flagTimeFrom, flagTimeTo, "weekday_mask", "day_kind_mask", flagCalendar, flagPriority},
		rows, rules)
}

func adminRuleDel(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin rule del")
	id := fs.Uint64(flagID, 0, "规则 id")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if err := env.service.DeleteRule(ctx, *id); err != nil {
		return env.fail(err)
	}
	return env.printf("已删除规则 id=%d\n", *id)
}

func adminCalendar(ctx context.Context, args []string, env *adminEnv) int {
	if len(args) == 0 {
		return env.usageError("calendar 需要动作：import / list")
	}
	action, rest := args[0], args[1:]
	switch action {
	case actionImport:
		return adminCalendarImport(ctx, rest, env)
	case actionList:
		return adminCalendarList(ctx, rest, env)
	default:
		return env.usageErrorf("calendar 未知动作 %q", action)
	}
}

func adminCalendarImport(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin calendar import")
	calendar := fs.String(flagCalendar, "", "日历名，如 cn")
	file := fs.String(flagFile, "", "输入文件；不填从标准输入读")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *calendar == "" {
		return env.usageError("calendar import 需要 --calendar")
	}
	reader := env.stdin
	if *file != "" {
		opened, err := os.Open(*file)
		if err != nil {
			return env.fail(err)
		}
		defer func() { _ = opened.Close() }()
		reader = opened
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return env.fail(err)
	}
	days, err := admin.ParseCalendarImport(string(raw))
	if err != nil {
		return env.usageError(err.Error())
	}
	if err := env.service.ImportCalendar(ctx, *calendar, days); err != nil {
		return env.fail(err)
	}
	return env.printf("已导入日历 %s 共 %d 天\n", *calendar, len(days))
}

func adminCalendarList(ctx context.Context, args []string, env *adminEnv) int {
	fs := env.newFlagSet("admin calendar list")
	calendar := fs.String(flagCalendar, "", "日历名")
	asJSON := fs.Bool(flagJSON, false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	days, err := env.service.ListCalendarDays(ctx, *calendar)
	if err != nil {
		return env.fail(err)
	}
	rows := make([][]string, 0, len(days))
	for _, d := range days {
		rows = append(rows, []string{d.Date, string(d.DayKind)})
	}
	return env.emit(*asJSON, []string{"date", "day_kind"}, rows, days)
}

// formatUint8Ptr 格式化可空位掩码。
func formatUint8Ptr(value *uint8) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatUint(uint64(*value), 10)
}

// formatUint16Ptr 格式化可空位掩码。
func formatUint16Ptr(value *uint16) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatUint(uint64(*value), 10)
}
