package partner

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/credential"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件实现商家域的各动作：上游渠道与凭据的列出、登记与启停，以及名下用量的聚合。
//
// 作用域一律取自会话推导出的商家，请求体与路径都不接受商家参数。

const (
	// enableSuffix 是渠道与凭据启用的路径尾段。
	enableSuffix = "/enable"
	// disableSuffix 是渠道与凭据停用的路径尾段。
	disableSuffix = "/disable"
	// itemsKey 是列表数据的字段名，与其余列表端点同名。
	itemsKey = "items"
	// maxBodyBytes 是写端点的请求体上限：字段只有几个短值，放大上限只会给
	// 「用大 body 撑爆解析」留空间。
	maxBodyBytes = 8 << 10
	// maxNameRunes 是渠道名与凭据名的字符数上限，与契约的 maxLength 一致。
	maxNameRunes = 64
	// maxGroupRunes 是凭据分组名的字符数上限，与列的宽度一致。
	maxGroupRunes = 64
)

// 查询参数名。取值与契约一致，改动须同步契约。
const (
	pageQueryParam    = "page"
	sizeQueryParam    = "size"
	enabledQueryParam = "enabled"
	sinceQueryParam   = "since"
	untilQueryParam   = "until"
	modelQueryParam   = "model"
	apiKeyQueryParam  = "api_key_id"
	groupByQueryParam = "group_by"
)

// 分页与聚合的默认值与上限，与契约的 Page / Size / group_by 声明一致。
const (
	defaultPageSize = 20
	maxPageSize     = 100
	// defaultGroupBy 是未指定分组维度时的取值，与账户面同口径。
	defaultGroupBy = "day"
)

// 商家域的错误文案，直接写回信封。
var (
	errInvalidPaging    = errors.New("page 与 size 必须是正整数，size 上限 100")
	errInvalidTimeRange = errors.New("since 与 until 必须是 RFC3339 时刻")
	errInvalidGroupBy   = errors.New("group_by 取值必须是 day / model / api_key")
)

// usageStatsGroups 是分组维度的取值集合，与契约的枚举一致。
var usageStatsGroups = map[string]struct{}{
	"day":     {},
	"model":   {},
	"api_key": {},
}

// subject 是会话解析出的登录主体：作用域推导用 id，身份集合用于判定商家身份。
type subject struct {
	userID uint64
	roles  []string
}

// channelView 是一条上游渠道的对外形状，字段与契约 PartnerChannel 对齐。
//
// 不含 merchant_id：它就是会话本身，回显只是把内部标识漏给页面。
type channelView struct {
	ID        uint64 `json:"id"`
	Name      string `json:"name"`
	Vendor    string `json:"vendor"`
	Type      string `json:"type"`
	CredGroup string `json:"cred_group"`
	BaseURL   string `json:"base_url"`
	Priority  int    `json:"priority"`
	Weight    int    `json:"weight"`
	Enabled   bool   `json:"enabled"`
}

// credentialView 是一条上游凭据的对外形状，字段与契约 PartnerCredential 对齐。
//
// 只有前缀：明文在库里可读，但页面只在创建那一次见到它。
type credentialView struct {
	ID        uint64 `json:"id"`
	CredGroup string `json:"cred_group"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	Enabled   bool   `json:"enabled"`
}

// createdCredentialView 是凭据的创建响应：在视图上追加一次性明文。
type createdCredentialView struct {
	credentialView
	Secret string `json:"secret"`
}

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

// usageStatsItemView 是一个分组的用量合计，字段与契约 UsageStatsItem 对齐。
type usageStatsItemView struct {
	// Key 是分组值：日期（YYYY-MM-DD）、模型名或密钥 id 的十进制文本。
	Key string `json:"key"`
	// Calls 是区间内成功履约的请求次数。
	Calls int64 `json:"calls"`
	// Usage 是各 token 分量的合计。
	Usage usageTokensView `json:"usage"`
	// ChargedAmount 是应扣量合计，十进制字符串。
	ChargedAmount string `json:"charged_amount"`
}

// createChannelRequest 是登记渠道的请求体，字段与契约 CreatePartnerChannelRequest 对齐。
type createChannelRequest struct {
	Name            string `json:"name"`
	Vendor          string `json:"vendor"`
	Type            string `json:"type"`
	CredGroup       string `json:"cred_group"`
	BaseURL         string `json:"base_url"`
	CredentialStyle string `json:"credential_style"`
	Priority        int    `json:"priority"`
	Weight          int    `json:"weight"`
}

// createCredentialRequest 是登记凭据的请求体，字段与契约 CreatePartnerCredentialRequest 对齐。
type createCredentialRequest struct {
	CredGroup string `json:"cred_group"`
	Name      string `json:"name"`
	APIKey    string `json:"api_key"`
}

// ServeHTTP 按路径分发到各动作；子树内未声明的路径回页面信封 404。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == ChannelsPath:
		h.handleChannels(w, r)
	case strings.HasPrefix(r.URL.Path, ChannelsPath+"/"):
		h.handleChannelAction(w, r)
	case r.URL.Path == CredentialsPath:
		h.handleCredentials(w, r)
	case strings.HasPrefix(r.URL.Path, CredentialsPath+"/"):
		h.handleCredentialAction(w, r)
	case r.URL.Path == UsageStatsPath:
		h.handleUsageStats(w, r)
	case r.URL.Path == SettlementPath:
		h.handleSettlement(w, r)
	default:
		webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "端点不存在")
	}
}

// handleChannels 处理渠道集合：GET 列表，POST 登记。
func (h *Handler) handleChannels(w http.ResponseWriter, r *http.Request) {
	merchant, ok := h.currentMerchant(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listChannels(w, r, merchant.ID)
	case http.MethodPost:
		h.createChannel(w, r, merchant.ID)
	default:
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 与 POST 方法")
	}
}

// handleCredentials 处理凭据集合：GET 列表，POST 登记。
func (h *Handler) handleCredentials(w http.ResponseWriter, r *http.Request) {
	merchant, ok := h.currentMerchant(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listCredentials(w, r, merchant.ID)
	case http.MethodPost:
		h.createCredential(w, r, merchant.ID)
	default:
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 与 POST 方法")
	}
}

// handleChannelAction 处理渠道的启停：/channels/{id}/enable|disable。
func (h *Handler) handleChannelAction(w http.ResponseWriter, r *http.Request) {
	id, enabled, ok := parseActionPath(r.URL.Path, ChannelsPath)
	if !ok {
		webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "端点不存在")
		return
	}
	merchant, ok := h.currentMerchant(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 POST 方法")
		return
	}
	hit, err := h.store.SetChannelEnabledForMerchant(r.Context(), id, merchant.ID, enabled)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "服务端错误")
		return
	}
	// 不属于本商家的渠道按不存在处理：以 403 区分会让渠道 id 可被探测。
	if !hit {
		webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "渠道不存在")
		return
	}
	webapi.WriteOK(w, nil)
}

// handleCredentialAction 处理凭据的启停：/credentials/{id}/enable|disable。
func (h *Handler) handleCredentialAction(w http.ResponseWriter, r *http.Request) {
	id, enabled, ok := parseActionPath(r.URL.Path, CredentialsPath)
	if !ok {
		webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "端点不存在")
		return
	}
	merchant, ok := h.currentMerchant(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 POST 方法")
		return
	}
	hit, err := h.store.SetCredentialEnabledForMerchant(r.Context(), id, merchant.ID, enabled)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "服务端错误")
		return
	}
	if !hit {
		webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "凭据不存在")
		return
	}
	webapi.WriteOK(w, nil)
}

// listChannels 按偏移分页返回本商家的渠道。
func (h *Handler) listChannels(w http.ResponseWriter, r *http.Request, merchantID uint64) {
	page, size, err := parsePaging(r.URL.Query())
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	enabled, err := parseEnabled(r.URL.Query().Get(enabledQueryParam))
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	channels, total, err := h.store.ChannelsByMerchant(r.Context(), merchantID, enabled, size, (page-1)*size)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "渠道查询失败")
		return
	}
	items := make([]channelView, 0, len(channels))
	for i := range channels {
		items = append(items, newChannelView(&channels[i]))
	}
	webapi.WritePage(w, map[string]any{itemsKey: items}, page, size, total)
}

// listCredentials 按偏移分页返回本商家的凭据，只给出脱敏前缀。
func (h *Handler) listCredentials(w http.ResponseWriter, r *http.Request, merchantID uint64) {
	page, size, err := parsePaging(r.URL.Query())
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	rows, total, err := h.store.CredentialsByMerchant(r.Context(), merchantID, size, (page-1)*size)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "凭据查询失败")
		return
	}
	items := make([]credentialView, 0, len(rows))
	for i := range rows {
		items = append(items, newCredentialView(&rows[i]))
	}
	webapi.WritePage(w, map[string]any{itemsKey: items}, page, size, total)
}

// createChannel 为本商家登记一条上游渠道。
//
// 形状校验在页面层先做一遍，登记动作复用管理面：校验失败因此回 400（而不是把管理面的
// 错误文案当服务端故障），唯一键冲突回 409，其余回 500。
func (h *Handler) createChannel(w http.ResponseWriter, r *http.Request, merchantID uint64) {
	var req createChannelRequest
	if err := decodeJSON(r, &req); err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "请求体格式非法")
		return
	}
	name := strings.TrimSpace(req.Name)
	credGroup := strings.TrimSpace(req.CredGroup)
	baseURL := strings.TrimSpace(req.BaseURL)
	vendor := strings.TrimSpace(req.Vendor)
	channelType := store.ChannelType(strings.TrimSpace(req.Type))
	style := domain.CredentialHeaderStyle(strings.TrimSpace(req.CredentialStyle))
	switch {
	case name == "" || len([]rune(name)) > maxNameRunes:
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "渠道名必填且不超过 64 个字符")
		return
	case vendor != "" && len([]rune(vendor)) > maxNameRunes:
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "厂商标签不超过 64 个字符")
		return
	case store.ValidateChannelType(channelType) != nil:
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "协议方言不受支持")
		return
	case credGroup == "" || len([]rune(credGroup)) > maxGroupRunes:
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "凭据分组必填且不超过 64 个字符")
		return
	case baseURL == "":
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "上游根地址必填")
		return
	case req.CredentialStyle != "" && !style.Valid():
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "凭据注入形态不受支持")
		return
	}
	config, err := channelConfig(style)
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	id, err := h.store.InsertChannel(r.Context(), store.Channel{
		MerchantID: merchantID,
		Name:       name,
		Vendor:     vendor,
		Type:       channelType,
		CredGroup:  credGroup,
		BaseURL:    baseURL,
		Priority:   defaultOr(req.Priority, defaultChannelPriority),
		Weight:     defaultOr(req.Weight, defaultChannelWeight),
		Config:     config,
	})
	if err != nil {
		writeWriteError(w, err, "渠道登记失败")
		return
	}
	// 新建的渠道一律启用：InsertChannel 的 SQL 固定写 enabled = 1。
	webapi.WriteOK(w, channelView{
		ID:        id,
		Name:      name,
		Vendor:    vendor,
		Type:      string(channelType),
		CredGroup: credGroup,
		BaseURL:   baseURL,
		Priority:  defaultOr(req.Priority, defaultChannelPriority),
		Weight:    defaultOr(req.Weight, defaultChannelWeight),
		Enabled:   true,
	})
}

// createCredential 为本商家登记一条上游凭据，明文只在本次响应出现。
func (h *Handler) createCredential(w http.ResponseWriter, r *http.Request, merchantID uint64) {
	var req createCredentialRequest
	if err := decodeJSON(r, &req); err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "请求体格式非法")
		return
	}
	credGroup := strings.TrimSpace(req.CredGroup)
	name := strings.TrimSpace(req.Name)
	apiKey := strings.TrimSpace(req.APIKey)
	switch {
	case credGroup == "" || len([]rune(credGroup)) > maxGroupRunes:
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "凭据分组必填且不超过 64 个字符")
		return
	case name == "" || len([]rune(name)) > maxNameRunes:
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "凭据名必填且不超过 64 个字符")
		return
	case apiKey == "":
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "凭据明文必填")
		return
	}
	secret, err := credential.BuildAPISecret(apiKey)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "服务端错误")
		return
	}
	id, err := h.store.InsertCredential(r.Context(), store.CredentialRow{
		MerchantID: merchantID,
		CredGroup:  credGroup,
		Name:       name,
		Secret:     secret,
	})
	if err != nil {
		writeWriteError(w, err, "凭据登记失败")
		return
	}
	// 新建的凭据一律启用：InsertCredential 的 SQL 固定写 enabled = 1。
	webapi.WriteOK(w, createdCredentialView{
		credentialView: credentialView{
			ID: id, CredGroup: credGroup, Name: name,
			Prefix: credential.MaskSecret(secret), Enabled: true,
		},
		Secret: apiKey,
	})
}

// handleUsageStats 返回本商家名下渠道的用量聚合。
func (h *Handler) handleUsageStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 方法")
		return
	}
	merchant, ok := h.currentMerchant(w, r)
	if !ok {
		return
	}
	query, err := parseUsageStatsQuery(r, merchant.ID)
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	items, err := h.store.MerchantUsageStats(r.Context(), query)
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
	// 不是可增长的数据集合。分页三字段照填「一页含全部」，与账户面同形。
	total := len(views)
	webapi.WritePage(w, map[string]any{itemsKey: views}, 1, total, total)
}

// currentSubject 从会话推导登录主体；会话无效回 401。
func (h *Handler) currentSubject(w http.ResponseWriter, r *http.Request) (subject, bool) {
	token, ok := webapi.BearerToken(r.Header.Get("Authorization"))
	if !ok {
		webapi.WriteError(w, http.StatusUnauthorized, webapi.CodeUnauthorized, "登录状态已失效")
		return subject{}, false
	}
	userID, roles, err := h.sessions.SessionSubject(r.Context(), token)
	if err != nil {
		webapi.WriteError(w, http.StatusUnauthorized, webapi.CodeUnauthorized, "登录状态已失效")
		return subject{}, false
	}
	return subject{userID: userID, roles: roles}, true
}

// currentMerchant 在会话之上推导本商家：没有商家身份回 403，商家未开通回 403。
//
// 两者都不折叠成 401：会话是有效的，下一步动作是「等平台开通」而不是「重新登录」。
// 非本人商家的资源由存储层的谓词拦成 404，这里不参与归属判定。
func (h *Handler) currentMerchant(w http.ResponseWriter, r *http.Request) (*store.Merchant, bool) {
	sub, ok := h.currentSubject(w, r)
	if !ok {
		return nil, false
	}
	if !slices.Contains(sub.roles, store.RolePartner) {
		webapi.WriteError(w, http.StatusForbidden, webapi.CodeForbidden, "需要商家身份")
		return nil, false
	}
	merchant, err := h.store.MerchantByOwner(r.Context(), sub.userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			webapi.WriteError(w, http.StatusForbidden, webapi.CodeForbidden, "商家尚未开通")
			return nil, false
		}
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "服务端错误")
		return nil, false
	}
	return merchant, true
}

// newChannelView 把存储行裁剪为对外视图。
func newChannelView(c *store.Channel) channelView {
	return channelView{
		ID:        c.ID,
		Name:      c.Name,
		Vendor:    c.Vendor,
		Type:      string(c.Type),
		CredGroup: c.CredGroup,
		BaseURL:   c.BaseURL,
		Priority:  c.Priority,
		Weight:    c.Weight,
		Enabled:   c.Enabled,
	}
}

// newCredentialView 把存储行裁剪为对外视图，secret 只经脱敏前缀出现。
func newCredentialView(c *store.CredentialRow) credentialView {
	return credentialView{
		ID:        c.ID,
		CredGroup: c.CredGroup,
		Name:      c.Name,
		Prefix:    credential.MaskSecret(c.Secret),
		Enabled:   c.Enabled,
	}
}

// usageTokensFromMetrics 把存储层的指标合计映射为契约形状。
func usageTokensFromMetrics(metrics map[string]int64) usageTokensView {
	return usageTokensView{
		InputTokens:        int(metrics[string(billing.MetricInputToken)]),
		OutputTokens:       int(metrics[string(billing.MetricOutputToken)]),
		CacheReadTokens:    int(metrics[string(billing.MetricCacheReadToken)]),
		CacheWriteTokens:   int(metrics[string(billing.MetricCacheWriteToken)]),
		CacheWrite5mTokens: int(metrics[string(billing.MetricCacheWrite5m)]),
		CacheWrite1hTokens: int(metrics[string(billing.MetricCacheWrite1h)]),
		ReasoningTokens:    int(metrics[string(billing.MetricReasoningToken)]),
		ServerToolUses:     int(metrics[serverToolUsesMetric]),
	}
}

// serverToolUsesMetric 是服务端工具执行次数的用量键，与账户面同名同源。
const serverToolUsesMetric = "server_tool_uses"

// writeWriteError 把登记动作的错误翻成页面信封：唯一键冲突回 409，其余回 500。
//
// 形状校验已在调用前完成，到这里还不合法只可能是唯一键冲突或真的服务端故障 ——
// 前者是可判定的（store.ErrConflict），后者不能把驱动文案漏给页面。
func writeWriteError(w http.ResponseWriter, err error, message string) {
	if errors.Is(err, store.ErrConflict) {
		webapi.WriteError(w, http.StatusConflict, webapi.CodeConflict, message+"：同名记录已存在")
		return
	}
	webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "服务端错误")
}

// parseActionPath 解析 /<集合>/{id}/enable|disable；不匹配返回 ok=false。
func parseActionPath(path, collection string) (uint64, bool, bool) {
	rest, found := strings.CutPrefix(path, collection+"/")
	if !found {
		return 0, false, false
	}
	enabled := false
	switch {
	case strings.HasSuffix(rest, enableSuffix):
		enabled = true
		rest = strings.TrimSuffix(rest, enableSuffix)
	case strings.HasSuffix(rest, disableSuffix):
		rest = strings.TrimSuffix(rest, disableSuffix)
	default:
		return 0, false, false
	}
	if rest == "" || strings.Contains(rest, "/") {
		return 0, false, false
	}
	id, err := strconv.ParseUint(rest, 10, 64)
	if err != nil || id == 0 {
		return 0, false, false
	}
	return id, enabled, true
}

// parseUsageStatsQuery 解析聚合查询的过滤与分组条件；作用域由调用方给定。
func parseUsageStatsQuery(r *http.Request, merchantID uint64) (store.MerchantUsageStatsQuery, error) {
	params := r.URL.Query()
	since, err := parseMoment(params.Get(sinceQueryParam))
	if err != nil {
		return store.MerchantUsageStatsQuery{}, err
	}
	until, err := parseMoment(params.Get(untilQueryParam))
	if err != nil {
		return store.MerchantUsageStatsQuery{}, err
	}
	groupBy := strings.TrimSpace(params.Get(groupByQueryParam))
	if groupBy == "" {
		groupBy = defaultGroupBy
	}
	if _, ok := usageStatsGroups[groupBy]; !ok {
		return store.MerchantUsageStatsQuery{}, errInvalidGroupBy
	}
	apiKeyID, err := parseAPIKeyID(params.Get(apiKeyQueryParam))
	if err != nil {
		return store.MerchantUsageStatsQuery{}, err
	}
	return store.MerchantUsageStatsQuery{
		MerchantID:     merchantID,
		Since:          since,
		Until:          until,
		RequestedModel: strings.TrimSpace(params.Get(modelQueryParam)),
		APIKeyID:       apiKeyID,
		GroupBy:        groupBy,
	}, nil
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

// parseAPIKeyID 解析密钥维度过滤参数；空串返回 0 表示不过滤。
func parseAPIKeyID(raw string) (uint64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	keyID, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil || keyID == 0 {
		return 0, errors.New("api_key_id 必须是正整数")
	}
	return keyID, nil
}

// parsePaging 解析 page 与 size：缺省 page=1、size=20，size 超上限截到上限。
func parsePaging(query url.Values) (int, int, error) {
	page, err := parsePositive(query.Get(pageQueryParam), 1)
	if err != nil {
		return 0, 0, err
	}
	size, err := parsePositive(query.Get(sizeQueryParam), defaultPageSize)
	if err != nil {
		return 0, 0, err
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	return page, size, nil
}

// parsePositive 解析正整数字符串；缺省取 fallback，非正数或非整数报错。
func parsePositive(raw string, fallback int) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil || value < 1 {
		return 0, errInvalidPaging
	}
	return value, nil
}

// parseEnabled 解析 enabled 过滤参数；缺省返回 nil 表示不过滤。
func parseEnabled(raw string) (*bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(trimmed)
	if err != nil {
		return nil, errors.New("enabled 必须是布尔值")
	}
	return &value, nil
}

// decodeJSON 解析请求体，拒绝未知字段：契约之外的字段静默忽略会让拼写错误
// 变成「提交成功但没生效」。
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// defaultOr 在取值未给时返回默认值，与列的 DEFAULT 一致。
func defaultOr(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

// channelConfig 把凭据注入形态收敛成落库的渠道 config。
//
// 页面不接受原始 config：渠道级扩展配置里可以有上游请求头，那属平台运维口径，
// 不由商家自助填写。键名与管理面同源（internal/admin 的 configFieldCredentialStyle）。
func channelConfig(style domain.CredentialHeaderStyle) (json.RawMessage, error) {
	if style == domain.CredentialHeaderAuto {
		return nil, nil
	}
	if !style.Valid() {
		return nil, errors.New("凭据注入形态不受支持")
	}
	return json.Marshal(map[string]string{configFieldCredentialStyle: string(style)})
}

// configFieldCredentialStyle 是渠道 config 里凭据注入形态的键名，与选路边界的读取口径同源。
//
//nolint:gosec // G101：这是渠道配置的键名，不是凭据。
const configFieldCredentialStyle = "credential_style"

// defaultChannelPriority / defaultChannelWeight 与 0001 迁移的列默认值一致：
// 回显的取值必须与真正落库的一致，管理面在同一处取同样的默认值。
const (
	defaultChannelPriority = 100
	defaultChannelWeight   = 100
)
