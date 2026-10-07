package gateway

import (
	"context"
	"log/slog"
	"strings"

	"github.com/sumwai/tokenmp/internal/plugin"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是中间件作用域的装配期检查：作用域里的名字必须在当前配置里存在。
//
// 三个名字轴（模型名、方言、厂商标签）都靠运营在数据里命名：改一次 vendor 标签就会让依赖它的
// 中间件静默失效。启动时报出来，比等某天发现某个模型行为不对便宜得多。
// 检查是提示性的：读不到配置只记一条告警，不拦启动。

// scopeConfigReader 是漂移检查需要的读取面。
//
// 就地声明成一个小接口而不是加进 gatewayStore：它只在装配期用一次、失败也不影响转发，
// 不值得进核心依赖面。存储层满足它，其它实现（测试替身）不满足时静默跳过检查。
type scopeConfigReader interface {
	ListChannels(ctx context.Context) ([]store.Channel, error)
	ListModelMaps(ctx context.Context) ([]store.ModelMap, error)
}

// warnScopeDrift 把中间件作用域里匹配不到任何配置的取值记一条告警。
func warnScopeDrift(ctx context.Context, logger *slog.Logger, middleware *plugin.Set, reader scopeConfigReader) {
	if logger == nil || middleware.Empty() {
		return
	}
	declarations := middleware.ScopeDeclarations()
	if len(declarations) == 0 {
		return
	}
	known, err := knownScopeValues(ctx, reader)
	if err != nil {
		logger.Warn("中间件作用域漂移检查跳过：读取当前配置失败", "error", err.Error())
		return
	}
	for _, declaration := range declarations {
		for _, axis := range []string{plugin.AxisModels, plugin.AxisVendors, plugin.AxisProtocols} {
			for _, value := range declaration.Values[axis] {
				if known[axis][value] {
					continue
				}
				logger.Warn("中间件作用域里的取值在当前配置中不存在，该插件不会对任何渠道生效",
					"plugin", declaration.Middleware, "axis", axis, "value", value)
			}
		}
	}
}

// knownScopeValues 取当前配置里实际存在的模型名、厂商标签与渠道方言。
//
// 方言轴取渠道类型而不是端点路径：作用域写的是协议取值（openai_chat 一类），
// 与 upstream_channel.type 同源。
func knownScopeValues(ctx context.Context, reader scopeConfigReader) (map[string]map[string]bool, error) {
	known := map[string]map[string]bool{
		plugin.AxisModels:    {},
		plugin.AxisVendors:   {},
		plugin.AxisProtocols: {},
	}
	channels, err := reader.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	for _, channel := range channels {
		if vendor := strings.TrimSpace(channel.Vendor); vendor != "" {
			known[plugin.AxisVendors][vendor] = true
		}
		known[plugin.AxisProtocols][string(channel.Type)] = true
	}
	maps, err := reader.ListModelMaps(ctx)
	if err != nil {
		return nil, err
	}
	for _, modelMap := range maps {
		if modelMap.Enabled {
			known[plugin.AxisModels][strings.TrimSpace(modelMap.Model)] = true
		}
	}
	return known, nil
}
