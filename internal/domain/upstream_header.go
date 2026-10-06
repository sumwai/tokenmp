package domain

import (
	"strings"
)

// 渠道可以配置静态上游请求头，但有一部分头名由网关自身决定，配上去只会互相打架。
// 本文件是这份保留清单的唯一出处，写入侧（管理面）拒绝、读取侧（选路）防御性丢弃。
//
// 分两类：
//
//	鉴权头    凭据注入形态决定哪一个头承载上游凭据（见 CredentialHeaderStyle）。
//	          静态头占用同名位置会让客户端有机会顶替上游凭据；形态还能随渠道在
//	          authorization / x-api-key / x-goog-api-key 之间切换，所以三个都要挡，
//	          不能只挡当前生效的那一个。
//
//	报文控制头 Content-Type 与 Accept 由网关按本次响应形态设定，取客户端值会与实际
//	          报文形态不一致；Content-Length、Host 与逐跳头属 HTTP 传输层，不由业务
//	          配置决定。
var reservedUpstreamHeaders = map[string]struct{}{
	"authorization":       {},
	"x-api-key":           {},
	"x-goog-api-key":      {},
	"content-type":        {},
	"accept":              {},
	"content-length":      {},
	"host":                {},
	"connection":          {},
	"transfer-encoding":   {},
	"upgrade":             {},
	"keep-alive":          {},
	"te":                  {},
	"trailer":             {},
	"proxy-authorization": {},
	"proxy-connection":    {},
}

// IsReservedUpstreamHeader 报告头名是否由网关自身占用，渠道静态请求头不得配置。
//
// 判据与 http.Header 的键规整一致：HTTP 头名不区分大小写，写 "Authorization" 与写
// "authorization" 是同一件事。前后空白一并忽略，避免靠空白绕过。
func IsReservedUpstreamHeader(name string) bool {
	_, ok := reservedUpstreamHeaders[strings.ToLower(strings.TrimSpace(name))]
	return ok
}
