package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// 本文件是商家分佣对账的读路径：merchant.settle_info 的读写与账期聚合。
//
// 聚合口径与 docs/compatibility.md 的「分佣与结算口径」一节同源：卖出总额取
// account_purchase 的实付，上游成本取 billing_usage 的牌价基础价。两条聚合各走
// 自己的商家 + 时间索引，而不是拉成一条多表 JOIN：管理面是运维工具，把口径写进
// ON 条件只会让它更难核对。金额一律以库中十进制文本交出，数值解释属计费口径那一层。

const selectMerchantSettleInfoSQL = `SELECT settle_info FROM merchant WHERE id = ?`

// MerchantSettleInfo 读一个商家的分账口径原文；未配置时返回 nil。
//
// 交出原文而不是解析结果：settle_info 的形状属计费口径，规则在 internal/settlement。
// 存储层只负责把库里的字节取出来——JSON 列可空，先扫进 []byte（NULL 得到 nil），
// 再交给上层决定怎么解释。
func (s *Store) MerchantSettleInfo(ctx context.Context, id uint64) ([]byte, error) {
	if id == 0 {
		return nil, errors.New("store: merchant.id 不能为 0")
	}
	var raw []byte
	if err := s.db.QueryRowContext(ctx, selectMerchantSettleInfoSQL, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("store: 商家 id=%d 不存在", id)
		}
		return nil, fmt.Errorf("store: 查询 merchant.settle_info 失败: %w", err)
	}
	return raw, nil
}

const updateMerchantSettleInfoSQL = `UPDATE merchant SET settle_info = ? WHERE id = ?`

// SetMerchantSettleInfo 写一个商家的分账口径。
//
// 刻意不看影响行数：MySQL 在新值与旧值相同时报 0 行，据此判「商家不存在」会把
// 重复写入同一口径的正常动作误报成失败。存在性由调用方的读路径负责
// （管理面先读现值再写，缺省语义也依赖这次读）。
func (s *Store) SetMerchantSettleInfo(ctx context.Context, id uint64, raw []byte) error {
	if id == 0 {
		return errors.New("store: merchant.id 不能为 0")
	}
	var value any
	if len(raw) > 0 {
		value = string(raw)
	}
	if _, err := s.db.ExecContext(ctx, updateMerchantSettleInfoSQL, value, id); err != nil {
		return fmt.Errorf("store: 更新 merchant.settle_info 失败: %w", err)
	}
	return nil
}

// SettlementFacts 是一个商家在一个账期内的对账事实；金额保持库中十进制文本。
//
// 文本而不是 decimal：与 RecentUsage / UsageStatsItem 的口径一致，存储层不做
// 数值解释，折算与舍入由计费口径那一层负责。
type SettlementFacts struct {
	// MerchantID 是事实归属的商家。
	MerchantID uint64
	// Trades 是账期内该商家的成交笔数。
	Trades int64
	// GrossSales 是卖出总额（account_purchase.price_paid 合计）。
	GrossSales string
	// UpstreamCost 是上游成本（billing_usage.gross_amount 合计）。
	UpstreamCost string
}

const settlementSalesSQL = `SELECT COUNT(*), COALESCE(SUM(price_paid), 0)
FROM account_purchase
WHERE merchant_id = ? AND purchased_at >= ? AND purchased_at < ?`

const settlementCostSQL = `SELECT COALESCE(SUM(gross_amount), 0)
FROM billing_usage
WHERE merchant_id = ? AND created_at >= ? AND created_at < ?`

// MerchantSettlementFacts 聚合一个商家在账期 [from, to) 内的卖出与上游成本。
//
// 区间左闭右开：账期是首尾相接的，边界时刻的流水归后一期，既不会漏也不会被两期各算一次。
// 笔数与金额在同一条查询里取出：分两次扫会让两个数可能来自不同的数据快照，
// 出账时就会出现「三笔的钱」配「四笔的笔数」这种对不上的账。
func (s *Store) MerchantSettlementFacts(ctx context.Context, merchantID uint64, from, to time.Time) (SettlementFacts, error) {
	if merchantID == 0 {
		return SettlementFacts{}, errors.New("store: merchant_id 不能为 0")
	}
	facts := SettlementFacts{MerchantID: merchantID}
	if err := s.db.QueryRowContext(ctx, settlementSalesSQL, merchantID, from, to).
		Scan(&facts.Trades, &facts.GrossSales); err != nil {
		return SettlementFacts{}, fmt.Errorf("store: 聚合 account_purchase 失败: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, settlementCostSQL, merchantID, from, to).
		Scan(&facts.UpstreamCost); err != nil {
		return SettlementFacts{}, fmt.Errorf("store: 聚合 billing_usage 失败: %w", err)
	}
	return facts, nil
}
