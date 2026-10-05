package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件把定价解析、折算、倍率链与扣减序串成一次结算。
//
// 编排放在本包而不是 cmd：装配层只提供「事务与查询」的实现，结算步骤的顺序与
// 失败语义属于计费规则的一部分，放这里才能被单测直接覆盖。

// Repo 是结算依赖的数据面；实现负责事务边界。
type Repo interface {
	// InTx 在单个事务内执行 fn。fn 返回错误时回滚并返回该错误。
	InTx(ctx context.Context, fn func(context.Context, Tx) error) error
	// AccountBuckets 读账户的全部账本行，用于转发前的额度预检（不加锁）。
	AccountBuckets(ctx context.Context, accountID uint64) ([]Bucket, error)
}

// Tx 是同一事务内的最小数据访问面。
//
// 账户锁定是结算的第一步：同一账户的并发请求由 here 的行锁串行化，
// 防并发穿透（两个请求同时读到同一份 remaining 各扣一次）。
type Tx interface {
	// LockAccount 锁定账户行并返回账户倍率。
	LockAccount(ctx context.Context, accountID uint64) (decimal.Decimal, error)
	// ActivePricing 返回生效定价；没有生效版本时返回 ErrNoPricing。
	ActivePricing(ctx context.Context, merchantID uint64, model string, asOf time.Time) (*Pricing, error)
	// PriceRulesByScope 返回某 scope 下的规则。
	PriceRulesByScope(ctx context.Context, scope billing.Scope, scopeID uint64) ([]Rule, error)
	// ModelMapMultiplier 返回渠道模型映射的倍率；没有映射时返回 1。
	ModelMapMultiplier(ctx context.Context, channelID uint64, model string) (decimal.Decimal, error)
	// CalendarDay 返回日历某日的性质；无该日数据时 found 为 false。
	CalendarDay(ctx context.Context, calendar, date string) (billing.DayKind, bool, error)
	// LockBuckets 锁定并返回账户的全部账本行。
	LockBuckets(ctx context.Context, accountID uint64) ([]Bucket, error)
	// InsertUsage 写一条结算流水。
	InsertUsage(ctx context.Context, row Usage) (uint64, error)
	// UpdateBucketRemaining 更新账本存量。
	UpdateBucketRemaining(ctx context.Context, bucketID uint64, remaining decimal.Decimal) error
}

// Service 执行结算。
type Service struct {
	repo Repo
	logf func(msg string, args ...any)
	now  func() time.Time
}

// New 构造结算器；logf 为 nil 时不记日志。
func New(repo Repo, logf func(msg string, args ...any)) *Service {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Service{repo: repo, logf: logf, now: time.Now}
}

// scopeRef 是一个规则作用的范围与实体 id。
type scopeRef struct {
	scope billing.Scope
	id    uint64
}

// Settle 结算一次请求：解析定价与倍率、折算用量、扣减账本并写入流水。
//
// 全部动作在同一个事务里完成；返回错误时调用方按「结算失败」处理，落占位流水，
// 不阻断已经写给客户端的响应。
func (s *Service) Settle(ctx context.Context, in Input) error {
	if err := in.validate(); err != nil {
		return err
	}
	asOf := in.AsOf
	if asOf.IsZero() {
		asOf = s.now()
	}
	return s.repo.InTx(ctx, func(ctx context.Context, tx Tx) error {
		return s.settle(ctx, tx, in, asOf)
	})
}

// settle 在事务内完成一次结算。
func (s *Service) settle(ctx context.Context, tx Tx, in Input, asOf time.Time) error {
	accountMultiplier, err := tx.LockAccount(ctx, in.AccountID)
	if err != nil {
		return err
	}
	pricing, err := tx.ActivePricing(ctx, in.MerchantID, in.Model, asOf)
	if errors.Is(err, ErrNoPricing) {
		return s.insertUnpriced(ctx, tx, in)
	}
	if err != nil {
		return err
	}

	charges, hitComponents := ResolveCharges(in.Usage, pricing.Components)
	gross := GrossAmount(charges)

	channelMultiplier, err := tx.ModelMapMultiplier(ctx, in.ChannelID, in.RequestedModel)
	if err != nil {
		return err
	}

	scopes := []scopeRef{
		{scope: billing.ScopePricing, id: pricing.ID},
		{scope: billing.ScopeModelMap, id: in.ChannelID},
		{scope: billing.ScopeAccount, id: in.AccountID},
	}
	allRules, err := loadRules(ctx, tx, scopes)
	if err != nil {
		return err
	}
	dayKinds, err := s.loadDayKinds(ctx, tx, allRules, asOf)
	if err != nil {
		return err
	}
	matched := make([]Rule, 0, len(scopes))
	for _, ref := range scopes {
		if rule, ok := MatchRule(rulesFor(allRules, ref), in.Usage, asOf, dayKinds); ok {
			matched = append(matched, rule)
		}
	}
	multiplier := ChainMultiplier(channelMultiplier, accountMultiplier, matched)

	buckets, err := tx.LockBuckets(ctx, in.AccountID)
	if err != nil {
		return err
	}
	plan := PlanDeduction(NeedAfterMultiplier(charges, multiplier), buckets, asOf)

	snapshot, err := buildSnapshot(pricing, hitComponents, matched, channelMultiplier, accountMultiplier, asOf)
	if err != nil {
		return err
	}
	settlementPayload, err := buildSettlement(plan)
	if err != nil {
		return err
	}

	if _, err := tx.InsertUsage(ctx, Usage{
		MerchantID:      in.MerchantID,
		AccountID:       in.AccountID,
		ChannelID:       in.ChannelID,
		Model:           in.Model,
		Metrics:         in.Usage,
		PricingID:       pricing.ID,
		PricingSnapshot: snapshot,
		GrossAmount:     gross,
		Multiplier:      multiplier,
		Settlement:      settlementPayload,
	}); err != nil {
		return err
	}
	for _, bucketID := range sortedKeys(plan.Updates) {
		if err := tx.UpdateBucketRemaining(ctx, bucketID, plan.Updates[bucketID]); err != nil {
			return err
		}
	}
	if len(plan.Shortfall) > 0 {
		s.logf("结算欠额，未追偿",
			"request_id", in.RequestID, "account_id", in.AccountID, "shortfall", shortfallFields(plan.Shortfall))
	}
	return nil
}

// insertUnpriced 在无生效定价时落一条占位流水。
//
// 记 warning 但不阻断：新模型未上价不该让请求被拒绝，流水仍要留下这次转发发生过的事实。
func (s *Service) insertUnpriced(ctx context.Context, tx Tx, in Input) error {
	s.logf("没有生效定价，按占位口径落流水",
		"request_id", in.RequestID, "merchant_id", in.MerchantID, "model", in.Model)
	_, err := tx.InsertUsage(ctx, Usage{
		MerchantID: in.MerchantID,
		AccountID:  in.AccountID,
		ChannelID:  in.ChannelID,
		Model:      in.Model,
		Metrics:    in.Usage,
		Multiplier: decimal.NewFromInt(1),
	})
	return err
}

// loadDayKinds 预取规则涉及的日历在当日的性质。
//
// 匹配函数保持纯函数：它只查表，不发数据库请求，日历数据由这里一次性取齐。
func (s *Service) loadDayKinds(ctx context.Context, tx Tx, rules []Rule, asOf time.Time) (map[string]billing.DayKind, error) {
	date := asOf.Format(dateLayout)
	kinds := make(map[string]billing.DayKind)
	for _, rule := range rules {
		if rule.DayKindMask == nil || rule.Calendar == "" {
			continue
		}
		if _, ok := kinds[rule.Calendar]; ok {
			continue
		}
		kind, found, err := tx.CalendarDay(ctx, rule.Calendar, date)
		if err != nil {
			return nil, err
		}
		if !found {
			kind = billing.DayKindWorkday
		}
		kinds[rule.Calendar] = kind
	}
	return kinds, nil
}

// loadRules 取全部 scope 下的规则。
//
// 单独成函数避免在调用处 `rules, err :=` 遮蔽外层的 err，也让「取哪几个 scope」
// 的清单集中在 settle 里。
func loadRules(ctx context.Context, tx Tx, scopes []scopeRef) ([]Rule, error) {
	var all []Rule
	for _, ref := range scopes {
		rules, err := tx.PriceRulesByScope(ctx, ref.scope, ref.id)
		if err != nil {
			return nil, err
		}
		all = append(all, rules...)
	}
	return all, nil
}

// rulesFor 从规则池里取出某个 scope 与实体 id 下的规则。
func rulesFor(all []Rule, ref scopeRef) []Rule {
	var out []Rule
	for _, rule := range all {
		if rule.Scope == ref.scope && rule.ScopeID == ref.id {
			out = append(out, rule)
		}
	}
	return out
}

// sortedKeys 返回更新项的账本 id，升序。
//
// 遍历 map 的顺序不确定，落库顺序会影响测试与排查；排序让同一份输入每次得到同样的写库顺序。
func sortedKeys(updates map[uint64]decimal.Decimal) []uint64 {
	keys := make([]uint64, 0, len(updates))
	for id := range updates {
		keys = append(keys, id)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// shortfallFields 把欠额表转成稳定顺序的日志字段。
func shortfallFields(shortfall map[billing.UnitSettle]decimal.Decimal) []string {
	fields := make([]string, 0, len(shortfall))
	for unit, qty := range shortfall {
		fields = append(fields, fmt.Sprintf("%s=%s", unit, qty.String()))
	}
	sort.Strings(fields)
	return fields
}

// dateLayout 是 sys_calendar 的日期格式。
const dateLayout = "2006-01-02"

// snapshotComponentJSON 是快照里一个计价分量的可复算记载。
type snapshotComponentJSON struct {
	ID         uint64 `json:"id"`
	Metric     string `json:"metric"`
	UnitSettle string `json:"unit_settle"`
	UnitPrice  string `json:"unit_price"`
	BasisQty   string `json:"basis_qty"`
	TierFrom   string `json:"tier_from,omitempty"`
	TierTo     string `json:"tier_to,omitempty"`
	TierBasis  string `json:"tier_basis,omitempty"`
}

// snapshotRuleJSON 是快照里一条命中规则的记载。
type snapshotRuleJSON struct {
	ID         uint64 `json:"id"`
	Scope      string `json:"scope"`
	ScopeID    uint64 `json:"scope_id"`
	Multiplier string `json:"multiplier"`
}

// pricingSnapshotJSON 是 pricing_snapshot 列的内容。
//
// 记下命中的分量、单价与所用阶梯，以及倍率链的各因子与命中规则，
// 价格、规则与日历后续怎么变，历史流水都能按这份快照原样复算。
type pricingSnapshotJSON struct {
	PricingID         uint64                  `json:"pricing_id"`
	Version           int                     `json:"version"`
	At                string                  `json:"at"`
	Components        []snapshotComponentJSON `json:"components"`
	Rules             []snapshotRuleJSON      `json:"rules"`
	ChannelMultiplier string                  `json:"channel_multiplier"`
	AccountMultiplier string                  `json:"account_multiplier"`
}

// buildSnapshot 序列化定价快照。
func buildSnapshot(pricing *Pricing, components []Component, rules []Rule,
	channel, account decimal.Decimal, asOf time.Time) ([]byte, error) {
	snapshot := pricingSnapshotJSON{
		PricingID:         pricing.ID,
		Version:           pricing.Version,
		At:                asOf.Format(time.RFC3339),
		Components:        make([]snapshotComponentJSON, 0, len(components)),
		Rules:             make([]snapshotRuleJSON, 0, len(rules)),
		ChannelMultiplier: channel.String(),
		AccountMultiplier: account.String(),
	}
	for _, component := range components {
		snapshot.Components = append(snapshot.Components, snapshotComponentJSON{
			ID:         component.ID,
			Metric:     string(component.Metric),
			UnitSettle: string(component.UnitSettle),
			UnitPrice:  component.UnitPrice.String(),
			BasisQty:   component.BasisQty.String(),
			TierFrom:   component.TierFrom,
			TierTo:     component.TierTo,
			TierBasis:  component.TierBasis,
		})
	}
	for _, rule := range rules {
		snapshot.Rules = append(snapshot.Rules, snapshotRuleJSON{
			ID:         rule.ID,
			Scope:      string(rule.Scope),
			ScopeID:    rule.ScopeID,
			Multiplier: rule.Multiplier.String(),
		})
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("settlement: 编码 pricing_snapshot 失败: %w", err)
	}
	return payload, nil
}

// settlementLineJSON 是一行扣减明细。
//
// Rate 省略时表示原始单位的直接扣减；非空时该行由其它单位的差额按此折算率
// 换算而来，快照可据此复算（转为 currency 的行，数量 = 原单位差额 × rate）。
type settlementLineJSON struct {
	BucketID uint64 `json:"bucket_id"`
	Unit     string `json:"unit"`
	Qty      string `json:"qty"`
	Rate     string `json:"rate,omitempty"`
}

// settlementJSON 是 settlement 列的内容。
//
// 明细与欠额放同一个对象：多账本明细是常态字段，shortfall 只在不足时出现，
// 用对象承载两者比裸数组更能表达「这次扣了哪些、还差多少」。
type settlementJSON struct {
	Lines     []settlementLineJSON `json:"lines"`
	Shortfall map[string]string    `json:"shortfall,omitempty"`
}

// buildSettlement 序列化扣减明细。
func buildSettlement(plan DeductionPlan) ([]byte, error) {
	payload := settlementJSON{Lines: make([]settlementLineJSON, 0, len(plan.Lines))}
	for _, line := range plan.Lines {
		settlementLine := settlementLineJSON{
			BucketID: line.BucketID,
			Unit:     string(line.Unit),
			Qty:      line.Qty.String(),
		}
		// 零折算率是「无折算」的占位，不写进明细，避免每行都带一个无意义的 0。
		if line.Rate.IsPositive() {
			settlementLine.Rate = line.Rate.String()
		}
		payload.Lines = append(payload.Lines, settlementLine)
	}
	if len(plan.Shortfall) > 0 {
		payload.Shortfall = make(map[string]string, len(plan.Shortfall))
		for unit, qty := range plan.Shortfall {
			payload.Shortfall[string(unit)] = qty.String()
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("settlement: 编码 settlement 失败: %w", err)
	}
	return encoded, nil
}
