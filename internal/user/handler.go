package user

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// recentQueryParam 是账户概览的最近流水条数参数，与数据面自助查询同名同口径。
const recentQueryParam = "recent"

const (
	// defaultRecent 是未指定 recent 参数时返回的流水条数。
	defaultRecent = 10
	// maxRecent 是 recent 参数的上限：取一个过大的值只说明想尽量多拿，
	// 没有必要因此让整次查询失败，截到上限即可。
	maxRecent = 100
)

// errInvalidRecent 是 recent 参数非法时的错误，文案直接写回信封。
var errInvalidRecent = errors.New("recent 必须是 0 到 100 之间的整数")

// ServeHTTP 按路径分发到各动作；子树内未声明的路径回页面信封 404。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == ConsolePath:
		h.handleConsole(w, r)
	case r.URL.Path == AccountPath:
		h.handleAccount(w, r)
	case r.URL.Path == KeysPath:
		h.handleKeys(w, r)
	case r.URL.Path == UsagePath:
		h.handleUsage(w, r)
	case r.URL.Path == UsagePath+usageStatsSuffix:
		h.handleUsageStats(w, r)
	case r.URL.Path == ModelsPath:
		h.handleModels(w, r)
	case r.URL.Path == RequestsPath:
		h.handleRequests(w, r)
	case r.URL.Path == RequestsPath+requestStatsSuffix:
		h.handleRequestStats(w, r)
	case strings.HasPrefix(r.URL.Path, RequestsPath+"/"):
		requestID, ok := parseRequestIDPath(r.URL.Path)
		if !ok {
			webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "端点不存在")
			return
		}
		h.handleRequestDetail(w, r, requestID)
	case strings.HasPrefix(r.URL.Path, KeysPath+"/"):
		id, ok := parseRevokePath(r.URL.Path)
		if !ok {
			webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "端点不存在")
			return
		}
		h.handleRevokeKey(w, r, id)
	default:
		webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "端点不存在")
	}
}

// handleAccount 返回当前账户的摘要。
func (h *Handler) handleAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 方法")
		return
	}
	account, ok := h.currentAccount(w, r)
	if !ok {
		return
	}
	limit, err := parseRecent(r.URL.Query().Get(recentQueryParam))
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	// apiKeyID 传 0：账户概览只回答账户维度的限额；密钥维度的限额挂在具体密钥上，
	// 这一层没有可对应的密钥。
	summary, err := h.summary.Summary(r.Context(), account.ID, 0, limit)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "账户摘要查询失败")
		return
	}
	webapi.WriteOK(w, summary)
}

// subject 是会话解析出的登录主体：作用域推导用 id，控制台清单还用到身份集合。
type subject struct {
	userID uint64
	roles  []string
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

// ownedAccount 在主体之上推导归属账户；没有归属账户回 403。
//
// 会话有效但没有归属账户回 403 而不是 401：两者的下一步动作不同 —— 一个是等待开户，
// 一个是重新登录，折叠成同一个码会让前端无法区分。
func (h *Handler) ownedAccount(w http.ResponseWriter, r *http.Request, userID uint64) (*store.Account, bool) {
	account, err := h.store.AccountByOwner(r.Context(), userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			webapi.WriteError(w, http.StatusForbidden, webapi.CodeForbidden, "账户尚未开通")
			return nil, false
		}
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "服务端错误")
		return nil, false
	}
	return account, true
}

// currentAccount 是「解析主体 + 推导归属账户」的组合，供需要账户的端点取用。
func (h *Handler) currentAccount(w http.ResponseWriter, r *http.Request) (*store.Account, bool) {
	sub, ok := h.currentSubject(w, r)
	if !ok {
		return nil, false
	}
	return h.ownedAccount(w, r, sub.userID)
}

// parseRecent 解析 recent 查询参数：缺省取默认条数，负数或非整数报错，超上限截到上限。
//
// 0 是合法取值，表示只要账户、包与限额，不要流水。
func parseRecent(raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return defaultRecent, nil
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil || value < 0 {
		return 0, errInvalidRecent
	}
	if value > maxRecent {
		return maxRecent, nil
	}
	return value, nil
}
