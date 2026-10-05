// Package circuit 实现按渠道维度的进程内熔断器：闭合、打开、半开三态。
//
// 动机：单条上游渠道持续故障时，网关不应把每个请求都压到它身上等超时与重试，
// 而应在一个短暂窗口内跳过它、把流量交给其余候选渠道，窗口满后再放一笔探活验证恢复。
//
// 状态只存进程内内存：进程重启后全部渠道从闭合态开始，不落库、不跨实例同步。
// 多实例部署时每个实例各自统计连续失败，同一渠道的总探测次数可达实例数；
// 落库共享状态要在每个请求上引入读写放大，换来的只是「重启后仍然记得」，尚未出现需要。
//
// 判定失败的唯一依据是上游调用的错误码（见 trippable）：只把上游不可用与上游超时
// 计入连续失败，客户端取消、本端写出失败、上游 4xx 拒绝与上游限流都不计入，
// 避免把客户侧或平台侧的问题算到渠道健康度上。
package circuit

import (
	"log/slog"
	"sync"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
)

// State 是渠道熔断器的状态取值。
type State string

const (
	// StateClosed 是闭合态：正常调度，累计连续失败。
	StateClosed State = "closed"
	// StateOpen 是打开态：不调度，等冷却期结束。
	StateOpen State = "open"
	// StateHalfOpen 是半开态：只放行配置条数的探测流量验证恢复。
	StateHalfOpen State = "half_open"
)

// 零值参数取用的默认值。
const (
	// defaultFailureThreshold 与 defaultCooldown 是配置层的兜底值：配置层不导入本包，
	// 两处各写一份常量，取值必须一致。
	defaultFailureThreshold = 5
	defaultCooldown         = 30 * time.Second
	// defaultProbeConcurrency 是半开态同时放行的探测条数默认值：单探测最保守，
	// 上游恢复瞬间不会被一次齐发再打满。
	defaultProbeConcurrency = 1
)

// Options 是熔断器的构造参数。零值字段取对应默认值。
type Options struct {
	// FailureThreshold 是连续失败多少次后打开熔断；<= 0 时取 defaultFailureThreshold。
	FailureThreshold int
	// Cooldown 是打开态持续多久后允许探测；同时充当「在途探测多久未上报结果即视为丢失」
	// 的上限。<= 0 时取 defaultCooldown。
	Cooldown time.Duration
	// ProbeConcurrency 是半开态同时放行的探测条数；<= 0 时取 defaultProbeConcurrency。
	ProbeConcurrency int
	// Clock 取当前时刻；nil 时用 time.Now。注入后冷却判定不依赖真实时间，测试无需 sleep。
	Clock func() time.Time
	// Logger 记录状态迁移；nil 时不记录。
	Logger *slog.Logger
}

// Status 是单个渠道熔断器的只读状态快照。
type Status struct {
	// UpstreamID 是渠道标识。
	UpstreamID string
	// State 是当前状态。
	State State
	// ConsecutiveFailures 是闭合态下已累计的连续失败次数；打开态下固定为触发阈值。
	ConsecutiveFailures int
}

// Breaker 是按渠道维度维护的三态熔断器，可并发读写。
type Breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	probes    int
	clock     func() time.Time
	logger    *slog.Logger
	// channels 只在放行、上报与查询时按需创建渠道状态。
	channels map[string]*channel
}

// channel 是单条渠道的熔断器内部状态。
type channel struct {
	upstreamID string
	state      State
	failures   int
	// openedUntil 是打开态允许探测的最早时刻。
	openedUntil time.Time
	// probeStarts 是半开态在途探测的开始时刻，按发生顺序排列。
	probeStarts []time.Time
}

// NewBreaker 构造渠道熔断器；零值参数取默认值。
func NewBreaker(opts Options) *Breaker {
	threshold := opts.FailureThreshold
	if threshold <= 0 {
		threshold = defaultFailureThreshold
	}
	cooldown := opts.Cooldown
	if cooldown <= 0 {
		cooldown = defaultCooldown
	}
	probes := opts.ProbeConcurrency
	if probes <= 0 {
		probes = defaultProbeConcurrency
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Breaker{
		threshold: threshold,
		cooldown:  cooldown,
		probes:    probes,
		clock:     clock,
		logger:    opts.Logger,
		channels:  make(map[string]*channel),
	}
}

// Allow 报告该渠道当前是否放行一次调度。
//
//   - 未登记过的渠道视为闭合态，放行；
//   - 闭合态放行；
//   - 打开态在冷却期未满时拒绝，冷却期已满时转入半开并占用一个探测位；
//   - 半开态在探测位已满时拒绝；在途探测超过一个冷却期仍无结果时按丢失处理并放行新的探测，
//     避免探测请求异常退出把渠道永久困在半开态。
//
// upstreamID 为空串时不参与熔断，恒放行。
func (b *Breaker) Allow(upstreamID string) bool {
	if upstreamID == "" {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.channels[upstreamID]
	if !ok {
		return true
	}
	now := b.clock()
	switch ch.state {
	case StateOpen:
		if now.Before(ch.openedUntil) {
			return false
		}
		return b.beginProbe(ch, now)
	case StateHalfOpen:
		return b.beginProbe(ch, now)
	default:
		return true
	}
}

// Probe 报告能否放弃剩余冷却、把该渠道作为探测放行。
//
// 与 Allow 的差别：Allow 只在冷却期已满后才把打开态渠道转为半开；Probe 用于流水线发现
// 「本次调度的候选全部被 Allow 拒绝」时的兜底——宁可试一次也不直接失败，因此冷却期未满
// 也放行一次探测。探测位已满时返回 false，交由调用方维持失败封闭。
//
// upstreamID 为空串时不参与熔断，恒放行。
func (b *Breaker) Probe(upstreamID string) bool {
	if upstreamID == "" {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.channels[upstreamID]
	if !ok {
		return true
	}
	switch ch.state {
	case StateOpen, StateHalfOpen:
		return b.beginProbe(ch, b.clock())
	default:
		return true
	}
}

// Record 上报一次该渠道调用的结果；err 为 nil 表示成功。
//
// 打开态下到达的回报只可能来自熔断打开前已发出的请求，一律忽略、不改变已定状态：
// 那些请求的结果不能代表当前渠道健康度，否则一次迟到成功会把刚打开的熔断立刻关掉。
// 需要放行探测时先经 Probe 或 Allow 转为半开态，回报才会被采纳。
//
// upstreamID 为空串时不上报。
func (b *Breaker) Record(upstreamID string, err error) {
	if upstreamID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.channels[upstreamID]
	if !ok {
		if err == nil || !trippable(err) {
			// 未知渠道上的中性结果无需建档，否则每个只成功过一次的渠道都会常驻一条状态。
			return
		}
		ch = &channel{upstreamID: upstreamID, state: StateClosed}
		b.channels[upstreamID] = ch
	}
	if ch.state == StateOpen {
		return
	}
	switch {
	case err == nil:
		b.recordSuccess(ch)
	case trippable(err):
		b.recordFailure(ch)
	default:
		b.recordNeutral(ch)
	}
}

// Status 返回单个渠道的只读状态快照；未登记过的渠道按闭合态返回。
//
// 只读：不创建状态行、不改变任何状态。
func (b *Breaker) Status(upstreamID string) Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.channels[upstreamID]
	if !ok {
		return Status{UpstreamID: upstreamID, State: StateClosed}
	}
	return Status{
		UpstreamID:          upstreamID,
		State:               ch.state,
		ConsecutiveFailures: ch.failures,
	}
}

// beginProbe 占用一个探测位并把渠道置为半开；探测位已满时返回 false。
//
// 先剔除超时未上报的探测：探测请求异常退出（进程被调度挂起、调用方 panic）时没有
// Record 到达，把这些残留位占满会让渠道永远无法再被探测。
func (b *Breaker) beginProbe(ch *channel, now time.Time) bool {
	b.pruneProbes(ch, now)
	if len(ch.probeStarts) >= b.probes {
		return false
	}
	if ch.state != StateHalfOpen {
		b.logTransition(ch, ch.state, StateHalfOpen)
	}
	ch.state = StateHalfOpen
	ch.probeStarts = append(ch.probeStarts, now)
	return true
}

// pruneProbes 丢弃超过一个冷却期仍未上报结果的在途探测。
func (b *Breaker) pruneProbes(ch *channel, now time.Time) {
	if len(ch.probeStarts) == 0 {
		return
	}
	kept := ch.probeStarts[:0]
	for _, started := range ch.probeStarts {
		if now.Sub(started) < b.cooldown {
			kept = append(kept, started)
		}
	}
	ch.probeStarts = kept
}

// recordSuccess 处理一次成功：闭合态的连续失败清零，半开态恢复闭合。
func (b *Breaker) recordSuccess(ch *channel) {
	previous := ch.state
	ch.state = StateClosed
	ch.failures = 0
	ch.openedUntil = time.Time{}
	ch.probeStarts = nil
	if previous != StateClosed {
		b.logTransition(ch, previous, StateClosed)
	}
}

// recordFailure 处理一次计入熔断的上游失败：闭合态累计到阈值即打开，半开态重新打开并重新计时。
func (b *Breaker) recordFailure(ch *channel) {
	if ch.state == StateHalfOpen {
		b.reopen(ch)
		return
	}
	ch.failures++
	if ch.failures >= b.threshold {
		b.reopen(ch)
	}
}

// recordNeutral 处理一次既非成功也非渠道故障的结果：不改动累计值，
// 只在半开态释放一个探测位，让下一个请求重新探测。
func (b *Breaker) recordNeutral(ch *channel) {
	if ch.state == StateHalfOpen && len(ch.probeStarts) > 0 {
		ch.probeStarts = ch.probeStarts[1:]
	}
}

// reopen 把渠道置为打开态，从当前时刻重新计时并清空探测位。
func (b *Breaker) reopen(ch *channel) {
	previous := ch.state
	now := b.clock()
	ch.state = StateOpen
	ch.failures = b.threshold
	ch.openedUntil = now.Add(b.cooldown)
	ch.probeStarts = nil
	b.logTransition(ch, previous, StateOpen)
}

// logTransition 记录一次状态迁移。
func (b *Breaker) logTransition(ch *channel, from, to State) {
	if b.logger == nil {
		return
	}
	args := []any{
		"upstream_id", ch.upstreamID,
		"from", string(from),
		"to", string(to),
		"consecutive_failures", ch.failures,
	}
	if to == StateOpen {
		b.logger.Warn("渠道熔断状态迁移", args...)
		return
	}
	b.logger.Info("渠道熔断状态迁移", args...)
}

// trippable 报告错误是否属于「计入熔断」的上游硬故障。
//
// 判据只取统一错误码：上游不可用（HTTP 5xx、连接失败、响应无法解析）与上游超时
// （网络超时、上游 408）。上游限流虽然可重试，但它说明渠道仍然存活、只是配额暂时用尽，
// 计入会把限流误判成渠道故障；上游 4xx 拒绝、客户端取消（归为平台内部错误）与本端写出
// 失败同理不计。
func trippable(err error) bool {
	domainErr := domain.AsError(err)
	if domainErr == nil {
		return false
	}
	switch domainErr.Code {
	case domain.CodeUpstreamUnavailable, domain.CodeUpstreamTimeout:
		return true
	default:
		return false
	}
}
