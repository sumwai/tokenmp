package me

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/sumwai/tokenmp/internal/access"
	"github.com/sumwai/tokenmp/internal/domain"
)

// AccountPath 是自助查询端点的固定路径。
const AccountPath = "/v1/me/account"

// recentQueryParam 是调整最近流水条数的查询参数名。
const recentQueryParam = "recent"

const (
	// defaultRecent 是未指定 recent 参数时返回的流水条数。
	defaultRecent = 10
	// maxRecent 是 recent 参数的上限。封顶而不是拒绝：取一个过大的值只说明
	// 想尽量多拿，没有必要因此让整次查询失败。
	maxRecent = 100
)

// NewHandler 构造自助查询的 HTTP 处理器。
//
// 处理器假定自己已被数据面同一鉴权中间件包住：账户与密钥从上下文里的
// access.Identity 取，拿不到身份时回 401 而不是查询一个未知账户。
func NewHandler(svc *Service) http.Handler {
	return &handler{svc: svc}
}

type handler struct {
	svc *Service
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 方法校验在鉴权之后（处理器被鉴权中间件包住）：与数据面各端点一致，
	// 未带凭据的请求先被 401 拦下，不因方法错误而绕过鉴权。
	if r.Method != http.MethodGet {
		access.WriteJSONError(w, http.StatusMethodNotAllowed, domain.CodeInvalidRequest, "只支持 GET 方法")
		return
	}
	identity, ok := access.IdentityFromContext(r.Context())
	if !ok {
		access.WriteJSONError(w, http.StatusUnauthorized, domain.CodeUnauthorized, "缺少鉴权上下文")
		return
	}
	limit, err := parseRecent(r.URL.Query().Get(recentQueryParam))
	if err != nil {
		access.WriteJSONError(w, http.StatusBadRequest, domain.CodeInvalidRequest, err.Error())
		return
	}
	summary, err := h.svc.Summary(r.Context(), identity.AccountID, identity.APIKeyID, limit)
	if err != nil {
		access.WriteJSONError(w, http.StatusInternalServerError, domain.CodeInternal, "账户摘要查询失败")
		return
	}
	writeJSON(w, summary)
}

// parseRecent 解析 recent 查询参数：缺省取默认条数，负数或非整数报错，超出上限截到上限。
//
// 0 是合法取值，表示只要账户、包与限额，不要流水。
func parseRecent(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultRecent, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s 必须是 0 到 %d 之间的整数", recentQueryParam, maxRecent)
	}
	if n > maxRecent {
		return maxRecent, nil
	}
	return n, nil
}

// writeJSON 写一个 200 JSON 响应。
func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", access.JSONContentType)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(payload)
}
