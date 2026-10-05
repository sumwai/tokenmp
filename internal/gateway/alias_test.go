package gateway

import (
	"context"
	"net"
	"net/http"

	"github.com/sumwai/tokenmp/internal/access"
)

// 本文件是测试侧别名与共享夹具：测试随装配实现下沉到本包，沿用下沉前的短名，
// 断言语义不变。常量与类型统一由 internal/access 提供。
const (
	authorizationHeader = access.AuthorizationHeader
	authSchemePrefix    = access.AuthSchemePrefix
	jsonContentType     = access.JSONContentType
	retryAfterHeader    = access.RetryAfterHeader
	accountStatusActive = access.AccountStatusActive
)

// errorEnvelope 是共享错误体的测试别名。
type errorEnvelope = access.ErrorEnvelope

// hashAPIKey 转发到 access 的哈希实现。
func hashAPIKey(key string) string { return access.HashAPIKey(key) }

// 装配入口下沉后改为导出名，测试沿用短名以减少移动噪声。
type gatewayOptions = Options

func newGateway(st gatewayStore, opts Options) (*Gateway, error) { return New(st, opts) }

func runServer(ctx context.Context, server *http.Server, ln net.Listener) error {
	return Run(ctx, server, ln)
}

const (
	readHeaderTimeout = ReadHeaderTimeout
	healthzPath       = HealthzPath
)
