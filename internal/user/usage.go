package user

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件实现账户面的用量流水列表。
//
// 作用域由会话推导，不接受账户或商家参数；渠道与商家标识不进响应：
// 它们是运营口径，账户面只回答「我用了多少、扣了多少」。

const (
	// sinceQueryParam / untilQueryParam 是写入时刻的闭区间参数。
	sinceQueryParam = "since"
	untilQueryParam = "until"
	// modelQueryParam 是按客户端模型名精确匹配的过滤参数。
	modelQueryParam = "model"
	// apiKeyQueryParam 是按签发密钥过滤的参数。
	apiKeyQueryParam = "api_key_id"
)

// serverToolUsesMetric 是服务端工具执行次数的用量键。
//
// 它与 domain.Usage 的同名字段对应，但 billing 的计费分量表里没有这一项
// （见 billing.UsageFromDomain：无对应 Metric 的分量记日志后丢弃），
// 因此既有流水里该键不存在，读出恒为 0。键一旦在计费分量里落地，
// 这里无需改动即可读到取值。
const serverToolUsesMetric = "server_tool_uses"

// errInvalidTimeRange 是 since / until 非法时的错误，文案直接写回信封。
var errInvalidTimeRange = errors.New("since 与 until 必须是 RFC3339 时刻")

// usageTokensView 是一次调用的 token 用量分量，字段与契约 UsageTokens 对齐。
//
// 各计数恒出现（未取得的为 0），前端不必做缺省分支。
type usageTokensView struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	CacheReadTokens    int `json:"cache_read_tokens"`
	CacheWriteTokens   int `json:"cache_write_tokens"`
	CacheWrite5mTokens int `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int `json:"cache_write_1h_tokens"`
	ReasoningTokens    int `json:"reasoning_tokens"`
	ServerToolUses     int `json:"server_tool_uses"`
}

// usageItemView 是一条用量流水，字段与契约 UsageItem 对齐。
type usageItemView struct {
	ID uint64 `json:"id"`
	// Model 是客户端请求的模型名，UpstreamModel 是实际履约的模型名。
	Model         string          `json:"model"`
	UpstreamModel string          `json:"upstream_model"`
	Protocol      string          `json:"protocol"`
	CrossProtocol bool            `json:"cross_protocol"`
	Usage         usageTokensView `json:"usage"`
	// ChargedAmount 是本次应扣量：基础价 × 用量 × 最终倍率，十进制字符串。
	// 展示层不得经浮点解析，故这里给出与账户概览同一口径的已折算文本。
	ChargedAmount string    `json:"charged_amount"`
	CreatedAt     time.Time `json:"created_at"`
}

// handleUsage 按偏移分页返回当前账户的用量流水。
func (h *Handler) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 方法")
		return
	}
	account, ok := h.currentAccount(w, r)
	if !ok {
		return
	}
	page, size, err := parsePaging(r.URL.Query())
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	filter, err := parseUsageFilter(r.URL.Query(), account.ID, size, (page-1)*size)
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	rows, total, err := h.store.ListAccountUsage(r.Context(), filter)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "用量流水查询失败")
		return
	}
	items := make([]usageItemView, 0, len(rows))
	for i := range rows {
		item, err := newUsageItemView(&rows[i])
		if err != nil {
			// 库中的金额与倍率是 DECIMAL 文本，解析失败说明这一行已损坏；
			// 按 500 处理而不是跳过该行：静默少一行会让用户以为那次调用没发生。
			webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "用量流水解析失败")
			return
		}
		items = append(items, item)
	}
	webapi.WritePage(w, map[string]any{itemsKey: items}, page, size, total)
}

// parseUsageFilter 解析用量列表的过滤条件；账户与分页由调用方给定。
func parseUsageFilter(query url.Values, accountID uint64, limit, offset int) (store.AccountUsageFilter, error) {
	filter := store.AccountUsageFilter{AccountID: accountID, Limit: limit, Offset: offset}
	since, err := parseMoment(query.Get(sinceQueryParam))
	if err != nil {
		return store.AccountUsageFilter{}, err
	}
	until, err := parseMoment(query.Get(untilQueryParam))
	if err != nil {
		return store.AccountUsageFilter{}, err
	}
	filter.Since, filter.Until = since, until
	filter.RequestedModel = strings.TrimSpace(query.Get(modelQueryParam))
	rawKey := strings.TrimSpace(query.Get(apiKeyQueryParam))
	if rawKey != "" {
		keyID, err := strconv.ParseUint(rawKey, 10, 64)
		if err != nil || keyID == 0 {
			return store.AccountUsageFilter{}, errors.New("api_key_id 必须是正整数")
		}
		filter.APIKeyID = keyID
	}
	return filter, nil
}

// parseMoment 解析 RFC3339 时刻；空串返回零值表示该侧不限。
func parseMoment(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, nil
	}
	moment, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}, errInvalidTimeRange
	}
	return moment, nil
}

// newUsageItemView 把存储行映射为对外形状。
func newUsageItemView(row *store.AccountUsageRow) (usageItemView, error) {
	gross, err := decimal.NewFromString(row.GrossAmount)
	if err != nil {
		return usageItemView{}, err
	}
	multiplier, err := decimal.NewFromString(row.Multiplier)
	if err != nil {
		return usageItemView{}, err
	}
	usage, err := decodeUsageTokens(row.Usage)
	if err != nil {
		return usageItemView{}, err
	}
	return usageItemView{
		ID:            row.ID,
		Model:         row.RequestedModel,
		UpstreamModel: row.Model,
		Protocol:      row.Protocol,
		CrossProtocol: row.CrossProtocol,
		Usage:         usage,
		ChargedAmount: gross.Mul(multiplier).String(),
		CreatedAt:     row.CreatedAt,
	}, nil
}

// decodeUsageTokens 把落库的 metric -> 数量 JSON 映射为对外的分量字段。
//
// 未登记的键直接忽略：计费表可以新增 metric（见 billing 的枚举约定），
// 多出来的键属于该版本还不认识的计费维度，读到这里既不必报错也无处展示。
func decodeUsageTokens(raw json.RawMessage) (usageTokensView, error) {
	var metrics map[string]int
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &metrics); err != nil {
			return usageTokensView{}, err
		}
	}
	return usageTokensView{
		InputTokens:        metrics[string(billing.MetricInputToken)],
		OutputTokens:       metrics[string(billing.MetricOutputToken)],
		CacheReadTokens:    metrics[string(billing.MetricCacheReadToken)],
		CacheWriteTokens:   metrics[string(billing.MetricCacheWriteToken)],
		CacheWrite5mTokens: metrics[string(billing.MetricCacheWrite5m)],
		CacheWrite1hTokens: metrics[string(billing.MetricCacheWrite1h)],
		ReasoningTokens:    metrics[string(billing.MetricReasoningToken)],
		ServerToolUses:     metrics[serverToolUsesMetric],
	}, nil
}
