// Package webapi 实现页面通信的公共部分：六字段信封与页面会话的 Bearer 令牌解析。
//
// 信封形状以 docs/openapi-web.yaml 为准。与数据面的 ErrorEnvelope
// （{error:{message,type,code}}）是两个形状：页面信封成功失败同形，前端按 code
// 分支即可，不必先探测 content 里的 error 键。
//
// 认证面与用户业务面共用这里的唯一写出点，避免出现第二份信封实现后两边漂移。
package webapi

import (
	"encoding/json"
	"net/http"
)

// 页面业务码。取值与 docs/openapi-web.yaml 的业务码表一致，改动须同步契约。
const (
	CodeOK           = 200
	CodeBadRequest   = 400
	CodeUnauthorized = 401
	// CodePaymentRequired 与数据面的 402 同一语义：账户无可用额度。
	// 页面侧的落点是账户概览，页面据此给充值指引（web/AGENTS.md 的三态）。
	CodePaymentRequired  = 402
	CodeForbidden        = 403
	CodeNotFound         = 404
	CodeConflict         = 409
	CodeChallengeExpired = 410
	CodeTooManyRequests  = 429
	CodeInternal         = 500
)

// Envelope 是页面通信的统一信封，六个字段固定出现。
//
// Page、Size、Total 用指针：非列表端点必须序列化为 null 而不是 0，
// 前端据此区分「没有分页」与「第 0 页」。
type Envelope struct {
	Code    int    `json:"code"`
	Data    any    `json:"data"`
	Message string `json:"message"`
	Page    *int   `json:"page"`
	Size    *int   `json:"size"`
	Total   *int   `json:"total"`
}

// WriteOK 写成功信封；data 为 nil 时 data 字段序列化为 null。
func WriteOK(w http.ResponseWriter, data any) {
	WriteEnvelope(w, http.StatusOK, CodeOK, "ok", data, nil, nil, nil)
}

// WriteError 写失败信封：HTTP 状态与业务码在页面侧保持同值，
// 前端无论看状态码还是看 body 都得到同一判断。
func WriteError(w http.ResponseWriter, httpStatus, code int, message string) {
	WriteEnvelope(w, httpStatus, code, message, nil, nil, nil, nil)
}

// WritePage 写分页列表信封；列表数据在 data 内，分页三字段固定填充。
func WritePage(w http.ResponseWriter, data any, page, size, total int) {
	WriteEnvelope(w, http.StatusOK, CodeOK, "ok", data, &page, &size, &total)
}

// WriteEnvelope 是唯一的写出点，保证六个字段与 Content-Type 不被各端点写歪。
func WriteEnvelope(w http.ResponseWriter, httpStatus, code int, message string,
	data any, page, size, total *int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(Envelope{
		Code:    code,
		Data:    data,
		Message: message,
		Page:    page,
		Size:    size,
		Total:   total,
	})
}
