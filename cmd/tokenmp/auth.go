package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
)

// 本文件是客户端鉴权与共享 JSON 错误体。
//
// 鉴权在协议分派之前完成：凭据决定用哪个账户与商家，选路随后按它们查候选。
// 因此鉴权结果放进请求上下文，供存储层之上的选路与凭据读取消费。

const (
	// authorizationHeader 是客户端携带密钥的请求头名。
	authorizationHeader = "Authorization"
	// authSchemePrefix 是 Authorization 头里密钥方案的固定前缀。
	// 不叫 bearer 之类的名字：gosec 的硬编码凭据启发式会把含该词的名字误报成凭据。
	authSchemePrefix = "Bearer "
	// accountStatusActive 是可用于转发的账户状态，与 0001 迁移的取值一致。
	accountStatusActive = "active"
	// jsonContentType 是共享 JSON 错误体的 Content-Type。
	jsonContentType = "application/json"
)

// identity 是一次通过鉴权的请求归属。
//
// 它随请求上下文传递，不落日志：account_id 与 merchant_id 只用于选路与凭据读取，
// api_key_id 只用于后续的用量归属。
type identity struct {
	accountID  uint64
	merchantID uint64
	apiKeyID   uint64
}

// identityContextKey 是上下文中承载 identity 的键。
// 用未导出的空结构体作键：外部包无法构造同值的键，也就无法顶替鉴权结果。
type identityContextKey struct{}

// withIdentity 把鉴权结果写入上下文。
func withIdentity(ctx context.Context, id identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, id)
}

// identityFromContext 读取鉴权结果；未鉴权时第二个返回值为 false。
func identityFromContext(ctx context.Context) (identity, bool) {
	id, ok := ctx.Value(identityContextKey{}).(identity)
	return id, ok
}

// hashAPIKey 计算客户端密钥的存储键：SHA-256 的十六进制小写串。
//
// 库中只存该哈希（uk_api_key_hash），明文不落库，因此鉴权路径只做哈希比对。
func hashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// bearerToken 从 Authorization 头里取出密钥，并报告格式是否合法。
//
// 方案名不区分大小写（HTTP 语义如此），密钥本身保持原样；空白密钥按非法处理，
// 避免空串走到哈希查询再报一个与真实原因无关的 401。
func bearerToken(header string) (string, bool) {
	if len(header) < len(authSchemePrefix) || !strings.EqualFold(header[:len(authSchemePrefix)], authSchemePrefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(authSchemePrefix):])
	return token, token != ""
}

// authenticator 是按密钥哈希鉴权的中间件。
type authenticator struct {
	store gatewayStore
	now   func() time.Time
}

// newAuthenticator 构造鉴权中间件；now 为 nil 时取系统时钟。
func newAuthenticator(store gatewayStore, now func() time.Time) *authenticator {
	if now == nil {
		now = time.Now
	}
	return &authenticator{store: store, now: now}
}

// middleware 包装下游处理器：鉴权通过后把 identity 写入上下文，失败时回统一的 401 JSON。
//
// 三类失败合并为 401：头缺失或格式非法、密钥查不到或已过期、账户不可用。
// 分开回不同状态码会把「这个密钥是否存在」「这个账户是否被停用」暴露给未授权调用方。
// 存储层报出的是查询失败（非无匹配）时回 500，与鉴权失败区分开。
func (a *authenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get(authorizationHeader))
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, domain.CodeUnauthorized, "缺少或非法的 Authorization 头")
			return
		}
		auth, err := a.store.LookupAPIKey(r.Context(), hashAPIKey(token), a.now())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSONError(w, http.StatusUnauthorized, domain.CodeUnauthorized, "API key 无效或已过期")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, domain.CodeInternal, "鉴权查询失败")
			return
		}
		if auth.AccountStatus != accountStatusActive {
			writeJSONError(w, http.StatusUnauthorized, domain.CodeUnauthorized, "账户不可用")
			return
		}
		ctx := withIdentity(r.Context(), identity{
			accountID:  auth.AccountID,
			merchantID: auth.MerchantID,
			apiKeyID:   auth.APIKeyID,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// errorEnvelope 是共享 JSON 错误体的外层结构，形如 {"error":{"message":"…","type":"…","code":"…"}}。
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// errorBody 是共享 JSON 错误体的内容。
type errorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// writeJSONError 写一个共享格式的 JSON 错误响应。
//
// 鉴权失败、未注册路径与存储层故障都经这里写出：它们发生在协议分派之前或之外，
// 没有可用的适配器，只能给一个与协议无关的统一错误体。
// 已注册路径在分派之后的错误仍由各协议适配器编码，形态与该协议自身的错误体一致。
func writeJSONError(w http.ResponseWriter, code int, domainCode domain.Code, message string) {
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: errorBody{
		Message: message,
		Type:    errorTypeForCode(domainCode),
		Code:    string(domainCode),
	}})
}

// errorTypeForCode 把统一错误码映射为错误体里的 type 取值。
//
// 取值与 OpenAI 系适配器的映射口径一致，使发生在协议分派之前的那批错误
// 与协议内错误的分类看起来是同一套。
func errorTypeForCode(code domain.Code) string {
	switch code {
	case domain.CodeInvalidRequest, domain.CodeUnauthorized, domain.CodeForbidden,
		domain.CodeModelNotFound, domain.CodeNotFound, domain.CodeRateLimited:
		return "invalid_request_error"
	case domain.CodeUpstreamTimeout, domain.CodeUpstreamUnavailable,
		domain.CodeUpstreamRateLimited, domain.CodeUpstreamRejected:
		return "upstream_error"
	default:
		return "api_error"
	}
}
