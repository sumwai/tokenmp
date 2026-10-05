package pipeline

// Breaker 是渠道熔断器的窄接口。
//
// 流水线在遍历候选渠道时询问它是否放行该渠道，并在一次上游尝试结束后上报该渠道的结果；
// 具体实现由装配层注入（生产装配注入其断路器实现），
// 流水线不依赖具体实现，端口由消费方定义。
type Breaker interface {
	// Allow 报告该渠道当前是否放行一次调度；返回 false 时流水线跳过该候选。
	Allow(upstreamID string) bool
	// Record 上报一次该渠道调用的结果；err 为 nil 表示成功。
	//
	// 实现方自行判定哪些错误计入熔断：流水线只如实上报本次尝试的错误。
	Record(upstreamID string, err error)
}

// BreakerProber 是渠道熔断器的可选能力：在本次调度的候选全部被熔断器拒绝时，
// 请求把其中一条作为探测放行（宁可试一次，也不把整条链路直接判为不可用）。
//
// 与 Breaker 分开声明：核心端口只描述放行与上报，而「全部打开也要试一次」是流水线的兜底
// 策略，需要熔断器额外提供一次绕过冷却期的放行。熔断器实现本接口时流水线才启用该兜底，
// 未实现时保持既有的失败封闭行为。
type BreakerProber interface {
	// Probe 报告能否把该渠道作为探测放行；冷却期未满也应放行一次。
	// 返回 false（例如探测位已满）时流水线维持失败封闭。
	Probe(upstreamID string) bool
}
