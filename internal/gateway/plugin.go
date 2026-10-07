package gateway

import (
	"context"
	"io"
	"net/http"

	"github.com/sumwai/tokenmp/internal/access"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/plugin"
	"github.com/sumwai/tokenmp/internal/transport"
)

// 本文件是中间件插件层与转发内核之间的装配件：插件层不依赖 transport，
// 转发内核不依赖 plugin，两者在装配层对接。

// pluginForwarder 在流水线之外完成请求改写。
//
// 请求改写必须发生在路由解析之前，而路由解析在流水线内部；把装饰器放在流水线之外，
// 改写后的请求体经客户端适配器重新解码后再进入流水线，选路看到的就是改写后的模型名。
// 逐事件与非流式响应改写不需要这一层，它们经流水线端口注入，与请求改写共用同一请求状态。
type pluginForwarder struct {
	inner   transport.Forwarder
	plugins *plugin.Live
}

// 编译期断言：装饰器与流水线同形，可直接交给入口层。
var _ transport.Forwarder = (*pluginForwarder)(nil)

// Forward 建立本次请求的插件上下文，执行请求改写，再把控制权交给流水线。
func (f *pluginForwarder) Forward(ctx context.Context, client domain.Adapter, req *domain.Request, out io.Writer) error {
	ctx, release := f.plugins.BeginRequest(ctx, req)
	defer release()
	if err := f.plugins.OnRequest(ctx, client, req); err != nil {
		return err
	}
	return f.inner.Forward(ctx, client, req, out)
}

// pluginContextMiddleware 把请求路径与鉴权归属写进上下文。
//
// 鉴权在更外层完成，因此本中间件必须注册在鉴权之后：归属取自鉴权结果，
// 未通过鉴权的请求到不了这里。它始终装着（换装可以让集合从空变非空），但集合为空时
// 直接放行：没装插件时不写上下文，也不产生额外分配。
func pluginContextMiddleware(live *plugin.Live, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if live.Empty() {
			next.ServeHTTP(w, r)
			return
		}
		ctx := plugin.WithPath(r.Context(), r.URL.Path)
		if id, ok := access.IdentityFromContext(ctx); ok {
			ctx = plugin.WithAgent(ctx, plugin.Agent{
				AccountID:  id.AccountID,
				MerchantID: id.MerchantID,
				APIKeyID:   id.APIKeyID,
			})
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
