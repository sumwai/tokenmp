package domain

import "context"

// RouteFacts 是选路结果里供中间件作用域判定的事实。
//
// 只有厂商标签需要随 context 传：模型名与客户端方言在钩子入参的 Request 上就有，
// 而渠道是选路之后才确定的上游事实，不属于客户端请求 —— 塞进 Request 会让那个概念
// 同时承载两侧的信息。
type RouteFacts struct {
	// Vendor 是渠道的厂商标签（upstream_channel.vendor），由运营在数据里命名。
	//
	// 作用域按它判定 provider 而不用渠道主键：主键是环境事实（重建库就换号），
	// 同一份插件在不同库里会作用到不同渠道，且文件里看不出数字是谁。
	Vendor string
}

// routeFactsKey 是选路事实在 context 里的键。类型不导出，避免外部写入。
type routeFactsKey struct{}

// WithRouteFacts 把选路事实挂到 ctx 上，供选路之后的中间件钩子做作用域判定。
//
// 由流水线在每次渠道尝试的入口挂一次，而不是逐分片挂：钩子在流式热路径上被反复调用，
// 每次重新 WithValue 只会白付一次分配。
func WithRouteFacts(ctx context.Context, facts RouteFacts) context.Context {
	return context.WithValue(ctx, routeFactsKey{}, facts)
}

// RouteFactsFromContext 取出选路事实；选路之前（或未挂）时第二个返回值为 false。
func RouteFactsFromContext(ctx context.Context) (RouteFacts, bool) {
	facts, ok := ctx.Value(routeFactsKey{}).(RouteFacts)
	return facts, ok
}
