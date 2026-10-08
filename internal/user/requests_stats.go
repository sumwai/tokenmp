package user

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件实现账户面的请求记录聚合。
//
// 它只回答计数，不返回单条记录：按 request_id 定位具体请求走详情端点。
// 数据源是随明细同步维护的按天缓存，因此明细超期后同一区间仍可查。

const (
	// groupByQueryParam 是聚合的分组维度参数。
	groupByQueryParam = "group_by"
	// defaultGroupBy 是未指定分组维度时的取值。
	defaultGroupBy = "day"
)

// requestStatsGroups 是分组维度的取值集合，与契约的枚举一致。
var requestStatsGroups = map[string]struct{}{
	"day":    {},
	"model":  {},
	"status": {},
}

// errInvalidGroupBy 是分组维度非法时的错误，文案直接写回信封。
var errInvalidGroupBy = errors.New("group_by 取值必须是 day / model / status")

// requestStatsItemView 是一个分组的计数，与契约 RequestStatsItem 对齐。
type requestStatsItemView struct {
	// Key 是分组值：日期（YYYY-MM-DD）、模型名或终态。
	Key       string `json:"key"`
	Total     int64  `json:"total"`
	Success   int64  `json:"success"`
	Failed    int64  `json:"failed"`
	Cancelled int64  `json:"cancelled"`
}

// handleRequestStats 返回当前账户的请求计数聚合。
func (h *Handler) handleRequestStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 方法")
		return
	}
	account, ok := h.currentAccount(w, r)
	if !ok {
		return
	}
	query, err := parseRequestStatsQuery(r, account.ID)
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	items, err := h.store.RequestStats(r.Context(), query)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "请求聚合查询失败")
		return
	}
	views := make([]requestStatsItemView, 0, len(items))
	for _, item := range items {
		views = append(views, requestStatsItemView{
			Key:       item.Key,
			Total:     item.Total,
			Success:   item.Success,
			Failed:    item.Failed,
			Cancelled: item.Cancelled,
		})
	}
	// 聚合结果不分页：分组维度下的取值数量由维度自身决定（日期、模型或三种终态），
	// 不是可增长的数据集合。分页三字段照填「一页含全部」，与其余列表端点同形。
	total := len(views)
	webapi.WritePage(w, map[string]any{itemsKey: views}, 1, total, total)
}

// parseRequestStatsQuery 解析聚合查询的过滤与分组条件。
func parseRequestStatsQuery(r *http.Request, accountID uint64) (store.RequestStatsQuery, error) {
	params := r.URL.Query()
	since, err := parseMoment(params.Get(sinceQueryParam))
	if err != nil {
		return store.RequestStatsQuery{}, err
	}
	until, err := parseMoment(params.Get(untilQueryParam))
	if err != nil {
		return store.RequestStatsQuery{}, err
	}
	groupBy := strings.TrimSpace(params.Get(groupByQueryParam))
	if groupBy == "" {
		groupBy = defaultGroupBy
	}
	if _, ok := requestStatsGroups[groupBy]; !ok {
		return store.RequestStatsQuery{}, errInvalidGroupBy
	}
	apiKeyID, err := parseAPIKeyID(params.Get(apiKeyQueryParam))
	if err != nil {
		return store.RequestStatsQuery{}, err
	}
	return store.RequestStatsQuery{
		AccountID:      accountID,
		Since:          since,
		Until:          until,
		RequestedModel: strings.TrimSpace(params.Get(modelQueryParam)),
		APIKeyID:       apiKeyID,
		GroupBy:        groupBy,
	}, nil
}
