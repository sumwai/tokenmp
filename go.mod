module github.com/sumwai/tokenmp

go 1.27.0

require (
	// 中间件插件的 JavaScript 引擎：纯 Go、零 CGO、不引入 C 工具链，
	// 且编译与建运行时开销低于其它纯 Go 方案，适合逐请求与逐事件的流式热路径。
	// 上游处于 alpha，API 可能变动，因此精确锁版本；升级须连同插件层验收一起做。
	github.com/Calcium-Ion/moejs v0.1.0-alpha.5
	github.com/go-sql-driver/mysql v1.10.1
	github.com/shopspring/decimal v1.4.0
)

require filippo.io/edwards25519 v1.2.0 // indirect
