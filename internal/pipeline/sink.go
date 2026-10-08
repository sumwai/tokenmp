package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
)

// attemptSink 是单次流式尝试的下沉目标：除 domain.ChunkSink 外，还向流水线报告
//   - 是否已向客户端写出字节（决定能否换渠道重试）
//   - 是否写出失败（决定终态是否还能向该写出目标补发字节）
//   - 响应侧改写标注
//   - 本次尝试取得的用量
type attemptSink interface {
	domain.ChunkSink
	// wroteBytes 报告本次尝试是否已向客户端写出过字节。
	wroteBytes() bool
	// writeFailed 报告本次尝试是否曾向客户端写出并拿到错误，即终止错误是否本端写出失败。
	// 它不按已写字节数判断：写出目标在失败时报告的字节数不可信，可靠事实只有「调用是否返回错误」。
	writeFailed() bool
	// rewriteParts 返回响应侧改写标注。
	rewriteParts() domain.RewriteParts
	// credentialRenewed 报告上游是否在本次 2xx 响应里声明凭据登录态已续期。
	credentialRenewed() bool
	// collectedUsage 返回本次尝试取到的用量：取「最后一个携带用量的分片」，
	// 未取得时返回来源未知的零值。
	collectedUsage() domain.Usage
}

// passthroughSink 是「客户端协议与上游协议一致」时的下沉目标。
//
// 它实现 domain.FrameSink，把上游一帧的原始字节原样写给客户端；分片只用于记账，
// 不再被编码给客户端，避免出现「原始帧 + 网关自己编码的帧」的双写。
type passthroughSink struct {
	out io.Writer
	// suppressUsageFrames 为真时抑制只承载用量的上游帧：客户端未索取用量帧，
	// 网关为计费注入索取开关后上游多发的那一帧不该写给它。用量仍照常记账。
	suppressUsageFrames bool
	// events 是流式逐事件中间件；nil 时不做任何逐事件处置。
	events domain.StreamMiddleware
	// req 是本次请求，供中间件构造钩子上下文。
	req *domain.Request
	// client 是面向客户端的适配器，仅在中间件确实改动了帧时才用于重新编码。
	client domain.Adapter
	// usage 是最后一个携带用量的分片给出的用量；未取得时为零值。
	usage domain.Usage
	// wrote 记录是否已向客户端写出过字节。
	wrote bool
	// failed 记录向客户端写出时是否拿到过错误（写出失败）。
	failed bool
	// renewed 记录上游是否声明本次凭据登录态已续期。
	renewed bool
	// mixedFrames 是「本帧混有非内容分片、中间件未介入」的限频留痕出口；nil 时不记录。
	mixedFrames *mixedFrameLog
}

var (
	_ domain.FrameSink             = (*passthroughSink)(nil)
	_ domain.CredentialRenewalSink = (*passthroughSink)(nil)
	_ attemptSink                  = (*passthroughSink)(nil)
)

// Send 只接受结束分片：EOF 收尾分片（domain.Adapter.FinishStream 的返回值）没有对应的
// 上游帧、没有原始字节可交付，上游调用方经本方法交付它。透传路径不得因收尾分片向客户端
// 多写一个字节，故这里只记录其用量，既不写出任何字节，也不据此把「已向客户端写出字节」
// 置位。其余分片类型走 SendFrame，经本方法到达属调用方错误，仍按内部错误拒绝。
//
// 流末补发也必须在这里做：openai_chat 的结束分片有两条来源（终止帧与 EOF 补齐），
// 后者只经本方法到达。只在 SendFrame 里补发会让这条路上的缓冲尾巴静默丢失。
func (s *passthroughSink) Send(ctx context.Context, chunk domain.Chunk) error {
	if chunk.Kind != domain.ChunkStreamEnd {
		return domain.NewError(domain.CodeInternal, "透传下沉目标不得接收结束分片以外的逐分片调用")
	}
	if s.events != nil {
		s.flushStreamEnd(ctx)
	}
	s.recordUsage(chunk)
	return nil
}

// SendFrame 原样写出上游帧，并先按该帧解出的分片记账。
//
// 分片里含只承载用量的帧且客户端未索取用量时，用量记下、原始字节不写出，也不置位 wrote：
// 这一帧本不产生客户端字节，抑制它不应关闭换渠道重试。判定依据是分片类型而不是字节前缀，
// 无法解析或形状非预期的帧不会产出该分片，因而原样转发，宁多勿丢。
//
// 中间件层只在本帧全部由内容分片组成时才介入：一帧里混有用量或结束分片时，
// 重新编码会丢失那些分片，宁可整帧原样透传。确实改动了分片时按分片重新编码；
// 重新编码不出来（例如协议在编码方向不下发该分片）时同样回退为原样透传。
func (s *passthroughSink) SendFrame(ctx context.Context, raw []byte, chunks []domain.Chunk) error {
	for _, chunk := range chunks {
		s.recordUsage(chunk)
	}
	if s.events != nil {
		// 终止帧之前先让中间件冲刷尾巴：逐事件钩子拿不到流结束分片，
		// 跨分片缓冲的改写没有这个时机就只能丢弃缓冲。
		if carriesTerminal(chunks) {
			s.flushStreamEnd(ctx)
		}
		handled, err := s.applyEvents(ctx, chunks)
		if err != nil {
			return err
		}
		if handled {
			return nil
		}
	}
	if s.suppressUsageFrames && carriesUsageOnlyFrame(chunks) {
		return nil
	}
	s.wrote = true
	if _, err := s.out.Write(raw); err != nil {
		s.failed = true
		return clientWriteError(err)
	}
	return nil
}

// carriesTerminal 报告该帧里的分片是否含面向客户端的收尾信号。
//
// 两种都算：ChunkFinish 是协议自身的结束原因（如 Chat 的 finish_reason），
// ChunkStreamEnd 是整条流的结束（带用量）。部分上游只给前者再直接 EOF，
// 只认后者会让补发排到终止帧之后。
// 一帧可能同时带内容与收尾，不能假定收尾分片独占一帧。
func carriesTerminal(chunks []domain.Chunk) bool {
	for _, chunk := range chunks {
		if chunk.Kind == domain.ChunkStreamEnd || chunk.Kind == domain.ChunkFinish {
			return true
		}
	}
	return false
}

// flushStreamEnd 在终止帧之前向中间件取一次补发分片并写出。
//
// 中间件未实现 domain.StreamEndMiddleware 这一可选端口时零开销 —— 多数中间件不跨分片缓冲。
// 编码不出来或得到空帧的分片跳过而不报错，与逐事件路径同一口径：中间件改写失败只让
// 这次补发落空，不把转发变成失败。
func (s *passthroughSink) flushStreamEnd(ctx context.Context) {
	ender, ok := s.events.(domain.StreamEndMiddleware)
	if !ok {
		return
	}
	for _, chunk := range ender.OnStreamEnd(ctx, s.req) {
		frame, err := s.client.EncodeChunk(chunk)
		if err != nil || len(frame) == 0 {
			continue
		}
		s.wrote = true
		if _, err := s.out.Write(frame); err != nil {
			s.failed = true
			return
		}
	}
}

// applyEvents 在整帧都是内容分片时交给中间件层处置，并返回是否已写完全帧。
//
// 未改动、或重新编码不出来时返回 false，由调用方按原帧透传。
func (s *passthroughSink) applyEvents(ctx context.Context, chunks []domain.Chunk) (bool, error) {
	filtered, changed, drop := s.filter(ctx, chunks)
	if drop {
		return true, nil
	}
	if !changed {
		return false, nil
	}
	return s.writeFiltered(filtered)
}

// filter 逐分片询问中间件层；返回改写后的分片、是否发生改动、是否整帧丢弃。
//
// 只要帧里出现非内容分片就直接放弃介入（changed 与 drop 都为 false），
// 由调用方按原始帧透传。帧里确实含内容分片时另留一条限频告警：那些内容这一帧没有经过
// onEvent，而转发成功、计数不增长，表现与「没导出钩子」一样，只能靠日志看出来。
func (s *passthroughSink) filter(ctx context.Context, chunks []domain.Chunk) (kept []domain.Chunk, changed, drop bool) {
	content := false
	for _, chunk := range chunks {
		if isContentChunk(chunk.Kind) {
			content = true
			break
		}
	}
	// 只承载用量或结束分片的帧：本来就没有内容要处置，不算缺口，也不留痕。
	if !content {
		return nil, false, false
	}
	for _, chunk := range chunks {
		if !isContentChunk(chunk.Kind) {
			s.noteMixedFrame(chunks)
			return nil, false, false
		}
	}
	kept = make([]domain.Chunk, 0, len(chunks))
	for _, chunk := range chunks {
		result := s.events.OnEvent(ctx, s.req, chunk)
		switch {
		case result.Drop:
			changed = true
		case result.Unchanged:
			kept = append(kept, chunk)
		default:
			changed = true
			kept = append(kept, result.Chunk)
		}
	}
	if !changed {
		return nil, false, false
	}
	if len(kept) == 0 {
		return nil, true, true
	}
	return kept, true, false
}

// writeFiltered 按分片重新编码原帧；任一存活分片编码不出来（含编码方向不下发的分片）
// 时返回 wrote=false，由调用方回退为原样透传。
func (s *passthroughSink) writeFiltered(chunks []domain.Chunk) (bool, error) {
	frames := make([][]byte, 0, len(chunks))
	for _, chunk := range chunks {
		frame, err := s.client.EncodeChunk(chunk)
		if err != nil {
			//nolint:nilerr // 编码失败按「无法重建该帧」处理，回退为原样透传，不视为转发失败。
			return false, nil
		}
		if len(frame) == 0 {
			return false, nil
		}
		frames = append(frames, frame)
	}
	s.wrote = true
	for _, frame := range frames {
		if _, err := s.out.Write(frame); err != nil {
			s.failed = true
			return true, clientWriteError(err)
		}
	}
	return true, nil
}

// noteMixedFrame 留痕「本帧的内容分片没有经过中间件」。
//
// 调用点只在「帧里确实含内容分片、同时又有非内容分片」时命中：整帧透传是有意的取舍，
// 但取舍的代价必须能从日志里看见 —— 否则这一帧与「没导出钩子」在生产上无从区分。
func (s *passthroughSink) noteMixedFrame(chunks []domain.Chunk) {
	if s.mixedFrames == nil {
		return
	}
	skipped := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if !isContentChunk(chunk.Kind) {
			skipped = append(skipped, string(chunk.Kind))
		}
	}
	s.mixedFrames.warn(s.req, skipped)
}

// isContentChunk 报告分片是否是逐事件中间件可处置的内容分片。
func isContentChunk(kind domain.ChunkKind) bool {
	switch kind {
	case domain.ChunkTextDelta, domain.ChunkToolCallDelta, domain.ChunkReasoningDelta:
		return true
	default:
		return false
	}
}

// carriesUsageOnlyFrame 报告一帧解出的分片里是否含只承载用量的帧。
//
// 该分片由 openai_chat 解码器对「choices 为空且 usage 非空」的帧产出；
// anthropic / responses 的用量是协议固有事件，不产出它，故本判据不作用于它们。
func carriesUsageOnlyFrame(chunks []domain.Chunk) bool {
	for _, chunk := range chunks {
		if chunk.Kind == domain.ChunkUsage {
			return true
		}
	}
	return false
}

// recordUsage 记下分片携带的用量；后到的用量覆盖先到的。
func (s *passthroughSink) recordUsage(chunk domain.Chunk) {
	if chunk.Usage != nil {
		s.usage = *chunk.Usage
	}
}

func (s *passthroughSink) wroteBytes() bool { return s.wrote }

func (s *passthroughSink) writeFailed() bool { return s.failed }

func (s *passthroughSink) collectedUsage() domain.Usage { return s.usage }

// rewriteParts 报告响应侧改写标注。透传路径逐字节写出上游原始帧、不做任何改写，故恒为空。
func (s *passthroughSink) rewriteParts() domain.RewriteParts { return nil }

// SetCredentialRenewed 记录上游声明本次凭据登录态已续期。
func (s *passthroughSink) SetCredentialRenewed() { s.renewed = true }

func (s *passthroughSink) credentialRenewed() bool { return s.renewed }

// rebuildSink 是「客户端协议与上游协议不一致」时的下沉目标。
//
// 它只实现 domain.ChunkSink：把上游分片按面向客户端的协议重新编码后写出。
// 用量分片只用于记账，不交给编码方向。
type rebuildSink struct {
	out    io.Writer
	client domain.Adapter
	// events 是流式逐事件中间件；nil 时不做任何逐事件处置。
	events domain.StreamMiddleware
	// req 是本次请求，供中间件构造钩子上下文。
	req *domain.Request
	// startFrame 是流开始帧，延迟到首个字节真正要写出时才发，
	// 使「上游在写出前失败」仍可换渠道重试。
	startFrame []byte
	started    bool
	usage      domain.Usage
	wrote      bool
	// failed 记录向客户端写出时是否拿到过错误（写出失败）。
	failed bool
	// renewed 记录上游是否声明本次凭据登录态已续期。
	renewed bool
}

var (
	_ domain.CredentialRenewalSink = (*rebuildSink)(nil)
	_ attemptSink                  = (*rebuildSink)(nil)
)

// newRebuildSink 为一条流构造重建下沉目标，并预先生成流开始帧。
//
// model 是写入开始帧的模型名，由调用方按 Route.UpstreamModel 决定。
func newRebuildSink(out io.Writer, client domain.Adapter, model string, events domain.StreamMiddleware, req *domain.Request) (*rebuildSink, error) {
	startFrame, err := client.EncodeStreamStart(model)
	if err != nil {
		return nil, err
	}
	return &rebuildSink{out: out, client: client, events: events, req: req, startFrame: startFrame}, nil
}

// Send 把一个上游分片编码为面向客户端的帧并写出。
//
// ChunkUsage 只出现在解码方向，必须在此跳过；结束分片按上游给出的用量原样交适配器编码。
func (s *rebuildSink) Send(ctx context.Context, chunk domain.Chunk) error {
	// 终止帧之前先让中间件冲刷尾巴，理由同透传路径。
	if chunk.Kind == domain.ChunkStreamEnd || chunk.Kind == domain.ChunkFinish {
		s.flushStreamEnd(ctx)
	}
	if chunk.Usage != nil {
		s.usage = *chunk.Usage
	}
	// 内容分片先交中间件层处置：丢弃直接返回，改写后的分片继续走原有编码分支。
	if s.events != nil && isContentChunk(chunk.Kind) {
		result := s.events.OnEvent(ctx, s.req, chunk)
		if result.Drop {
			return nil
		}
		if !result.Unchanged {
			chunk = result.Chunk
		}
	}
	// 用量与结束原因都不产生客户端字节：用量只用于记账，结束原因由下游的
	// EncodeStreamEnd 按整条流的状态统一给出，各自单独成帧会让客户端收到两次结束。
	if chunk.Kind == domain.ChunkUsage || chunk.Kind == domain.ChunkFinish {
		return nil
	}
	var (
		frame []byte
		err   error
	)
	switch chunk.Kind {
	// 推理分片与文本、工具调用分片走同一条编码路径：三者都是要写给客户端的内容，
	// 区别只在客户端方言里的字段名。曾经在这里直接丢弃，导致「插件把正文改成推理」
	// 在重建路径上让内容静默消失，而透传路径上只是回退原帧 —— 两条路结果不一致。
	case domain.ChunkTextDelta, domain.ChunkToolCallDelta, domain.ChunkReasoningDelta:
		frame, err = s.client.EncodeChunk(chunk)
	case domain.ChunkStreamEnd:
		frame, err = s.client.EncodeStreamEnd(chunk)
	default:
		return domain.NewError(domain.CodeInternal,
			fmt.Sprintf("流式分片类型 %q 无法下沉", string(chunk.Kind)))
	}
	if err != nil {
		return err
	}
	return s.write(frame)
}

// flushStreamEnd 在终止帧之前向中间件取一次补发分片并写出。
//
// 与透传路径同一语义，但走 s.write 而不是直接写 out：重建路径的首个字节前要先补发流开始帧，
// 绕开它会让补发帧成为客户端收到的第一帧，序列不完整。
func (s *rebuildSink) flushStreamEnd(ctx context.Context) {
	ender, ok := s.events.(domain.StreamEndMiddleware)
	if !ok {
		return
	}
	for _, chunk := range ender.OnStreamEnd(ctx, s.req) {
		frame, err := s.client.EncodeChunk(chunk)
		if err != nil || len(frame) == 0 {
			continue
		}
		if err := s.write(frame); err != nil {
			return
		}
	}
}

// write 在首次写出字节前补发流开始帧，避免客户端收到缺少开头的事件序列。
func (s *rebuildSink) write(frame []byte) error {
	if !s.started {
		s.started = true
		if len(s.startFrame) > 0 {
			s.wrote = true
			if _, err := s.out.Write(s.startFrame); err != nil {
				s.failed = true
				return clientWriteError(err)
			}
		}
	}
	if len(frame) == 0 {
		return nil
	}
	s.wrote = true
	if _, err := s.out.Write(frame); err != nil {
		s.failed = true
		return clientWriteError(err)
	}
	return nil
}

func (s *rebuildSink) wroteBytes() bool { return s.wrote }

func (s *rebuildSink) writeFailed() bool { return s.failed }

func (s *rebuildSink) collectedUsage() domain.Usage { return s.usage }

// rewriteParts 报告响应侧改写标注：确实写出过网关编码字节（含开始帧）时，
// 面向客户端的响应由网关重建。
func (s *rebuildSink) rewriteParts() domain.RewriteParts {
	if !s.wrote {
		return nil
	}
	return domain.RewriteParts{domain.RewritePartResponseReencoded}
}

// SetCredentialRenewed 记录上游声明本次凭据登录态已续期。
func (s *rebuildSink) SetCredentialRenewed() { s.renewed = true }

func (s *rebuildSink) credentialRenewed() bool { return s.renewed }

// clientWriteError 把面向客户端的写出失败包装为不可重试的平台内部错误。
func clientWriteError(err error) error {
	return domain.NewError(domain.CodeInternal, "写出客户端响应失败").WithCause(err)
}

// mixedFrameLogPerSecond 是「中间件未介入该帧」这条告警每秒放行的条数。
//
// 命中该形态的渠道每个请求都会撞上它：不限频时同一句话把日志刷满，完全静默又会让人以为
// 插件在正常工作。
const mixedFrameLogPerSecond = 5

// mixedFrameLog 是「一帧混有非内容分片、中间件未介入该帧」的限频留痕出口。
//
// 配额按流水线实例共享：这是进程级的事实，一条流一个限频器等于没限。被压下的条数在
// 下一条放出的记录里报出，与插件层同类日志同一口径。
type mixedFrameLog struct {
	logger *slog.Logger
	mu     sync.Mutex
	// window 是本秒窗口的起点；count 是窗口内已放行的条数。
	window time.Time
	count  int
	// suppressed 是已被压下的条数，下一条放出的记录里报出后清零。
	suppressed atomic.Int64
}

// newMixedFrameLog 构造留痕出口；logger 为 nil 时返回 nil，调用方按「不记录」处理。
func newMixedFrameLog(logger *slog.Logger) *mixedFrameLog {
	if logger == nil {
		return nil
	}
	return &mixedFrameLog{logger: logger}
}

// warn 记一次「该帧未经中间件」；接收者为 nil（未注入日志）时空操作。
func (l *mixedFrameLog) warn(req *domain.Request, skipped []string) {
	if l == nil {
		return
	}
	if !l.allow() {
		l.suppressed.Add(1)
		return
	}
	// 键名刻意不带模型：共享转发层不得出现 "model" 这类协议字段字面量
	//（见 internal/arch_test.go 的协议字面量红线），模型按 request_id 从请求记录里取。
	attrs := []any{
		"request_id", req.RequestID,
		"protocol", string(req.Protocol),
		"skipped_kinds", skipped,
	}
	if suppressed := l.suppressed.Swap(0); suppressed > 0 {
		attrs = append(attrs, "suppressed", suppressed)
	}
	l.logger.Warn("同协议透传的一帧混有非内容分片，中间件未介入该帧", attrs...)
}

// allow 报告本秒窗口是否还在配额内。
func (l *mixedFrameLog) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.window) >= time.Second {
		l.window = now
		l.count = 0
	}
	if l.count >= mixedFrameLogPerSecond {
		return false
	}
	l.count++
	return true
}
