package user

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/apikey"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件实现客户端密钥的自助管理：列出、创建与吊销。
//
// 归属由会话推导，作用域内只有当前账户的密钥；请求体不接受账户或商家参数，
// 密钥归属写死为当前账户。

const (
	// revokeSuffix 是密钥吊销的路径尾段：/api/v1/user/keys/{id}/revoke。
	revokeSuffix = "/revoke"
	// enabledQueryParam 是密钥列表的启用状态过滤参数。
	enabledQueryParam = "enabled"
	// maxKeyNameRunes 是密钥名的字符数上限，与契约的 maxLength 一致。
	maxKeyNameRunes = 64
	// maxBodyBytes 是写端点的请求体上限：字段只有几个短值，
	// 放大上限只会给「用大 body 撑爆解析」留空间。
	maxBodyBytes = 8 << 10
)

// 分页默认值与上限，与契约的 Page / Size 参数一致。
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// errInvalidPaging 是 page / size 非法时的错误，文案直接写回信封。
var errInvalidPaging = errors.New("page 与 size 必须是正整数，size 上限 100")

// apiKeyView 是密钥的对外形状，字段与契约 ApiKey 对齐。
//
// 刻意不含 account_id 与 merchant_id：前者就是会话本身，后者是结算内部标识。
// KeyHash 不在结构体里，因此没有任何序列化路径能带出它。
type apiKeyView struct {
	ID         uint64     `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Enabled    bool       `json:"enabled"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// createdAPIKeyView 是创建响应：在密钥视图上追加一次性明文。
type createdAPIKeyView struct {
	apiKeyView
	Secret string `json:"secret"`
}

// newAPIKeyView 把存储行裁剪为对外视图。
func newAPIKeyView(k *store.APIKey) apiKeyView {
	return apiKeyView{
		ID:         k.ID,
		Name:       k.Name,
		KeyPrefix:  k.KeyPrefix,
		Enabled:    k.Enabled,
		ExpiresAt:  k.ExpiresAt,
		LastUsedAt: k.LastUsedAt,
	}
}

// createKeyRequest 是创建密钥的请求体，字段与契约 CreateApiKeyRequest 对齐。
type createKeyRequest struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// handleKeys 处理密钥集合上的列出与创建。
func (h *Handler) handleKeys(w http.ResponseWriter, r *http.Request) {
	account, ok := h.currentAccount(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listKeys(w, r, account)
	case http.MethodPost:
		h.createKey(w, r, account)
	default:
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 与 POST 方法")
	}
}

// listKeys 按偏移分页返回当前账户的密钥。
func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request, account *store.Account) {
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
	keys, total, err := h.store.ListAPIKeysByAccount(r.Context(), account.ID, enabled, size, (page-1)*size)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "密钥查询失败")
		return
	}
	items := make([]apiKeyView, 0, len(keys))
	for i := range keys {
		items = append(items, newAPIKeyView(&keys[i]))
	}
	webapi.WritePage(w, map[string]any{itemsKey: items}, page, size, total)
}

// createKey 为当前账户签发一把密钥，明文只在本次响应出现。
func (h *Handler) createKey(w http.ResponseWriter, r *http.Request, account *store.Account) {
	var req createKeyRequest
	if err := decodeJSON(r, &req); err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "请求体格式非法")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > maxKeyNameRunes {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "密钥名必填且不超过 64 个字符")
		return
	}
	plaintext, err := apikey.Generate()
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "服务端错误")
		return
	}
	prefix := apikey.Prefix(plaintext)
	id, err := h.store.InsertAPIKey(r.Context(), store.APIKey{
		AccountID: account.ID,
		Name:      name,
		KeyHash:   apikey.Hash(plaintext),
		KeyPrefix: prefix,
		ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "服务端错误")
		return
	}
	// 新建的密钥一律启用：InsertAPIKey 的 SQL 固定写 enabled = 1。
	webapi.WriteOK(w, createdAPIKeyView{
		apiKeyView: apiKeyView{ID: id, Name: name, KeyPrefix: prefix, Enabled: true, ExpiresAt: req.ExpiresAt},
		Secret:     plaintext,
	})
}

// handleRevokeKey 吊销一把属于当前账户的密钥。
func (h *Handler) handleRevokeKey(w http.ResponseWriter, r *http.Request, id uint64) {
	if r.Method != http.MethodPost {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 POST 方法")
		return
	}
	account, ok := h.currentAccount(w, r)
	if !ok {
		return
	}
	key, err := h.store.APIKeyByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "密钥不存在")
			return
		}
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "服务端错误")
		return
	}
	// 不属于当前账户的密钥按不存在处理：以 403 区分会让密钥 id 可被探测。
	if key.AccountID != account.ID {
		webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "密钥不存在")
		return
	}
	if err := h.store.SetAPIKeyEnabled(r.Context(), id, false); err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "服务端错误")
		return
	}
	webapi.WriteOK(w, nil)
}

// parseRevokePath 解析 /api/v1/user/keys/{id}/revoke；不匹配返回 ok=false。
func parseRevokePath(path string) (uint64, bool) {
	rest, found := strings.CutPrefix(path, KeysPath+"/")
	if !found {
		return 0, false
	}
	idPart, found := strings.CutSuffix(rest, revokeSuffix)
	if !found || idPart == "" || strings.Contains(idPart, "/") {
		return 0, false
	}
	id, err := strconv.ParseUint(idPart, 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// parsePaging 解析 page 与 size：缺省 page=1、size=20，size 超上限截到上限。
func parsePaging(query url.Values) (int, int, error) {
	page, err := parsePositive(query.Get("page"), 1)
	if err != nil {
		return 0, 0, err
	}
	size, err := parsePositive(query.Get("size"), defaultPageSize)
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
