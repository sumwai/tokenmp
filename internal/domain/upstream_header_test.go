package domain

import "testing"

// TestIsReservedUpstreamHeader 守护保留头名清单的判据：大小写与前后空白都不放过。
//
// 清单本身是安全边界：这些头名由网关按凭据形态与响应形态自行设定，一旦被渠道配置
// 占用，客户端就有机会顶替上游凭据或让报文头与实际形态不一致。
func TestIsReservedUpstreamHeader(t *testing.T) {
	reserved := []string{
		"Authorization", "authorization", "AUTHORIZATION",
		"x-api-key", "X-Api-Key",
		"x-goog-api-key", "X-Goog-Api-Key",
		"Content-Type", "content-type",
		"Accept",
		"Content-Length",
		"Host",
		"Connection",
		"Transfer-Encoding",
		"Upgrade",
		"Keep-Alive",
		"TE",
		"Trailer",
		"Proxy-Authorization",
		"Proxy-Connection",
		"  Authorization  ",
	}
	for _, name := range reserved {
		if !IsReservedUpstreamHeader(name) {
			t.Errorf("IsReservedUpstreamHeader(%q) = false，期望 true", name)
		}
	}

	allowed := []string{
		"x-opencode-session",
		"x-trace-id",
		"anthropic-version",
		"x-stainless-lang",
		"",
		"   ",
	}
	for _, name := range allowed {
		if IsReservedUpstreamHeader(name) {
			t.Errorf("IsReservedUpstreamHeader(%q) = true，期望 false", name)
		}
	}
}
