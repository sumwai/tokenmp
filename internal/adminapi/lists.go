package adminapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件是九组只读清单的视图与处理器。
//
// 视图不直接序列化存储行：对外字段是一份白名单，存储层新增列不会自动出现在管理面。
// 每个视图的字段与 `tokenmp admin <组> list --json` 的行一一对应（凭据与限额直接
// 复用 internal/admin 的展示口径），页面因此与 CLI 同源，不会各算一套。

// channelView 是渠道的对外形状，字段与契约 AdminChannel 对齐。
type channelView struct {
	ID         uint64 `json:"id"`
	MerchantID uint64 `json:"merchant_id"`
	Name       string `json:"name"`
	Vendor     string `json:"vendor"`
	Type       string `json:"type"`
	CredGroup  string `json:"cred_group"`
	BaseURL    string `json:"base_url"`
	Priority   int    `json:"priority"`
	Weight     int    `json:"weight"`
	Enabled    bool   `json:"enabled"`
}

// modelMapView 是渠道模型映射的对外形状，字段与契约 AdminModelMap 对齐。
type modelMapView struct {
	ID               uint64          `json:"id"`
	ChannelID        uint64          `json:"channel_id"`
	Model            string          `json:"model"`
	UpstreamModel    string          `json:"upstream_model"`
	PriceMultiplier  string          `json:"price_multiplier"`
	RequestOverrides json.RawMessage `json:"request_overrides,omitempty"`
	Enabled          bool            `json:"enabled"`
}

// accountView 是账户的对外形状，字段与契约 AdminAccount 对齐。
type accountView struct {
	ID                uint64  `json:"id"`
	Code              string  `json:"code"`
	Name              string  `json:"name"`
	OwnerUserID       *uint64 `json:"owner_user_id"`
	DefaultMerchantID *uint64 `json:"default_merchant_id"`
	PriceMultiplier   string  `json:"price_multiplier"`
	Status            string  `json:"status"`
}

// pricingView 是定价版本的对外形状，字段与契约 AdminPricing 对齐。
//
// 不含「生效 / 已退役」这类派生列：判定就是 retired_at 是否为 null，前端从同一字段
// 得出，与 CLI 的同一判定，不额外多一份状态来源。
type pricingView struct {
	ID          uint64     `json:"id"`
	MerchantID  uint64     `json:"merchant_id"`
	Model       string     `json:"model"`
	Version     int        `json:"version"`
	EffectiveAt time.Time  `json:"effective_at"`
	RetiredAt   *time.Time `json:"retired_at"`
}

// adjustmentView 是调账记录的对外形状，字段与契约 AdminAdjustment 对齐。
type adjustmentView struct {
	ID          uint64    `json:"id"`
	AccountID   uint64    `json:"account_id"`
	DeltaAmount string    `json:"delta_amount"`
	Reason      string    `json:"reason"`
	Operator    string    `json:"operator"`
	CreatedAt   time.Time `json:"created_at"`
}

// usageView 是用量流水的对外形状，字段与契约 AdminUsageItem 对齐。
type usageView struct {
	ID         uint64          `json:"id"`
	MerchantID uint64          `json:"merchant_id"`
	AccountID  uint64          `json:"account_id"`
	ChannelID  uint64          `json:"channel_id"`
	Model      string          `json:"model"`
	Usage      json.RawMessage `json:"usage"`
	Gross      string          `json:"gross_amount"`
	Multiplier string          `json:"multiplier"`
	Settlement json.RawMessage `json:"settlement,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

// handleChannels 列出全部渠道。
func (h *Handler) handleChannels(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	page, ok := pageOf(w, r)
	if !ok {
		return
	}
	rows, err := h.lister.ListChannels(r.Context())
	if err != nil {
		writeInternal(w)
		return
	}
	views := make([]channelView, 0, len(rows))
	for _, row := range rows {
		views = append(views, channelView{
			ID:         row.ID,
			MerchantID: row.MerchantID,
			Name:       row.Name,
			Vendor:     row.Vendor,
			Type:       string(row.Type),
			CredGroup:  row.CredGroup,
			BaseURL:    row.BaseURL,
			Priority:   row.Priority,
			Weight:     row.Weight,
			Enabled:    row.Enabled,
		})
	}
	items, total := pageSlice(views, page)
	writeItems(w, items, page, total)
}

// handleCredentials 列出全部上游凭据（只给脱敏前缀）。
func (h *Handler) handleCredentials(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	page, ok := pageOf(w, r)
	if !ok {
		return
	}
	views, err := h.lister.ListCredentials(r.Context())
	if err != nil {
		writeInternal(w)
		return
	}
	items, total := pageSlice(views, page)
	writeItems(w, items, page, total)
}

// handleModelMaps 列出全部渠道模型映射。
func (h *Handler) handleModelMaps(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	page, ok := pageOf(w, r)
	if !ok {
		return
	}
	rows, err := h.lister.ListModelMaps(r.Context())
	if err != nil {
		writeInternal(w)
		return
	}
	views := make([]modelMapView, 0, len(rows))
	for _, row := range rows {
		views = append(views, modelMapView{
			ID:               row.ID,
			ChannelID:        row.ChannelID,
			Model:            row.Model,
			UpstreamModel:    row.UpstreamModel,
			PriceMultiplier:  row.PriceMultiplier,
			RequestOverrides: row.RequestOverrides,
			Enabled:          row.Enabled,
		})
	}
	items, total := pageSlice(views, page)
	writeItems(w, items, page, total)
}

// handleAccounts 列出全部账户。
func (h *Handler) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	page, ok := pageOf(w, r)
	if !ok {
		return
	}
	rows, err := h.lister.ListAccounts(r.Context())
	if err != nil {
		writeInternal(w)
		return
	}
	views := make([]accountView, 0, len(rows))
	for _, row := range rows {
		views = append(views, accountView{
			ID:                row.ID,
			Code:              row.Code,
			Name:              row.Name,
			OwnerUserID:       row.OwnerUserID,
			DefaultMerchantID: row.DefaultMerchantID,
			PriceMultiplier:   row.PriceMultiplier,
			Status:            row.Status,
		})
	}
	items, total := pageSlice(views, page)
	writeItems(w, items, page, total)
}

// handlePricing 列出定价版本，支持按商家与模型过滤。
func (h *Handler) handlePricing(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	page, ok := pageOf(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	merchantID, err := parseOptionalUint(query, "merchant_id")
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	rows, err := h.lister.ListPricing(r.Context(), merchantID, query.Get("model"))
	if err != nil {
		writeInternal(w)
		return
	}
	views := make([]pricingView, 0, len(rows))
	for _, row := range rows {
		views = append(views, pricingView{
			ID:          row.ID,
			MerchantID:  row.MerchantID,
			Model:       row.Model,
			Version:     row.Version,
			EffectiveAt: row.EffectiveAt,
			RetiredAt:   row.RetiredAt,
		})
	}
	items, total := pageSlice(views, page)
	writeItems(w, items, page, total)
}

// handleQuotas 列出窗口限额，支持按范围与账户过滤。
//
// 过滤维度与 CLI 一致：`scope` + `scope_id` 是通用入口，`account_id` 是账户维度的简写；
// 两者互斥，`scope` 与 `scope_id` 必须成对，否则缺省的空 scope 会被白名单校验报出，
// 报错点就偏离真正缺失的那一半。
func (h *Handler) handleQuotas(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	page, ok := pageOf(w, r)
	if !ok {
		return
	}
	scope, ok := parseScopeFilter(w, r)
	if !ok {
		return
	}
	scopeID, err := parseOptionalUint(r.URL.Query(), "scope_id")
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	accountID, err := parseOptionalUint(r.URL.Query(), "account_id")
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	if scope != "" {
		if scopeID == 0 {
			webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "scope 与 scope_id 必须成对给出")
			return
		}
	} else if accountID != 0 {
		scope, scopeID = billing.ScopeAccount, accountID
	}
	views, err := h.lister.ListQuotas(r.Context(), scope, scopeID)
	if err != nil {
		writeInternal(w)
		return
	}
	items, total := pageSlice(views, page)
	writeItems(w, items, page, total)
}

// parseScopeFilter 读并校验 scope 查询参数；空串表示不过滤。
func parseScopeFilter(w http.ResponseWriter, r *http.Request) (billing.Scope, bool) {
	raw := r.URL.Query().Get("scope")
	if raw == "" {
		return "", true
	}
	scope := billing.Scope(raw)
	if err := billing.ValidateScope(scope); err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return "", false
	}
	return scope, true
}

// handleAdjustments 列出调账记录，支持按账户过滤。
func (h *Handler) handleAdjustments(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	page, ok := pageOf(w, r)
	if !ok {
		return
	}
	accountID, err := parseOptionalUint(r.URL.Query(), "account_id")
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	rows, err := h.lister.ListAdjustments(r.Context(), accountID)
	if err != nil {
		writeInternal(w)
		return
	}
	views := make([]adjustmentView, 0, len(rows))
	for _, row := range rows {
		views = append(views, adjustmentView{
			ID:          row.ID,
			AccountID:   row.AccountID,
			DeltaAmount: row.DeltaAmount,
			Reason:      row.Reason,
			Operator:    row.Operator,
			CreatedAt:   row.CreatedAt,
		})
	}
	items, total := pageSlice(views, page)
	writeItems(w, items, page, total)
}

// handleUsage 列出全平台用量流水，支持按账户与起始时刻过滤。
func (h *Handler) handleUsage(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	page, ok := pageOf(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	accountID, err := parseOptionalUint(query, "account_id")
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	since, err := parseSince(query.Get("since"))
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	rows, err := h.lister.ListUsage(r.Context(), accountID, since)
	if err != nil {
		writeInternal(w)
		return
	}
	views := make([]usageView, 0, len(rows))
	for _, row := range rows {
		views = append(views, usageView{
			ID:         row.ID,
			MerchantID: row.MerchantID,
			AccountID:  row.AccountID,
			ChannelID:  row.ChannelID,
			Model:      row.Model,
			Usage:      row.Usage,
			Gross:      row.GrossAmount,
			Multiplier: row.Multiplier,
			Settlement: row.Settlement,
			CreatedAt:  row.CreatedAt,
		})
	}
	items, total := pageSlice(views, page)
	writeItems(w, items, page, total)
}

// adminSettlementView 是一个商家的结算对账单的对外形状，字段与契约 AdminSettlementItem 对齐。
//
// 字段名与 `settlement.BillView` 一致，只多一个 merchant_id：管理面是跨商家的清单，
// 没有它分行就无从归属。
type adminSettlementView struct {
	MerchantID     uint64 `json:"merchant_id"`
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

// handleSettlements 列出全平台结算对账单，支持按商家与账期过滤。
//
// 账期缺省时按各商家自己的 settle_info 账期取上一个完整自然周期：账期是商家自己的口径，
// 同一批里各家的账期可以不同。出账逻辑来自 internal/admin 的 SettlementBill，
// 与 CLI 的 `settlement list` 同源。
func (h *Handler) handleSettlements(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	page, ok := pageOf(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	merchantID, err := parseOptionalUint(query, "merchant_id")
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	from, err := parseMomentParam("from", query.Get("from"))
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	to, err := parseMomentParam("to", query.Get("to"))
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	// 只给一侧会让另一半落到零值，账期变成「世界上所有流水」：那是用法错误，不是默认值。
	if from.IsZero() != to.IsZero() {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "from 与 to 要么都给，要么都不给")
		return
	}
	if !from.IsZero() && !to.After(from) {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "to 必须晚于 from")
		return
	}
	bills, err := h.lister.SettlementBills(r.Context(), admin.SettlementQuery{
		MerchantID: merchantID,
		From:       from,
		To:         to,
	})
	if err != nil {
		writeInternal(w)
		return
	}
	views := make([]adminSettlementView, 0, len(bills))
	for _, bill := range bills {
		views = append(views, adminSettlementView{
			MerchantID:     bill.MerchantID,
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
	items, total := pageSlice(views, page)
	writeItems(w, items, page, total)
}

// parseSince 解析 since 查询参数；缺省为零值，表示不按时间过滤。
func parseSince(raw string) (time.Time, error) {
	return parseMomentParam("since", raw)
}

// parseMomentParam 解析一个 RFC3339 时刻查询参数；空串返回零值。
//
// name 只用于错误文案：账期端点用 from / to，沿用 since 的说法会让人以为传错了参数名。
func parseMomentParam(name, raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	moment, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s 必须是 RFC3339 时刻", name)
	}
	return moment, nil
}

// pageOf 解析分页参数并把非法取值写回信封；返回 false 表示已写出响应。
func pageOf(w http.ResponseWriter, r *http.Request) (pageQuery, bool) {
	page, err := parsePage(r.URL.Query())
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return pageQuery{}, false
	}
	return page, true
}
