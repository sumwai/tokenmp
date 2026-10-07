package auth

import (
	"encoding/json"
	"net/http"
)

// 本文件实现 docs/openapi-web.yaml 定义的六字段信封。
//
// 与数据面的 ErrorEnvelope（{error:{message,type,code}}）是两个形状：
// 页面信封成功失败同形，前端按 code 分支即可，不必先探测 content 里的 error 键。

// 页面业务码。取值与 docs/openapi-web.yaml 的业务码表一致，改动须同步契约。
const (
	codeOK               = 200
	codeBadRequest       = 400
	codeUnauthorized     = 401
	codeForbidden        = 403
	codeNotFound         = 404
	codeConflict         = 409
	codeChallengeExpired = 410
	codeTooManyRequests  = 429
	codeInternal         = 500
)

// envelope 是页面通信的统一信封，六个字段固定出现。
//
// Page、Size、Total 用指针：非列表端点必须序列化为 null 而不是 0，
// 前端据此区分「没有分页」与「第 0 页」。
type envelope struct {
	Code    int    `json:"code"`
	Data    any    `json:"data"`
	Message string `json:"message"`
	Page    *int   `json:"page"`
	Size    *int   `json:"size"`
	Total   *int   `json:"total"`
}

// writeOK 写成功信封；data 为 nil 时 data 字段序列化为 null。
func writeOK(w http.ResponseWriter, data any) {
	writeEnvelope(w, http.StatusOK, codeOK, "ok", data, nil, nil, nil)
}

// writeErr 写失败信封：HTTP 状态与业务 code 在页面侧保持同值，
// 前端无论看状态码还是看 body 都得到同一判断。
func writeErr(w http.ResponseWriter, httpStatus, code int, message string) {
	writeEnvelope(w, httpStatus, code, message, nil, nil, nil, nil)
}

// writeEnvelope 是唯一的写出点，保证六个字段与 Content-Type 不被各端点写歪。
func writeEnvelope(w http.ResponseWriter, httpStatus, code int, message string,
	data any, page, size, total *int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(envelope{
		Code:    code,
		Data:    data,
		Message: message,
		Page:    page,
		Size:    size,
		Total:   total,
	})
}
