package partner

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/sumwai/tokenmp/internal/settlement"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件是商家域的分账对账单端点：把本商家一个账期的结算结果读出来。
//
// 出账只有一处实现（`internal/admin` 的 `SettlementBill`，口径在 `internal/settlement`），
// 本文件只负责作用域与对外形状：作用域来自会话推导出的商家，端点因此不接受商家参数，
// 越权面与其余商家域端点相同。

// SettlementPath 是分账对账单的固定路径。取值与 docs/openapi-web.yaml 一致，
// 改动须同步契约。
const SettlementPath = PathPrefix + "/settlement"

// Settlements 出账一个商家的对账单，由 internal/admin 的出账实现满足。
//
// 抽成口径、账期边界与四处金额的相互约束都在被实现方，本包不重算 ——
// 页面与 CLI 因此看的是同一份出账逻辑，两处的数不会各算一套。
type Settlements interface {
	// SettlementBill 出账一个商家在 [from, to) 的对账单；from / to 都为零值时
	// 按该商家的账期取上一个完整自然周期。
	SettlementBill(ctx context.Context, merchantID uint64, from, to time.Time) (settlement.BillView, error)
}

// partnerSettlementView 是对账单的对外形状，字段与契约 PartnerSettlement 对齐
// （即 settlement.BillView 减去 merchant_id）。
//
// 金额是十进制字符串：DECIMAL(24,8) 的有效位远超双精度浮点，写成 JSON 数字会在浏览器里丢精度。
// 不含 merchant_id：它就是会话本身，回显只是把内部标识漏给页面。
type partnerSettlementView struct {
	Period         string `json:"period"`
	From           string `json:"from"`
	To             string `json:"to"`
	CommissionRate string `json:"commission_rate"`
	Trades         int64  `json:"trades"`
	GrossSales     string `json:"gross_sales"`
	Commission     string `json:"commission"`
	UpstreamCost   string `json:"upstream_cost"`
	Payout         string `json:"payout"`
}

// 账期参数名，取值与契约一致。
const (
	fromQueryParam = "from"
	toQueryParam   = "to"
)

// 账期参数的用法错误文案，直接写回信封。
var (
	errSettlementWindowPaired = errors.New("from 与 to 要么都给，要么都不给")
	errSettlementWindowOrder  = errors.New("to 必须晚于 from")
)

// handleSettlement 返回本商家一个账期的分账对账单。
//
// 不给区间时按本商家的账期取上一个完整自然周期；给了就按给的区间（左闭右开）。
func (h *Handler) handleSettlement(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 方法")
		return
	}
	merchant, ok := h.currentMerchant(w, r)
	if !ok {
		return
	}
	from, to, err := parseSettlementWindow(r.URL.Query())
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	if h.settlements == nil {
		// 装配缺少出账实现（存储层不满足管理面数据面时会出现）：这是部署问题。
		// 不回「没有对账单」——那会把配置错误说成业务事实。
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "服务端错误")
		return
	}
	bill, err := h.settlements.SettlementBill(r.Context(), merchant.ID, from, to)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "结算对账单查询失败")
		return
	}
	webapi.WriteOK(w, partnerSettlementView{
		Period:         bill.Period,
		From:           bill.From,
		To:             bill.To,
		CommissionRate: bill.CommissionRate,
		Trades:         bill.Trades,
		GrossSales:     bill.GrossSales,
		Commission:     bill.Commission,
		UpstreamCost:   bill.UpstreamCost,
		Payout:         bill.Payout,
	})
}

// parseSettlementWindow 读账期参数。
//
// 两个都缺省返回零值，由出账侧按商家的账期取上一个完整自然周期；只给一个算用法错误 ——
// 缺的那一半会落到零值，账期于是变成「世界上所有流水」，那是用法错误而不是默认值。
func parseSettlementWindow(query url.Values) (time.Time, time.Time, error) {
	from, err := parseMoment(query.Get(fromQueryParam))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := parseMoment(query.Get(toQueryParam))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if from.IsZero() != to.IsZero() {
		return time.Time{}, time.Time{}, errSettlementWindowPaired
	}
	if !from.IsZero() && !to.After(from) {
		return time.Time{}, time.Time{}, errSettlementWindowOrder
	}
	return from, to, nil
}
