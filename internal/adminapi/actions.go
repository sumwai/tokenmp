package adminapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件是上游账号的配置动作：商家、渠道、凭据与渠道模型映射。
//
// 写关系固定为「解析 → 业务动作 → 错误码翻译」：本包不拼 SQL、不构造存储行，
// 校验、默认值与派生计算只在 internal/admin 里，页面与 CLI 因此走同一条路径。
//
// 形状校验在解析之后再做一遍，与契约的 required 与 maxLength 对应：这样页面的
// 字段级提示不依赖业务层内部字段名，同时把明显不合法请求挡在数据库之前。
// 业务层的 admin.ErrInvalidInput 仍是兜底，它覆盖形状之外的口径（渠道 config 结构、
// 请求头保留名、模型映射 overrides 等）。
//
// 动作按主键置位，不区分行是否存在：存储层的 UPDATE 不影响任何行时也不报错，
// 与 CLI 同语义（幂等，重复调用返回成功）。

// 请求体上限与字段长度上限，与契约里对应字段的 maxLength 一致。
const (
	maxBodyBytes         = 32 << 10
	maxNameRunes         = 64
	maxCodeRunes         = 64
	maxURLRunes          = 255
	maxModelRunes        = 128
	maxMerchantNameRunes = 128
)

// 动作尾段：/<集合>/{id}/<尾段>。
const (
	enableAction  = "enable"
	disableAction = "disable"
	ownerAction   = "owner"
)

// createdID 是写入动作的响应数据，字段与契约 AdminResourceID 对齐。
type createdID struct {
	ID uint64 `json:"id"`
}

// merchantRequest 是新建商家的请求体，字段与契约 CreateAdminMerchantRequest 对齐。
type merchantRequest struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// ownerRequest 是绑定商家归属的请求体，字段与契约 SetAdminMerchantOwnerRequest 对齐。
type ownerRequest struct {
	UserID uint64 `json:"user_id"`
}

// channelRequest 是新建渠道的请求体，字段与契约 CreateAdminChannelRequest 对齐。
//
// Config 保原始 JSON：渠道级扩展配置的键由选路边界解释，本层只透传原文。
type channelRequest struct {
	MerchantID      uint64          `json:"merchant_id"`
	Name            string          `json:"name"`
	Vendor          string          `json:"vendor"`
	Type            string          `json:"type"`
	CredGroup       string          `json:"cred_group"`
	BaseURL         string          `json:"base_url"`
	CredentialStyle string          `json:"credential_style"`
	Config          json.RawMessage `json:"config"`
	Priority        int             `json:"priority"`
	Weight          int             `json:"weight"`
}

// credentialRequest 是写入凭据的请求体，字段与契约 CreateAdminCredentialRequest 对齐。
type credentialRequest struct {
	MerchantID uint64 `json:"merchant_id"`
	CredGroup  string `json:"cred_group"`
	Name       string `json:"name"`
	APIKey     string `json:"api_key"`
}

// modelMapRequest 是写入模型映射的请求体，字段与契约 SetAdminModelMapRequest 对齐。
type modelMapRequest struct {
	ChannelID        uint64          `json:"channel_id"`
	Model            string          `json:"model"`
	UpstreamModel    string          `json:"upstream_model"`
	PriceMultiplier  string          `json:"price_multiplier"`
	RequestOverrides json.RawMessage `json:"request_overrides"`
}

// handleMerchants 处理商家集合：GET 清单，POST 新建。
func (h *Handler) handleMerchants(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listMerchants(w, r)
	case http.MethodPost:
		h.createMerchant(w, r)
	default:
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 与 POST 方法")
	}
}

// handleMerchantAction 处理商家的停用与归属绑定：/merchants/{id}/disable 与
// /merchants/{id}/owner。
func (h *Handler) handleMerchantAction(w http.ResponseWriter, r *http.Request) {
	id, action, ok := parseSubresource(r.URL.Path, MerchantsPath)
	if !ok {
		notFound(w)
		return
	}
	if !h.requireWriter(w) {
		return
	}
	if !requirePost(w, r) {
		return
	}
	switch action {
	case disableAction:
		if err := h.writer.DisableMerchant(r.Context(), id); err != nil {
			writeActionError(w, err)
			return
		}
		webapi.WriteOK(w, nil)
	case ownerAction:
		h.setMerchantOwner(w, r, id)
	default:
		notFound(w)
	}
}

// handleChannelAction 处理渠道的启停：/channels/{id}/enable 与 /channels/{id}/disable。
func (h *Handler) handleChannelAction(w http.ResponseWriter, r *http.Request) {
	id, action, ok := parseSubresource(r.URL.Path, ChannelsPath)
	if !ok {
		notFound(w)
		return
	}
	if !h.requireWriter(w) {
		return
	}
	if !requirePost(w, r) {
		return
	}
	var err error
	switch action {
	case enableAction:
		err = h.writer.EnableChannel(r.Context(), id)
	case disableAction:
		err = h.writer.DisableChannel(r.Context(), id)
	default:
		notFound(w)
		return
	}
	if err != nil {
		writeActionError(w, err)
		return
	}
	webapi.WriteOK(w, nil)
}

// handleCredentialAction 处理凭据的启停：/credentials/{id}/enable 与 /credentials/{id}/disable。
func (h *Handler) handleCredentialAction(w http.ResponseWriter, r *http.Request) {
	id, action, ok := parseSubresource(r.URL.Path, CredentialsPath)
	if !ok {
		notFound(w)
		return
	}
	if !h.requireWriter(w) {
		return
	}
	if !requirePost(w, r) {
		return
	}
	var err error
	switch action {
	case enableAction:
		err = h.writer.EnableCredential(r.Context(), id)
	case disableAction:
		err = h.writer.DisableCredential(r.Context(), id)
	default:
		notFound(w)
		return
	}
	if err != nil {
		writeActionError(w, err)
		return
	}
	webapi.WriteOK(w, nil)
}

// handleModelMapAction 处理模型映射的停用：/modelmaps/{id}/disable。
//
// 没有 enable：模型映射的启用走写入端点（PUT 覆盖时启用位由存储层的 upsert 置回）。
func (h *Handler) handleModelMapAction(w http.ResponseWriter, r *http.Request) {
	id, action, ok := parseSubresource(r.URL.Path, ModelMapsPath)
	if !ok {
		notFound(w)
		return
	}
	if !h.requireWriter(w) {
		return
	}
	if !requirePost(w, r) {
		return
	}
	if action != disableAction {
		notFound(w)
		return
	}
	if err := h.writer.DisableModelMap(r.Context(), id); err != nil {
		writeActionError(w, err)
		return
	}
	webapi.WriteOK(w, nil)
}

// createMerchant 新建一个商家，新建一律 active。
func (h *Handler) createMerchant(w http.ResponseWriter, r *http.Request) {
	if !h.requireWriter(w) {
		return
	}
	var req merchantRequest
	if err := decodeJSON(r, &req); err != nil {
		badBody(w)
		return
	}
	code := strings.TrimSpace(req.Code)
	name := strings.TrimSpace(req.Name)
	kind := store.MerchantKind(strings.TrimSpace(req.Kind))
	switch {
	case code == "" || len([]rune(code)) > maxCodeRunes:
		badRequest(w, "商家编码必填且不超过 64 个字符")
		return
	case name == "" || len([]rune(name)) > maxMerchantNameRunes:
		badRequest(w, "商家名必填且不超过 128 个字符")
		return
	case store.ValidateMerchantKind(kind) != nil:
		badRequest(w, "商家类型只支持 platform 与 partner")
		return
	}
	id, err := h.writer.CreateMerchant(r.Context(), code, name, kind)
	if err != nil {
		writeActionError(w, err)
		return
	}
	webapi.WriteOK(w, createdID{ID: id})
}

// setMerchantOwner 把商家绑定到一个登录主体，商家域的作用域由此推导。
func (h *Handler) setMerchantOwner(w http.ResponseWriter, r *http.Request, id uint64) {
	var req ownerRequest
	if err := decodeJSON(r, &req); err != nil {
		badBody(w)
		return
	}
	if req.UserID == 0 {
		badRequest(w, "user_id 必须是正整数")
		return
	}
	if err := h.writer.SetMerchantOwner(r.Context(), id, req.UserID); err != nil {
		writeActionError(w, err)
		return
	}
	webapi.WriteOK(w, nil)
}

// createChannel 新建一条上游渠道。priority 与 weight 为 0 时由业务层取默认值 100：
// 0 会让渠道永远排在最后，不能当作「未配置」。
func (h *Handler) createChannel(w http.ResponseWriter, r *http.Request) {
	if !h.requireWriter(w) {
		return
	}
	var req channelRequest
	if err := decodeJSON(r, &req); err != nil {
		badBody(w)
		return
	}
	name := strings.TrimSpace(req.Name)
	vendor := strings.TrimSpace(req.Vendor)
	credGroup := strings.TrimSpace(req.CredGroup)
	baseURL := strings.TrimSpace(req.BaseURL)
	channelType := store.ChannelType(strings.TrimSpace(req.Type))
	style := domain.CredentialHeaderStyle(strings.TrimSpace(req.CredentialStyle))
	switch {
	case req.MerchantID == 0:
		badRequest(w, "merchant_id 必须是正整数")
		return
	case name == "" || len([]rune(name)) > maxNameRunes:
		badRequest(w, "渠道名必填且不超过 64 个字符")
		return
	case vendor != "" && len([]rune(vendor)) > maxNameRunes:
		badRequest(w, "厂商标签不超过 64 个字符")
		return
	case store.ValidateChannelType(channelType) != nil:
		badRequest(w, "协议方言不受支持")
		return
	case credGroup == "" || len([]rune(credGroup)) > maxNameRunes:
		badRequest(w, "凭据分组必填且不超过 64 个字符")
		return
	case baseURL == "" || len([]rune(baseURL)) > maxURLRunes:
		badRequest(w, "上游基地址必填且不超过 255 个字符")
		return
	case req.CredentialStyle != "" && !style.Valid():
		badRequest(w, "凭据注入形态不受支持")
		return
	case req.Priority < 0 || req.Weight < 0:
		badRequest(w, "优先级与权重不能为负数")
		return
	}
	id, err := h.writer.CreateChannel(r.Context(), admin.ChannelInput{
		MerchantID:      req.MerchantID,
		Name:            name,
		Vendor:          vendor,
		Type:            channelType,
		CredGroup:       credGroup,
		BaseURL:         baseURL,
		Priority:        req.Priority,
		Weight:          req.Weight,
		Config:          string(optionalJSON(req.Config)),
		CredentialStyle: style,
	})
	if err != nil {
		writeActionError(w, err)
		return
	}
	webapi.WriteOK(w, createdID{ID: id})
}

// createCredential 写入一行上游凭据，明文只在请求里出现，响应不回显。
func (h *Handler) createCredential(w http.ResponseWriter, r *http.Request) {
	if !h.requireWriter(w) {
		return
	}
	var req credentialRequest
	if err := decodeJSON(r, &req); err != nil {
		badBody(w)
		return
	}
	credGroup := strings.TrimSpace(req.CredGroup)
	name := strings.TrimSpace(req.Name)
	// 明文去掉首尾空白：从终端或密码管理器复制出来的值常带换行，留着只会让上游
	// 认证失败，而失败现象与「配错了」无从区分。
	apiKey := strings.TrimSpace(req.APIKey)
	switch {
	case req.MerchantID == 0:
		badRequest(w, "merchant_id 必须是正整数")
		return
	case credGroup == "" || len([]rune(credGroup)) > maxNameRunes:
		badRequest(w, "凭据分组必填且不超过 64 个字符")
		return
	case name == "" || len([]rune(name)) > maxNameRunes:
		badRequest(w, "凭据名必填且不超过 64 个字符")
		return
	case apiKey == "":
		badRequest(w, "凭据明文必填")
		return
	}
	id, err := h.writer.AddCredential(r.Context(), admin.CredentialInput{
		MerchantID: req.MerchantID,
		Group:      credGroup,
		Name:       name,
		APIKey:     apiKey,
	})
	if err != nil {
		writeActionError(w, err)
		return
	}
	webapi.WriteOK(w, createdID{ID: id})
}

// setModelMap 写入或覆盖一条渠道模型映射。
//
// 不带行 id：按 (channel_id, model) 的 upsert 命中已有行时取不到主键，回一个不可靠的
// id 比不回更糟。写入后从清单取。
func (h *Handler) setModelMap(w http.ResponseWriter, r *http.Request) {
	if !h.requireWriter(w) {
		return
	}
	var req modelMapRequest
	if err := decodeJSON(r, &req); err != nil {
		badBody(w)
		return
	}
	model := strings.TrimSpace(req.Model)
	upstreamModel := strings.TrimSpace(req.UpstreamModel)
	switch {
	case req.ChannelID == 0:
		badRequest(w, "channel_id 必须是正整数")
		return
	case model == "" || len([]rune(model)) > maxModelRunes:
		badRequest(w, "模型名必填且不超过 128 个字符")
		return
	case upstreamModel == "" || len([]rune(upstreamModel)) > maxModelRunes:
		badRequest(w, "上游模型名必填且不超过 128 个字符")
		return
	}
	if _, err := h.writer.SetModelMap(r.Context(), admin.ModelMapInput{
		ChannelID:        req.ChannelID,
		Model:            model,
		UpstreamModel:    upstreamModel,
		PriceMultiplier:  strings.TrimSpace(req.PriceMultiplier),
		RequestOverrides: optionalJSON(req.RequestOverrides),
	}); err != nil {
		writeActionError(w, err)
		return
	}
	webapi.WriteOK(w, nil)
}

// writeActionError 把业务动作的错误翻成页面信封。
//
// 输入不合法的文案来自业务层，带 `admin:` 前缀，去掉前缀后原样回给页面：那些句子
// 本来就是给操作者看的（「配置的请求头由网关自身占用」这类），换一句笼统的
// 「参数不合法」只会让管理员去翻服务端日志。唯一键冲突单独回 409，其余不外漏
// 驱动文案，一律回 500。
func writeActionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, admin.ErrInvalidInput):
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest,
			strings.TrimPrefix(err.Error(), "admin: "))
	case errors.Is(err, store.ErrConflict):
		webapi.WriteError(w, http.StatusConflict, webapi.CodeConflict, "同名记录已存在")
	default:
		writeInternal(w)
	}
}

// parseSubresource 解析 /<集合>/{id}/<尾段>，返回主键与尾段；形状不匹配返回 ok=false。
func parseSubresource(path, collection string) (uint64, string, bool) {
	rest, found := strings.CutPrefix(path, collection+"/")
	if !found {
		return 0, "", false
	}
	idPart, action, found := strings.Cut(rest, "/")
	if !found || idPart == "" || action == "" || strings.Contains(action, "/") {
		return 0, "", false
	}
	id, err := strconv.ParseUint(idPart, 10, 64)
	if err != nil || id == 0 {
		return 0, "", false
	}
	return id, action, true
}

// optionalJSON 把显式的 null 与空白当作「未给」。
//
// 契约里渠道 config 与模型映射 overrides 都是可选对象；客户端序列化一个未填的值
// 时会得到 null，把它当成配错会在页面上留下无从解释的 400。
func optionalJSON(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	return trimmed
}

// decodeJSON 解析请求体并拒绝未知字段：契约之外的字段静默忽略会让拼写错误变成
// 「提交成功但没生效」。
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// requirePost 只接受 POST：动作端点没有别的语义。
func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 POST 方法")
		return false
	}
	return true
}

// badRequest 写一条 400，文案是字段级的页面提示。
func badRequest(w http.ResponseWriter, message string) {
	webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, message)
}

// badBody 写请求体解析失败的 400。
func badBody(w http.ResponseWriter) {
	webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "请求体格式非法")
}

// notFound 写子树内未声明的路径的 404。
func notFound(w http.ResponseWriter) {
	webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "端点不存在")
}
