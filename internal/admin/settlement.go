package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/settlement"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是商家分佣与出账的管理面动作：settle_info 的读写与按账期出账。
//
// 口径（抽成率怎么解释、账期怎么取、四项合计怎么相互约束）由 internal/settlement 定义，
// 本文件只做编排：读口径 → 定账期 → 取事实 → 交给 settlement.Settle。分账规则不在
// 这一层重复实现，否则管理面与页面面会各算一遍，两处的数迟早不一致。

// SettleInfoInput 是写入分账口径的输入；空串字段表示保留现值。
//
// 分字段可缺省而不是「整份覆盖」：改抽成率不该顺带把账期重置回默认，
// 那是一次静默的口径变更，出账周期会莫名其妙地变。
type SettleInfoInput struct {
	// MerchantID 是商家 id。
	MerchantID uint64
	// CommissionRate 是新的平台抽成率，十进制字符串；空串保留现值。
	CommissionRate string
	// Period 是新的结算账期；空串保留现值。
	Period string
}

// MerchantSettleInfo 读一个商家的分账口径；未配置时给出默认口径（不抽成、按月出账）。
func (s *Service) MerchantSettleInfo(ctx context.Context, merchantID uint64) (settlement.SettleInfo, error) {
	if err := requireID("商家 id", merchantID); err != nil {
		return settlement.SettleInfo{}, err
	}
	raw, err := s.store.MerchantSettleInfo(ctx, merchantID)
	if err != nil {
		return settlement.SettleInfo{}, err
	}
	return settlement.ParseSettleInfo(raw)
}

// SetMerchantSettleInfo 更新一个商家的分账口径，返回更新后的口径。
//
// 先读现值再写：这是一次「改哪一项留哪一项」的部分更新，读回现值顺带把「商家是否
// 存在」问清楚了——对不存在的商家写口径在存储层是静默空操作，只有这次读能拦住。
func (s *Service) SetMerchantSettleInfo(ctx context.Context, in SettleInfoInput) (settlement.SettleInfo, error) {
	if err := requireID("商家 id", in.MerchantID); err != nil {
		return settlement.SettleInfo{}, err
	}
	info, err := s.MerchantSettleInfo(ctx, in.MerchantID)
	if err != nil {
		return settlement.SettleInfo{}, err
	}
	if rate := strings.TrimSpace(in.CommissionRate); rate != "" {
		parsed, perr := parseDecimal(rate)
		if perr != nil {
			return settlement.SettleInfo{}, fmt.Errorf("admin: 平台抽成率 %q 不是合法数字", in.CommissionRate)
		}
		info.CommissionRate = parsed
	}
	if period := strings.TrimSpace(in.Period); period != "" {
		info.Period = billing.Period(period)
	}
	// Encode 同时做校验：抽成率的取值域与账期白名单都在口径那一层收口。
	encoded, err := info.Encode()
	if err != nil {
		return settlement.SettleInfo{}, err
	}
	if err := s.store.SetMerchantSettleInfo(ctx, in.MerchantID, encoded); err != nil {
		return settlement.SettleInfo{}, err
	}
	return info, nil
}

// SettlementQuery 是出账视图的查询条件。
type SettlementQuery struct {
	// MerchantID 限定单个商家；0 表示全部商家（全平台结算视图）。
	MerchantID uint64
	// From / To 是账期边界 [From, To)；两者都为零值时按各商家 settle_info 的账期
	// 取「上一个完整自然周期」。只给一侧是用法错误，由命令层拦下。
	From time.Time
	To   time.Time
}

// SettlementBill 出账一个商家的对账单。
//
// merchantID 由调用方给出：管理面按 `--merchant` 或全表遍历给，商家域页面按会话推导出的
// 商家给 —— 出账规则因此只有一处实现，页面与 CLI 看到的数不会各算一套。
// from / to 都为零值时按该商家 settle_info 的账期取上一个完整自然周期。
func (s *Service) SettlementBill(ctx context.Context, merchantID uint64, from, to time.Time) (settlement.BillView, error) {
	if err := requireID("商家 id", merchantID); err != nil {
		return settlement.BillView{}, err
	}
	// 只给一侧会让缺的那一半落到零值，账期变成「世界上所有流水」或一个空区间：
	// 调用方各自拦过一遍，但出账是共用实现，防线不靠调用方自觉。
	if from.IsZero() != to.IsZero() {
		return settlement.BillView{}, errors.New("admin: 账期的起点与终点要么都给，要么都不给")
	}
	info, err := s.MerchantSettleInfo(ctx, merchantID)
	if err != nil {
		return settlement.BillView{}, err
	}
	if from.IsZero() && to.IsZero() {
		from, to, err = settlement.LastPeriod(info.Period, s.now())
		if err != nil {
			return settlement.BillView{}, err
		}
	}
	facts, err := s.store.MerchantSettlementFacts(ctx, merchantID, from, to)
	if err != nil {
		return settlement.BillView{}, err
	}
	sum, err := settlementSummary(facts)
	if err != nil {
		return settlement.BillView{}, err
	}
	return settlement.Settle(merchantID, info, from, to, sum).View(), nil
}

// SettlementBills 按账期出账，返回每个商家的对账单。
//
// 逐个商家出账而不是一条多表查询：账期是每个商家自己的口径，同一批里各家的账期
// 可以不同（按日 / 按周 / 按月），一条查询表达不了；管理面是运维工具，商家数量
// 以百计，逐条查询换来的可读性与口径独立性远比那点往返耗时值。
func (s *Service) SettlementBills(ctx context.Context, q SettlementQuery) ([]settlement.BillView, error) {
	merchants, err := s.settleMerchants(ctx, q.MerchantID)
	if err != nil {
		return nil, err
	}
	views := make([]settlement.BillView, 0, len(merchants))
	for _, m := range merchants {
		bill, billErr := s.SettlementBill(ctx, m.ID, q.From, q.To)
		if billErr != nil {
			return nil, billErr
		}
		views = append(views, bill)
	}
	return views, nil
}

// settleMerchants 取本次出账的商家集合：给定 id 时只取该商家，0 表示全部商家。
//
// 复用 ListMerchants 而不是新开一条按 id 的查询：管理面本来就要读全量商家，
// 多一条查询只会多一处需要同步维护的 SQL。
func (s *Service) settleMerchants(ctx context.Context, merchantID uint64) ([]store.Merchant, error) {
	merchants, err := s.store.ListMerchants(ctx)
	if err != nil {
		return nil, err
	}
	if merchantID == 0 {
		return merchants, nil
	}
	for _, m := range merchants {
		if m.ID == merchantID {
			return []store.Merchant{m}, nil
		}
	}
	return nil, fmt.Errorf("admin: 商家 id=%d 不存在", merchantID)
}

// settlementSummary 把库中的十进制文本折成对账事实。
func settlementSummary(facts store.SettlementFacts) (settlement.Summary, error) {
	grossSales, err := parseDecimal(facts.GrossSales)
	if err != nil {
		return settlement.Summary{}, fmt.Errorf("admin: 解析卖出总额 %q 失败: %w", facts.GrossSales, err)
	}
	upstreamCost, err := parseDecimal(facts.UpstreamCost)
	if err != nil {
		return settlement.Summary{}, fmt.Errorf("admin: 解析上游成本 %q 失败: %w", facts.UpstreamCost, err)
	}
	return settlement.Summary{
		Trades:       facts.Trades,
		GrossSales:   grossSales,
		UpstreamCost: upstreamCost,
	}, nil
}
