package auth

import (
	"net/http"

	"github.com/sumwai/tokenmp/internal/webapi"
)

// 页面信封的实现见 internal/webapi：信封与业务码是页面通信的共性，认证面与
// 用户业务面共用同一份写出点。此处保留包内名字，调用点不必随实现位置改名。

// 页面业务码。取值与 docs/openapi-web.yaml 的业务码表一致，改动须同步契约。
const (
	codeOK               = webapi.CodeOK
	codeBadRequest       = webapi.CodeBadRequest
	codeUnauthorized     = webapi.CodeUnauthorized
	codeForbidden        = webapi.CodeForbidden
	codeNotFound         = webapi.CodeNotFound
	codeConflict         = webapi.CodeConflict
	codeChallengeExpired = webapi.CodeChallengeExpired
	codeTooManyRequests  = webapi.CodeTooManyRequests
	codeInternal         = webapi.CodeInternal
)

// writeOK 写成功信封；data 为 nil 时 data 字段序列化为 null。
func writeOK(w http.ResponseWriter, data any) { webapi.WriteOK(w, data) }

// writeErr 写失败信封：HTTP 状态与业务 code 在页面侧保持同值，
// 前端无论看状态码还是看 body 都得到同一判断。
func writeErr(w http.ResponseWriter, httpStatus, code int, message string) {
	webapi.WriteError(w, httpStatus, code, message)
}
