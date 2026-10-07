// Package pipeline 实现网关唯一的核心转发流水线：
//   - 请求校验
//   - 选路与请求定稿
//   - 上游调用与候选回退
//
// 四种线协议共用本流水线，协议差异只由注入的适配器表达，流水线内不出现协议分支。
//
// 本包只依赖 internal/domain：上游客户端由装配层以 domain.UpstreamCaller 注入，
// 因此本包不得、也不需要导入 internal/upstream。
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
)

const (
	// maxAttemptsDefault 是同协议段（上游协议与客户端协议一致）的默认尝试上限。
	// 生产装配与直接构造流水线都取这一个默认值，不再各自维护一份。
	maxAttemptsDefault = 2
	// crossProtocolAttemptsDefault 是跨协议段（上游协议与客户端协议不一致）的默认尝试上限。
	// 跨协议重建的保真度与延迟都不如同协议透传，取值 1 表示「同协议段耗尽后给跨协议候选一次机会」，
	// 不在此之上继续扩大重试。
	crossProtocolAttemptsDefault = 1
	// maxTotalAttempts 是单请求尝试次数的防御性上限。
	//
	// 按段预算已让总次数有界；本常量再封一层，避免候选链异常增长或段预算被调大后一次请求打穿整条链。
	// 取值 4 = 两段默认预算之和（2 + 1）再加一条余量：既覆盖默认行为，也允许其中一段预算被上调一级。
	maxTotalAttempts = 4
)

// AdapterLookup 按协议返回适配器，用于按上游协议取请求定稿能力。
//
// 与 internal/upstream 的适配器查找同形，便于装配层用同一个查找函数同时供给两处；
// 本流水线只把返回值当作无状态的请求构建器使用，不参与流式状态。
type AdapterLookup func(protocol domain.Protocol) (domain.Adapter, error)

// Options 是构造流水线的依赖。
type Options struct {
	// Adapters 按协议返回适配器。必填。
	Adapters AdapterLookup
	// Upstream 调用上游渠道。必填。
	Upstream domain.UpstreamCaller
	// Routes 返回候选渠道。必填。
	Routes domain.RouteResolver
	// Observer 记录每次上游尝试；可为 nil，记录失败不影响转发结果。
	Observer domain.Observer
	// Usage 记录每次请求进入终态时的用量流水；可为 nil（不记录）。
	// 非流式在回写客户端之前落库，流式在流结束后落库；落库失败由实现记日志，
	// 流水线忽略其返回值，不改变对客户端的响应。
	Usage domain.UsageRecorder
	// Breaker 是渠道熔断器；可为 nil（不熔断，候选渠道按解析顺序逐个尝试）。
	// 非 nil 时，遍历候选时跳过处于熔断打开态的渠道，并在每条渠道尝试（含凭据轮换）结束后
	// 按最终结果上报。实现同时满足 BreakerProber 时，全部候选被拒的请求会放行第一条做探测，
	// 而不是直接返回「所有候选渠道均处于熔断状态」。
	Breaker Breaker
	// Credentials 在渠道尝试内按序切换组内凭据；可为 nil（不轮换，每次尝试只用一份凭据）。
	// 非 nil 时，凭据类失败不消耗尝试预算，改为在同一次渠道尝试内换下一条凭据；
	// 组内试遍后本次渠道尝试才算失败，再按 MaxAttempts 走渠道回退。
	Credentials domain.CredentialRotation
	// Limiter 在进入渠道尝试前获取该渠道的令牌与并发位；可为 nil（不限流）。
	// 未装配时与「渠道未配置上限」同义，流水线不引入任何等待。
	Limiter domain.ChannelLimiter
	// MaxAttempts 是同协议段的尝试上限；<= 0 时取 maxAttemptsDefault。
	MaxAttempts int
	// CrossProtocolAttempts 是跨协议段的尝试上限；<= 0 时取 crossProtocolAttemptsDefault。
	// 两段预算独立计量，跨协议候选存在时该段至少有一次尝试，
	// 使同协议候选全部失败后仍能降到跨协议候选。
	CrossProtocolAttempts int
	// Backoff 是换下一候选前的退避策略；零值字段取对应默认值。
	Backoff BackoffOptions
	// Stream 在流式分片写回客户端之前逐片介入；可为 nil（不介入）。
	// 中间件层由装配层按配置注入，流水线只消费 domain.StreamMiddleware 端口。
	Stream domain.StreamMiddleware
	// Response 在非流式响应体写回客户端之前介入；可为 nil（不介入）。
	Response domain.ResponseMiddleware
}

// Pipeline 是唯一的核心转发流水线。
type Pipeline struct {
	adapters    AdapterLookup
	upstream    domain.UpstreamCaller
	routes      domain.RouteResolver
	observer    domain.Observer
	usage       domain.UsageRecorder
	breaker     Breaker
	credentials domain.CredentialRotation
	limiter     domain.ChannelLimiter
	budget      attemptBudget
	backoff     backoff
	stream      domain.StreamMiddleware
	response    domain.ResponseMiddleware
}

// attemptBudget 是一次请求的按段尝试预算。
//
// 预算按候选段分别计量：同协议段与跨协议段各有自己的额度，因此同协议候选全部失败
// 不会挤掉跨协议降级的机会。总次数另设防御性上限，见 maxTotalAttempts。
// 预算语义在这里一次定义，forward 只消费 allows 的结论，不自行推算还能不能试。
type attemptBudget struct {
	sameProtocol  int
	crossProtocol int
	total         int
}

// allows 报告同协议段已试 sameMade 次、跨协议段已试 crossMade 次、合计 totalMade 次后，
// 本段还能不能再发起一次尝试。
func (b attemptBudget) allows(cross bool, sameMade, crossMade, totalMade int) bool {
	if totalMade >= b.total {
		return false
	}
	if cross {
		return crossMade < b.crossProtocol
	}
	return sameMade < b.sameProtocol
}

// hasCandidateWithin 报告从 from 起的剩余候选里是否至少有一条仍在预算内可试。
// 退避只在确实还有下一次尝试时等待，避免为空转的遍历白等。
func (b attemptBudget) hasCandidateWithin(candidates []domain.Route, from int, client domain.Protocol, sameMade, crossMade, totalMade int) bool {
	if totalMade >= b.total {
		return false
	}
	for i := from; i < len(candidates); i++ {
		cross := candidates[i].Protocol != client
		if b.allows(cross, sameMade, crossMade, totalMade) {
			return true
		}
	}
	return false
}

// attemptResult 是一次上游尝试的结果。Err 为 nil 表示成功。
type attemptResult struct {
	// Completion 是非流式尝试成功时的结果：原始响应字节与归一化响应。
	Completion *domain.UpstreamResult
	// Usage 是本次尝试取得的用量；未取得时为来源未知的零值。只用于尝试记录。
	Usage domain.Usage
	// ResponseParts 是响应侧改写标注，由下沉目标或非流式写出分支填写，
	// 与请求侧标注合并后写入尝试记录。
	ResponseParts domain.RewriteParts
	// CredentialRenewed 报告本次上游响应声明本次凭据登录态已续期：
	// 非流式取自 UpstreamResult，流式取自下沉目标回流。
	CredentialRenewed bool
	// WroteBytes 报告本次尝试是否已向客户端写出过字节：一旦写出就不再换渠道重试。
	WroteBytes bool
	// clientWriteFailed 报告本次尝试的终止错误是否本端向客户端写出失败：
	// 为真时不能再尝试向同一个写出目标补发任何字节。
	clientWriteFailed bool
	// StartedAt 与 EndedAt 是本次上游调用的两个边界时刻，由 doAttempt 紧贴上游调用前后采集。
	StartedAt time.Time
	EndedAt   time.Time
	// RateLimitWait 是本次尝试在渠道限流器上等待的时长，由 limitedAttempt 填入。
	RateLimitWait time.Duration
	// Err 是本次尝试的错误；nil 表示成功。
	Err error
}

// New 构造流水线。依赖缺失在构造时报出，不推迟到请求时。
func New(opts Options) (*Pipeline, error) {
	switch {
	case opts.Adapters == nil:
		return nil, domain.NewError(domain.CodeInternal, "缺少适配器查找函数")
	case opts.Upstream == nil:
		return nil, domain.NewError(domain.CodeInternal, "缺少上游调用器")
	case opts.Routes == nil:
		return nil, domain.NewError(domain.CodeInternal, "缺少候选渠道解析器")
	}
	sameAttempts := opts.MaxAttempts
	if sameAttempts <= 0 {
		sameAttempts = maxAttemptsDefault
	}
	crossAttempts := opts.CrossProtocolAttempts
	if crossAttempts <= 0 {
		crossAttempts = crossProtocolAttemptsDefault
	}
	return &Pipeline{
		adapters:    opts.Adapters,
		upstream:    opts.Upstream,
		routes:      opts.Routes,
		observer:    opts.Observer,
		usage:       opts.Usage,
		breaker:     opts.Breaker,
		credentials: opts.Credentials,
		limiter:     opts.Limiter,
		budget: attemptBudget{
			sameProtocol:  sameAttempts,
			crossProtocol: crossAttempts,
			total:         maxTotalAttempts,
		},
		backoff:  newBackoff(opts.Backoff),
		stream:   opts.Stream,
		response: opts.Response,
	}, nil
}

// Forward 处理一次转发：req.Stream 为 false 时把面向客户端的响应体写入 out，
// 为 true 时把面向客户端的流式字节写入 out。
func (p *Pipeline) Forward(ctx context.Context, client domain.Adapter, req *domain.Request, out io.Writer) error {
	if req == nil {
		return domain.NewError(domain.CodeInvalidRequest, "请求为空")
	}
	if client == nil {
		return domain.NewError(domain.CodeInternal, "缺少客户端适配器")
	}
	if out == nil {
		return domain.NewError(domain.CodeInternal, "转发缺少写出目标")
	}
	if req.Stream {
		return p.forward(ctx, client, req, out, func(attemptCtx context.Context, route domain.Route, body []byte, _ domain.RewriteParts) attemptResult {
			return p.streamAttempt(attemptCtx, client, req, route, body, out)
		})
	}
	return p.forward(ctx, client, req, out, func(attemptCtx context.Context, route domain.Route, body []byte, _ domain.RewriteParts) attemptResult {
		return p.completeAttempt(attemptCtx, client, req, route, body, out)
	})
}

// completeAttempt 执行一次非流式尝试，并在成功时把面向客户端的响应体写入 out。
//
// 字节来源由本次尝试的 route.Protocol 与 req.Protocol 是否一致决定：一致时逐字节写出
// 上游原始响应体，不一致时写出按客户端协议重建的结果。写出权归流水线，因为只有它同时
// 知道两侧协议，也只有它能保证重试时不对客户端重复写出。
func (p *Pipeline) completeAttempt(
	ctx context.Context,
	client domain.Adapter,
	req *domain.Request,
	route domain.Route,
	body []byte,
	out io.Writer,
) attemptResult {
	result := attemptResult{StartedAt: time.Now()}
	completion, callErr := p.upstream.Complete(ctx, route, req, body)
	result.EndedAt = time.Now()
	if callErr != nil {
		result.Err = callErr
		return result
	}
	result.Completion = completion
	result.Usage = completion.Response.Usage
	result.CredentialRenewed = completion.CredentialRenewed
	// 先落 billing_usage 再回写客户端：流水是扣费与对账的事实来源，
	// 客户端拿到响应时它必须已经存在。客户端写出失败不回滚流水——上游已经产生过用量。
	p.recordUsage(ctx, req, route, result.Usage)
	parts, writeErr := p.writeCompletion(ctx, client, req, route, completion, out)
	result.ResponseParts = parts
	if writeErr != nil {
		result.Err = writeErr
		result.WroteBytes = true
		result.clientWriteFailed = true
	}
	return result
}

// writeCompletion 把一次成功的非流式尝试的响应体写入 out，并返回本次写出的响应侧改写标注。
//
// 非流式响应体写回之前先交给中间件层：同协议与跨协议两条路径产出的都是面向客户端的
// 最终字节，中间件看到的就是客户端将看到的内容。
func (p *Pipeline) writeCompletion(
	ctx context.Context,
	client domain.Adapter,
	req *domain.Request,
	route domain.Route,
	completion *domain.UpstreamResult,
	out io.Writer,
) (domain.RewriteParts, error) {
	if completion == nil || completion.Response == nil {
		return nil, domain.NewError(domain.CodeInternal, "上游返回了空响应")
	}
	// 与流式路径同一口径：非流式响应中间件的作用域也按 vendor 判定。
	ctx = domain.WithRouteFacts(ctx, domain.RouteFacts{Vendor: route.Vendor})
	sameProtocol := route.Protocol == req.Protocol
	body := completion.Raw
	if !sameProtocol {
		encoded, err := client.EncodeResponse(completion.Response)
		if err != nil {
			return nil, err
		}
		body = encoded
	}
	if p.response != nil {
		if rewritten := p.response.OnResponse(ctx, req, body); len(rewritten) > 0 {
			body = rewritten
		}
	}
	if _, err := out.Write(body); err != nil {
		return nil, clientWriteError(err)
	}
	if sameProtocol {
		return nil, nil
	}
	return domain.RewriteParts{domain.RewritePartResponseReencoded}, nil
}

// streamAttempt 执行一次流式尝试：按协议是否一致选择透传或重建下沉目标，再把上游分片交给它。
func (p *Pipeline) streamAttempt(
	ctx context.Context,
	client domain.Adapter,
	req *domain.Request,
	route domain.Route,
	body []byte,
	out io.Writer,
) attemptResult {
	// 选路事实在本次尝试的入口挂一次：中间件作用域要按 vendor 判定 provider，
	// 而 vendor 只有选路之后才知道。逐分片挂会白付一次分配。
	ctx = domain.WithRouteFacts(ctx, domain.RouteFacts{Vendor: route.Vendor})
	streamClient := client.NewStream()
	if streamClient == nil {
		return attemptResult{Err: domain.NewError(domain.CodeInternal, "派生流式适配器失败")}
	}
	sink, err := p.newSink(req, route, out, streamClient)
	if err != nil {
		return attemptResult{Err: err}
	}
	result := attemptResult{StartedAt: time.Now()}
	result.Err = p.upstream.Stream(ctx, route, req, body, sink)
	result.EndedAt = time.Now()
	result.Usage = sink.collectedUsage()
	result.WroteBytes = sink.wroteBytes()
	result.clientWriteFailed = sink.writeFailed()
	result.ResponseParts = sink.rewriteParts()
	result.CredentialRenewed = sink.credentialRenewed()
	return result
}

// newSink 按客户端协议与上游协议是否一致选择下沉目标：一致时按原始帧透传，不一致时按客户端协议重建。
//
// 两条路径都套上中间件层的事件改写：网关侧没有中间件时对应字段为 nil，行为与既有透传一致。
func (p *Pipeline) newSink(req *domain.Request, route domain.Route, out io.Writer, streamClient domain.Adapter) (attemptSink, error) {
	if route.Protocol == req.Protocol {
		return &passthroughSink{
			out: out,
			// 客户端未索取用量帧时抑制只承载用量的帧：网关为计费注入了索取开关，
			// 上游因此多发的那一帧不该写回给未索取的客户端。跨协议重建路径不走本目标，
			// anthropic / responses 的用量是协议固有事件，不在此抑制。
			suppressUsageFrames: !req.UsageFramesRequested,
			events:              p.stream,
			req:                 req,
			client:              streamClient,
		}, nil
	}
	model := domain.UpstreamModelName(req.Model, domain.RewriteOptions{UpstreamModel: route.UpstreamModel})
	return newRebuildSink(out, streamClient, model, p.stream, req)
}

// writeStreamError 向客户端下发协议自身的流式错误帧；协议没有该能力时不下发，
// 由入口层按「结束后关闭连接」降级处理。
func writeStreamError(client domain.Adapter, out io.Writer, err error) {
	encoder, ok := client.(domain.StreamErrorEncoder)
	if !ok {
		return
	}
	frame, supported := encoder.EncodeStreamError(err)
	if !supported || len(frame) == 0 {
		return
	}
	_, _ = out.Write(frame)
}

// forward 是流式与非流式两条路径的公共骨架：
//
//  1. 请求校验；
//  2. 解析候选渠道；
//  3. 每条候选渠道内先按序试遍组内凭据（凭据类失败时切换，不消耗尝试预算），
//     该渠道尝试失败后可重试再换下一个候选，不可重试或已向客户端写出字节立即进入终态。
//
// 每次尝试的具体动作由 doAttempt 执行。out 实现 domain.RoutedModelSink 时被注入本次尝试
// 实际使用的事由模型名，使客户端能读到网关最终履约的模型。
func (p *Pipeline) forward(
	ctx context.Context,
	client domain.Adapter,
	req *domain.Request,
	out io.Writer,
	doAttempt func(ctx context.Context, route domain.Route, body []byte, requestParts domain.RewriteParts) attemptResult,
) error {
	if err := req.Validate(); err != nil {
		return err
	}

	candidateRoutes, err := p.routes.Candidates(ctx, req)
	if err != nil {
		return domain.NewError(domain.CodeInternal, "选路失败").WithCause(err)
	}
	if len(candidateRoutes) == 0 {
		return domain.NewError(domain.CodeModelNotFound, fmt.Sprintf("模型 %q 没有可用渠道", req.Model))
	}

	var lastErr error
	var lastRoute domain.Route
	var lastResult attemptResult
	attemptsMade := 0
	// sameAttempts / crossAttempts 分别是同协议段与跨协议段已发起的尝试次数。
	// 两段各自计量，使同协议候选失败耗尽时仍保留跨协议降级的额度。
	sameAttempts := 0
	crossAttempts := 0
	// upstreamCalls 统计本请求实际发出的上游调用次数，供尝试记录编号；
	// 它对凭据切换同样递增，因此同一条渠道上的多次凭据试用在记录里各占一条。
	upstreamCalls := 0
	// breakerProbe 标记本次迭代是「全部候选被熔断拒绝」后放行的探测：跳过熔断检查。
	breakerProbe := false
	for i := 0; i < len(candidateRoutes); i++ {
		route := candidateRoutes[i]
		cross := route.Protocol != req.Protocol
		// 总上限是唯一终止遍历的预算判断；段预算耗尽只跳过该段候选，遍历继续推进，
		// 同协议段用尽后仍能走到跨协议段。
		if attemptsMade >= p.budget.total {
			break
		}
		// 熔断跳过：打开态渠道不参与调度，不计入尝试次数。
		if !breakerProbe && !p.allowRoute(route) {
			p.recordBreakerSkip(ctx, req, route, attemptsMade+1)
			// 走到最后一条候选仍无一放行，说明全部候选都处于熔断打开态：放行第一条候选做一次
			// 探测（宁可试一次），而不是直接把整条链路判为不可用。Allow 会推进半开状态，
			// 无法预先扫描候选集合，只能在遍历到末尾时确认，再把游标拨回第一条并绕过熔断检查。
			// 探测位已满时 Probe 返回 false，此时维持既有的失败封闭。
			if attemptsMade == 0 && i == len(candidateRoutes)-1 {
				if prober, ok := p.breaker.(BreakerProber); ok && prober.Probe(candidateRoutes[0].UpstreamID) {
					breakerProbe = true
					i = -1
				}
			}
			continue
		}
		breakerProbe = false
		// 段预算耗尽：跳过该候选，不记录尝试，也不占用另一段的额度。
		if !p.budget.allows(cross, sameAttempts, crossAttempts, attemptsMade) {
			continue
		}
		body, requestParts, finalizeErr := p.finalizeRequest(req, route)
		if finalizeErr != nil {
			return finalizeErr
		}
		attemptsMade++
		if cross {
			crossAttempts++
		} else {
			sameAttempts++
		}
		p.markRoutedModel(out, route)
		attemptCtx := ctx
		if p.credentials != nil {
			attemptCtx = p.credentials.Begin(ctx, route)
		}
		// 内层按序试遍组内凭据：凭据类失败原地换下一条，不消耗换渠道的尝试预算。
		// 上下文取消或已向客户端写出字节时不再切换，与「流式开写后不重试」同一判据。
		var attempt attemptResult
		for credentialAttempt := 0; ; credentialAttempt++ {
			upstreamCalls++
			attempt = p.limitedAttempt(attemptCtx, route, body, requestParts, doAttempt)
			p.renewCredential(attemptCtx, route, attempt)
			// 在尝试结束后立即回流渠道 id、上游状态码与是否跨协议：重试时后一次覆盖前一次，
			// 请求结束时保留的是最终履约（或最终失败）的那次。
			p.recordAttemptInfo(out, req, route, attempt)
			attempt.ResponseParts = mergeParts(requestParts, attempt.ResponseParts)
			// 换渠道只发生在候选迭代的第一步：同一条候选内的后续尝试都是凭据轮换。
			// attemptsMade 在进入本候选时已自增，故 attemptsMade > 1 即表示本次不是全请求的首条候选。
			p.recordAttempt(ctx, req, route, upstreamCalls, attemptsMade > 1 && credentialAttempt == 0, attempt)
			if attempt.Err == nil || ctx.Err() != nil || attempt.WroteBytes {
				break
			}
			nextCtx, switched := p.advanceCredential(attemptCtx, route, attempt.Err)
			if !switched {
				break
			}
			attemptCtx = nextCtx
		}
		// 熔断按渠道尝试的最终结果上报：中间那几次凭据类失败不单独计入，
		// 否则一把失效 key 会先把渠道判成故障。
		p.recordRouteOutcome(route, attempt.Err)
		if attempt.Err == nil {
			p.recordStreamUsage(ctx, req, route, attempt)
			return nil
		}
		lastErr = attempt.Err
		lastRoute = route
		lastResult = attempt
		if !retryableFailure(attempt.Err) || ctx.Err() != nil || attempt.WroteBytes {
			// 本次尝试已是终态（不可重试、上下文取消或已向客户端写出字节），
			// 流式在此落库；可重试且未写出的失败尝试会换渠道，不产生流水。
			p.recordStreamUsage(ctx, req, route, attempt)
			if attempt.WroteBytes && !attempt.clientWriteFailed {
				// 状态码与响应头已送达客户端，只能下发协议自身的流式错误帧。
				writeStreamError(client, out, attempt.Err)
			}
			return attempt.Err
		}
		if p.budget.hasCandidateWithin(candidateRoutes, i+1, req.Protocol, sameAttempts, crossAttempts, attemptsMade) {
			if delay, ok := p.backoff.delay(attemptsMade, attempt.Err); ok {
				if waitErr := p.backoff.wait(ctx, delay); waitErr != nil {
					break
				}
			}
		}
	}
	if lastErr == nil {
		if attemptsMade == 0 {
			return domain.NewError(domain.CodeUpstreamUnavailable, "所有候选渠道均处于熔断状态")
		}
		return domain.NewError(domain.CodeUpstreamUnavailable, "候选渠道或尝试次数耗尽")
	}
	// 候选耗尽或退避等待被取消：最后一次尝试已是终态，流式在此落库。
	p.recordStreamUsage(ctx, req, lastRoute, lastResult)
	return lastErr
}

// limitedAttempt 在渠道限流器下执行一次上游调用，保证并发位与令牌在本次调用结束后释放。
//
// release 交由 defer 执行：doAttempt 的流式分支在流读完、非流式分支在响应写出后才返回，
// 两条路径都只有在本次上游调用彻底结束后才会释放并发位，不会提前。
//
// 限流等待超时按一次失败的上游尝试返回：错误可重试，由调用方换下一条候选，
// 不把网关自身的节制直接变成对客户端的拒绝。
func (p *Pipeline) limitedAttempt(
	ctx context.Context,
	route domain.Route,
	body []byte,
	requestParts domain.RewriteParts,
	doAttempt func(context.Context, domain.Route, []byte, domain.RewriteParts) attemptResult,
) attemptResult {
	if p.limiter == nil {
		return doAttempt(ctx, route, body, requestParts)
	}
	release, waited, err := p.limiter.Acquire(ctx, route)
	if err != nil {
		endedAt := time.Now()
		return attemptResult{
			Err: err, StartedAt: endedAt.Add(-waited), EndedAt: endedAt, RateLimitWait: waited,
		}
	}
	if release != nil {
		defer release()
	}
	result := doAttempt(ctx, route, body, requestParts)
	result.RateLimitWait = waited
	return result
}

// retryableFailure 报告一次渠道尝试失败能不能换下一条候选。
//
// 上游错误分级给出可重试，或该失败已被标注为凭据类时就换：凭据类失败走完渠道内的凭据轮换
// 后（Advance 返回 false），换一条候选意味着换一组凭据，可能成功；而分级里 4xx 一律不可重试的
// 口径只描述「同一组凭据下重试无益」，不适用于跨渠道。未被标注的 4xx 仍按不可重试处理，
// 免把一次参数错误放大成对候选渠道的逐个试探。
func retryableFailure(err error) bool {
	return domain.Retryable(err) || domain.CredentialRejected(err)
}

// advanceCredential 询问凭据轮换器能否在本次渠道尝试内换下一条凭据；未装配轮换器时恒为否。
func (p *Pipeline) advanceCredential(ctx context.Context, route domain.Route, failure error) (context.Context, bool) {
	if p.credentials == nil {
		return ctx, false
	}
	return p.credentials.Advance(ctx, route, failure)
}

// credentialRenewer 是凭据轮换实现可选具备的能力：解除本次尝试所用凭据的既有冷却。
//
// 声明为消费者侧的可选接口（同 BreakerProber）：解除冷却只对声明续期的厂商有意义，
// 不放进 domain.CredentialRotation 的必备方法集，免得每个实现都被追着补一个空方法。
// 实现未提供本能力时按「无法解除」处理，不影响转发。
type credentialRenewer interface {
	// Renew 解除本次尝试所用凭据的既有冷却；无尝试级状态时为空操作。
	Renew(ctx context.Context, route domain.Route)
}

// renewCredential 在上游声明本次凭据登录态已续期时解除其既有冷却；未装配轮换器时为空操作。
//
// 续期事实有两个来源：错误（非 2xx 响应带 renewed 信标）与成功结果（2xx 响应带 renewed 信标）。
// 除解除冷却外不改变本次尝试的处置：是否换凭据、是否换渠道仍由错误分级与凭据类判定决定。
func (p *Pipeline) renewCredential(ctx context.Context, route domain.Route, attempt attemptResult) {
	if p.credentials == nil {
		return
	}
	if !attempt.CredentialRenewed && !domain.CredentialRenewed(attempt.Err) {
		return
	}
	// 解除冷却不是轮换端口的必备能力：实现未提供时按「无法解除」处理，不影响转发。
	if renewer, ok := p.credentials.(credentialRenewer); ok {
		renewer.Renew(ctx, route)
	}
}

// markRoutedModel 把本次尝试实际使用的事由模型名注入客户端写出目标；目标不支持该能力时为空操作。
func (p *Pipeline) markRoutedModel(out io.Writer, route domain.Route) {
	sink, ok := out.(domain.RoutedModelSink)
	if !ok {
		return
	}
	sink.SetRoutedModel(domain.UpstreamModelName("", domain.RewriteOptions{UpstreamModel: route.UpstreamModel}))
}

// recordAttemptInfo 把本次尝试命中的渠道 id、上游状态码与是否跨协议回流给入口层写出目标；
// 目标不支持该能力时为空操作。
func (p *Pipeline) recordAttemptInfo(out io.Writer, req *domain.Request, route domain.Route, result attemptResult) {
	sink, ok := out.(domain.UpstreamAttemptSink)
	if !ok {
		return
	}
	sink.SetAttemptChannel(route.ChannelID)
	sink.SetUpstreamStatus(upstreamStatusOf(result.Err))
	sink.SetCrossProtocol(route.Protocol != req.Protocol)
}

// upstreamStatusOf 从一次尝试的结果里取出上游 HTTP 状态码。
//
// 成功固定记为 200：各协议的 2xx 成功响应都按 200 处理；失败时由上游客户端把实际状态码
// 附在错误的 UpstreamStatus 能力上，连接类失败没有该能力，记为 0 表示未取得。
func upstreamStatusOf(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var carrier interface{ UpstreamStatus() int }
	if errors.As(err, &carrier) {
		return carrier.UpstreamStatus()
	}
	return 0
}

// allowRoute 询问渠道熔断器是否放行该候选；未装配熔断器时恒放行。
func (p *Pipeline) allowRoute(route domain.Route) bool {
	if p.breaker == nil {
		return true
	}
	return p.breaker.Allow(route.UpstreamID)
}

// recordRouteOutcome 把一次上游尝试的结果上报给渠道熔断器；未装配熔断器时为空操作。
func (p *Pipeline) recordRouteOutcome(route domain.Route, err error) {
	if p.breaker == nil {
		return
	}
	p.breaker.Record(route.UpstreamID, err)
}

// recordBreakerSkip 把一次因熔断打开而被跳过的候选写进尝试记录；未配置观测器时不做任何事。
//
// 跳过的候选没有发起上游调用，因此结果记为 skipped、用量与错误码为空；attempt 取「本来会
// 排到的尝试序号」，使同一请求内被跳过的候选与真实尝试在时间线上可排序。
func (p *Pipeline) recordBreakerSkip(ctx context.Context, req *domain.Request, route domain.Route, attempt int) {
	if p.observer == nil {
		return
	}
	_ = p.observer.RecordAttempt(ctx, domain.AttemptRecord{
		RequestID:        req.RequestID,
		Attempt:          attempt,
		ClientProtocol:   req.Protocol,
		UpstreamProtocol: route.Protocol,
		CrossProtocol:    route.Protocol != req.Protocol,
		RequestedModel:   req.Model,
		UpstreamID:       route.UpstreamID,
		ChannelID:        route.ChannelID,
		UpstreamModel:    route.UpstreamModel,
		Outcome:          domain.AttemptSkipped,
	})
}

// recordAttempt 把一次上游尝试写入观测记录；未配置观测器时不做任何事。
func (p *Pipeline) recordAttempt(
	ctx context.Context,
	req *domain.Request,
	route domain.Route,
	attempt int,
	channelSwitched bool,
	result attemptResult,
) {
	if p.observer == nil {
		return
	}
	outcome := domain.AttemptOK
	if result.Err != nil {
		outcome = domain.AttemptFailed
		if ctx.Err() != nil {
			outcome = domain.AttemptCancelled
		}
	}
	rec := domain.AttemptRecord{
		RequestID:      req.RequestID,
		Attempt:        attempt,
		ClientProtocol: req.Protocol,
		// 上游协议取自本次尝试实际使用的渠道：它与客户端协议不同即表示走了跨协议重建，
		// 这是「响应为什么和客户端请求的形态不一样」的答案。
		UpstreamProtocol: route.Protocol,
		CrossProtocol:    route.Protocol != req.Protocol,
		RequestedModel:   req.Model,
		UpstreamID:       route.UpstreamID,
		ChannelID:        route.ChannelID,
		UpstreamModel:    route.UpstreamModel,
		Outcome:          outcome,
		UpstreamStatus:   upstreamStatusOf(result.Err),
		ChannelSwitched:  channelSwitched,
		Usage:            result.Usage,
		ErrorCode:        errorCode(result.Err),
		ErrorDetail:      errorDetail(result.Err),
		FailureClass:     domain.FailureClassOf(result.Err),
		StartedAt:        result.StartedAt,
		EndedAt:          result.EndedAt,
		RateLimitWait:    result.RateLimitWait,
		RewrittenParts:   result.ResponseParts,
	}
	_ = p.observer.RecordAttempt(ctx, rec)
}

// recordUsage 把一次请求进入终态时的用量交给注入的记账实现；未配置时不记录。
//
// 实现失败只由实现自己记日志：记账是转发的旁路，不得改变对客户端的响应。
func (p *Pipeline) recordUsage(ctx context.Context, req *domain.Request, route domain.Route, usage domain.Usage) {
	if p.usage == nil {
		return
	}
	_ = p.usage.RecordUsage(ctx, domain.UsageRecord{
		RequestID: req.RequestID,
		ChannelID: route.ChannelID,
		// 记录实际履约的上游模型名：定价按履约模型解析，客户端别名不参与定价。
		Model: domain.UpstreamModelName(req.Model, domain.RewriteOptions{UpstreamModel: route.UpstreamModel}),
		// 请求模型用于查渠道倍率：upstream_model_map 以客户端模型名为主键。
		RequestedModel: req.Model,
		Usage:          usage,
	})
}

// recordStreamUsage 在流式尝试进入终态时落库；非流式已在回写客户端之前落库，此处跳过。
func (p *Pipeline) recordStreamUsage(ctx context.Context, req *domain.Request, route domain.Route, attempt attemptResult) {
	if !req.Stream {
		return
	}
	p.recordUsage(ctx, req, route, attempt.Usage)
}

// mergeParts 合并请求侧与响应侧改写标注，去重并保持出现顺序。
func mergeParts(base, extra domain.RewriteParts) domain.RewriteParts {
	if len(extra) == 0 {
		return base
	}
	merged := append(domain.RewriteParts(nil), base...)
	for _, part := range extra {
		if !merged.Has(part) {
			merged = append(merged, part)
		}
	}
	return merged
}

// errorCode 返回错误码字符串；非统一错误返回空串。
func errorCode(err error) string {
	if domainErr := domain.AsError(err); domainErr != nil {
		return string(domainErr.Code)
	}
	return ""
}

// attemptErrorDetailMaxRunes 是写进尝试日志的排障细节字数上限。
const attemptErrorDetailMaxRunes = 256

// errorDetail 返回写进尝试日志的排障细节；非统一错误返回空串。
func errorDetail(err error) string {
	domainErr := domain.AsError(err)
	if domainErr == nil {
		return ""
	}
	return truncateRunes(domainErr.Detail, attemptErrorDetailMaxRunes)
}

// finalizeRequest 把内部请求定稿为上游请求体：同协议走报文改写、跨协议走重建。
func (p *Pipeline) finalizeRequest(req *domain.Request, route domain.Route) ([]byte, domain.RewriteParts, error) {
	builder, err := p.builderFor(route.Protocol)
	if err != nil {
		return nil, nil, err
	}
	options := domain.RewriteOptions{
		MaxOutputTokens: route.OutputLimit,
		UpstreamModel:   route.UpstreamModel,
		// 流式转发向支持该开关的上游索取用量：不注入就拿不到末尾的用量帧。
		IncludeUsage: req.Stream,
		// 渠道 × 模型的参数覆盖项经适配器合并进上游请求体；形状已在选路边界收敛。
		RequestOverrides: route.RequestOverrides,
	}
	if route.Protocol == req.Protocol {
		return builder.RewriteRawBody(req.RawBody, options)
	}
	return builder.EncodeRequest(req, options)
}

// builderFor 按上游协议取请求构建能力。
func (p *Pipeline) builderFor(protocol domain.Protocol) (domain.UpstreamRequestBuilder, error) {
	adapter, err := p.adapters(protocol)
	if err != nil {
		return nil, domain.NewError(domain.CodeInternal,
			fmt.Sprintf("取协议 %q 的适配器失败", string(protocol))).WithCause(err)
	}
	if adapter == nil {
		return nil, domain.NewError(domain.CodeInternal, fmt.Sprintf("协议 %q 没有适配器", string(protocol)))
	}
	builder, ok := adapter.(domain.UpstreamRequestBuilder)
	if !ok {
		return nil, domain.NewError(domain.CodeInternal,
			fmt.Sprintf("协议 %q 的适配器不支持请求构建", string(protocol)))
	}
	return builder, nil
}

// truncateRunes 把字符串按 Unicode 字符数截断到 limit。
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
