package user

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件实现账户面的用量聚合：按天 / 模型 / 密钥把区间内的用量与应扣量合计起来。
//
// 它只回答合计，不返回单条流水：看某次调用的明细走用量列表端点。合计与明细同源
// （同一组过滤谓词、同一个指标键集合），因此同一区间上的合计与逐行相加自洽。

const (
	// usageStatsSuffix 是挂在使用量端点下的聚合子树后缀。
	usageStatsSuffix = "/stats"
)

// usageStatsGroups 是分组维度的取值集合，与契约的枚举一致。
var usageStatsGroups = map[string]struct{}{
	"day":     {},
	"model":   {},
	"api_key": {},
}

// errInvalidUsageGroupBy 是分组维度非法时的错误，文案直接写回信封。
var errInvalidUsageGroupBy = errors.New("group_by 取值必须是 day / model / api_key")

// usageStatsItemView 是一个分组的用量合计，与契约 UsageStatsItem 对齐。
type usageStatsItemView struct {
	// Key 是分组值：日期（YYYY-MM-DD）、模型名或密钥 id 的十进制文本。
	Key string `json:"key"`
	// Calls 是区间内成功履约的请求次数。
	Calls int64 `json:"calls"`
	// Usage 是各 token 分量的合计，字段与用量明细同形。
	Usage usageTokensView `json:"usage"`
	// ChargedAmount 是应扣量合计，十进制字符串。
	ChargedAmount string `json:"charged_amount"`
}

// handleUsageStats 返回当前账户的用量聚合。
func (h *Handler) handleUsageStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 方法")
		return
	}
	account, ok := h.currentAccount(w, r)
	if !ok {
		return
	}
	query, err := parseUsageStatsQuery(r, account.ID)
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	items, err := h.store.AccountUsageStats(r.Context(), query)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "用量聚合查询失败")
		return
	}
	views := make([]usageStatsItemView, 0, len(items))
	for _, item := range items {
		views = append(views, usageStatsItemView{
			Key:           item.Key,
			Calls:         item.Calls,
			Usage:         usageTokensFromMetrics(item.Tokens),
			ChargedAmount: item.ChargedAmount,
		})
	}
	// 聚合结果不分页：分组取值数由维度自身决定（区间内的天数、模型数、密钥数），
	// 不是可增长的数据集合。分页三字段照填「一页含全部」，与其余列表端点同形。
	total := len(views)
	webapi.WritePage(w, map[string]any{itemsKey: views}, 1, total, total)
}

// parseUsageStatsQuery 解析聚合查询的过滤与分组条件。
func parseUsageStatsQuery(r *http.Request, accountID uint64) (store.UsageStatsQuery, error) {
	params := r.URL.Query()
	since, err := parseMoment(params.Get(sinceQueryParam))
	if err != nil {
		return store.UsageStatsQuery{}, err
	}
	until, err := parseMoment(params.Get(untilQueryParam))
	if err != nil {
		return store.UsageStatsQuery{}, err
	}
	groupBy := strings.TrimSpace(params.Get(groupByQueryParam))
	if groupBy == "" {
		groupBy = defaultGroupBy
	}
	if _, ok := usageStatsGroups[groupBy]; !ok {
		return store.UsageStatsQuery{}, errInvalidUsageGroupBy
	}
	apiKeyID, err := parseAPIKeyID(params.Get(apiKeyQueryParam))
	if err != nil {
		return store.UsageStatsQuery{}, err
	}
	return store.UsageStatsQuery{
		AccountID:      accountID,
		Since:          since,
		Until:          until,
		RequestedModel: strings.TrimSpace(params.Get(modelQueryParam)),
		APIKeyID:       apiKeyID,
		GroupBy:        groupBy,
	}, nil
}
