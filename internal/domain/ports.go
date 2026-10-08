package domain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// Adapter 负责一种线协议与内部统一格式的双向转换。
//
// 四种协议各自实现本接口：
//
//   - OpenAI Chat Completions（POST /v1/chat/completions）；
//   - OpenAI Responses（POST /v1/responses）；
//   - Anthropic Messages（POST /v1/messages）；
//   - Gemini generateContent（POST /v1beta/models/{model}:generateContent 与 :streamGenerateContent）。
//
// 流水线只依赖本接口，不感知协议细节。
//
// 同一适配器同时用于两个方向。
//
// 面向客户端：
//
//   - 请求解码：DecodeRequest；
//   - 响应编码：EncodeResponse；
//   - 流式开始编码：EncodeStreamStart；
//   - 流式分片编码：EncodeChunk；
//   - 流式结束编码：EncodeStreamEnd；
//   - 错误编码：EncodeError；
//   - 内容类型：ContentType / StreamContentType。
//
// 面向上游：
//
//   - 响应解码：DecodeResponse / DecodeStreamFrame；
//   - 流式收尾：FinishStream。
//
// 方法集合是四个协议实现共同遵守的公共契约：新增或删除方法必须同步全部实现，
// 避免适配器各自补私有方法而破坏统一转发流水线。
//
// 解码失败必须返回 domain.Error，错误码用 CodeInvalidRequest。
type Adapter interface {
	// Protocol 返回本适配器对应的线协议。
	Protocol() Protocol
	// NewStream 派生一个用于单次流式响应的适配器实例。
	//
	// 有状态的协议（例如 Anthropic 必须用 message_start 带上内容块下标）需要
	// 一次流一个实例；无状态实现可以直接返回自身。调用方必须为每条并发流
	// 单独调用 NewStream，不得把同一个实例用于多条并发流。
	//
	// 派生出的实例可以继承基础实例上的请求级信息；因此调用方必须先对**同一个基础实例**解码本次请求，
	// 再为本次流派生实例；基础实例不得跨请求复用，否则并发流会串用彼此的请求级信息。
	// 响应侧的模型名不由本机制传递，而是作为EncodeStreamStart 的入参由重建侧给出。
	NewStream() Adapter

	// DecodeRequest 把客户端请求体解码为内部统一请求。RequestID 由调用方注入，若请求体中已带则沿用。
	DecodeRequest(body []byte) (*Request, error)
	// EncodeResponse 把内部统一响应编码为对客户端响应体。
	EncodeResponse(resp *Response) ([]byte, error)
	// EncodeStreamStart 返回流开始帧；无需开始帧的协议返回 nil。
	//
	// model 是本次重建要写入开始帧的上游模型名，由重建侧传入。OpenAI Chat 与
	// OpenAI Responses 忽略入参；Anthropic Messages 用它写入
	// message_start.message.model。
	EncodeStreamStart(model string) ([]byte, error)
	// EncodeChunk 把一个内部统一分片编码为对客户端的流式帧（含协议自身的帧格式）。
	//
	// 约定：
	//   - 本方法只处理 ChunkTextDelta 与 ChunkToolCallDelta；
	//   - 推理分片 ChunkReasoningDelta 在编码方向不下发，实现应显式跳过而不报错；
	//   - 用量、结束原因与流结束一律经 EncodeStreamEnd 的结束分片下发，避免同一语义出现两种下发路径。
	EncodeChunk(chunk Chunk) ([]byte, error)
	// EncodeStreamEnd 把结束分片（Kind 为 ChunkStreamEnd，可携带用量与结束原因）编码为流结束帧；
	// 无需结束帧的协议返回 nil。不带用量的结束分片同样接受，是否输出用量字段由各协议自定
	// （例如 Anthropic 会写一个全零的 usage 对象，因为该协议要求 message_delta 必带 usage）。
	// 协议本身需要多个结束帧时（例如 Anthropic的 message_delta 加 message_stop），可拼接后一并返回。
	EncodeStreamEnd(chunk Chunk) ([]byte, error)
	// EncodeError 把错误编码为对客户端的 HTTP 状态码与响应体。
	EncodeError(err error) (status int, body []byte)
	// ContentType 与 StreamContentType 分别返回非流式与流式响应的 Content-Type。
	ContentType() string
	StreamContentType() string

	// DecodeResponse 把上游返回的完整响应体解码为内部统一响应。
	DecodeResponse(body []byte) (*Response, error)
	// DecodeStreamFrame 把上游返回的一帧流式数据解码为若干内部统一分片。
	// event 为协议事件名（无事件名的协议传空串），data 为帧载荷。
	//
	// 之所以返回切片而不是单个分片：上游可以在同一帧里给出多个载荷
	// （例如 OpenAI 的并行工具调用把多个 tool_calls 放进同一帧），
	// 单个分片的签名会迫使实现静默丢弃其余载荷。
	//
	// 约定：
	//   - 不产生分片时（例如心跳或纯控制帧）实现必须返回 nil 而非空切片，使三个适配器的形状一致、
	//     便于日志与观测统一处理；调用方仍一律以 len(chunks) == 0 判断，不得直接与 nil 比较。
	//   - 同类型载荷按协议数组顺序返回；文本与工具调用出现在同一帧时，先返回文本分片，
	//     再按数组顺序返回各工具调用分片。JSON 对象解码后键序已丢失，故不要求也不可能复现原始键序。
	DecodeStreamFrame(event string, data []byte) ([]Chunk, error)
	// FinishStream 在上游流到达 EOF 时给出本轮收尾分片。
	//
	// 调用方（共享转发层）在读取上游流的循环退出后调用一次，用于回答「这条流是完整结束还是被截断」
	// ——该事实只有读到 EOF 才知道，故不能提前到某一帧内判断。
	//
	// 语义：正常结束时返回恰好一个 ChunkStreamEnd，携带本轮已缓存的模型名、用量与结束
	// 原因，用量对象非 nil（未取得用量时为零值未知）；判定为截断时返回空（nil）。
	// 实现方在本方法内只允许产出 ChunkStreamEnd，不得产出内容、工具调用或用量分片。
	//
	// 幂等由适配器内部的「本轮已产出结束分片」标志承担：协议自身既有真实终止帧、又有
	// 可选哨兵帧时（例如 OpenAI Chat 的 [DONE]），两条路径合计只能产出一个 ChunkStreamEnd。
	// 调用方不得承担按协议去重的职责（不得解析帧、不得自行判断哪条路径该产出）；它只按
	// 一个与协议无关的事实决定是否交付 EOF 收尾分片：本轮是否已产出过 ChunkStreamEnd
	// （见 internal/upstream 的 sawEnd 判据）。该标志与累计状态分开保存：它在同一轮的
	// [DONE] 分支清零累计状态后仍须保持为真，但必须在下一轮起始处复位，否则跨轮复用实例时，
	// 新一轮的 FinishStream 会被上一轮的标志挡住而返回空，被误判为截断。
	//
	// 不采用带 error 的签名：本方法只读实例内已累计的内存状态，没有失败模式；截断由
	// 「本轮是否产出过 ChunkStreamEnd」这一唯一信号表达，不应再引入第二套信号。
	// 结束信号与哨兵帧的区分、以及各协议的实现口径由适配器各自实现。
	FinishStream() []Chunk
}

// EndpointFormat 由适配器声明本协议端点的格式差异，供装配层与上游调用消费。
//
// 既有三种方言的端点段是固定字符串，domain.Protocol 的 EndpointPath / EndpointSegment
// 已能表达；Gemini 的端点路径含模型名（/v1beta/models/{model}:generateContent），
// 且流式形态由路径后缀而非请求体字段表达，固定字符串表达不了。
//
// 因此端点的「客户端怎么匹配、上游怎么拼」由适配器声明；装配层注册与解析客户端路径、
// 上游调用拼接请求地址时只消费本接口，不解析任何协议字面量，
// 也不把格式差异放进路由与转发管道。
//
// 固定端点路径的协议不必实现本接口：调用方回退到 Protocol.EndpointPath / EndpointSegment。
type EndpointFormat interface {
	// ClientPathPrefix 返回客户端端点的固定前缀：以它开头的路径由本协议处理。
	// 固定端点路径的协议返回完整路径（不带结尾斜杠）。
	ClientPathPrefix() string
	// MatchClientPath 报告 path 是否属于本协议；命中时返回路径携带的模型名
	// （路径不含模型名时为空串）与本次是否为流式请求。
	MatchClientPath(path string) (model string, stream bool, ok bool)
	// UpstreamPath 返回上游端点相对渠道根地址的拼接段（以 / 开头）。
	// model 是实际发往上游的模型名；stream 表示本轮是否流式。
	UpstreamPath(model string, stream bool) string
}

// UpstreamQueryProvider 提供以 URL 查询参数注入的上游凭据项。
//
// 不是所有上游都接受请求头形态的凭据（例如 Gemini 兼容 ?key= 的端点），
// 而查询参数无法经请求头承载，因此单独成一个可选能力。
// 实现可选：未实现即为「没有查询参数凭据」，调用方不得据此报错。
type UpstreamQueryProvider interface {
	// UpstreamQuery 返回本次调用要追加到上游地址的查询参数项；无此项时返回 nil。
	UpstreamQuery(ctx context.Context, route Route) (url.Values, error)
}

// CredentialHeaderStyle 描述调用上游时凭据请求头的注入形态。
//
// 取值由上游渠道配置给出，只决定「用哪个请求头、以什么形态拼装凭据」，
// 不含凭据本身；凭据仍按协议从环境变量读入，不因本类型改变来源。
//
// 零值 CredentialHeaderAuto 表示「渠道未配置」，此时按协议现状注入：
// OpenAI Chat 与 OpenAI Responses 用 Authorization: Bearer、Anthropic Messages 用
// x-api-key、Gemini 用 x-goog-api-key。取值域含四个非零取值，「未配置」在数据里只能以
// 「键缺失」表达，因此零值不是合法配置值（Valid 对它返回 false）。
type CredentialHeaderStyle string

const (
	// CredentialHeaderAuto 是零值：渠道未配置该项，按协议现状注入。
	CredentialHeaderAuto CredentialHeaderStyle = ""
	// CredentialHeaderAuthorization 注入 Authorization: Bearer <凭据>。
	CredentialHeaderAuthorization CredentialHeaderStyle = "authorization"
	// CredentialHeaderXAPIKey 注入 x-api-key: <凭据>。
	CredentialHeaderXAPIKey CredentialHeaderStyle = "x-api-key"
	// CredentialHeaderXGoogAPIKey 注入 x-goog-api-key: <凭据>（Gemini 原生形态）。
	CredentialHeaderXGoogAPIKey CredentialHeaderStyle = "x-goog-api-key" //nolint:gosec // G101：这是注入形态名，不是凭据。
	// CredentialHeaderQuery 不注入凭据请求头，改为以查询参数 key=<凭据> 追加到上游地址。
	// 它服务只接受 ?key= 的 Gemini 兼容端点。
	CredentialHeaderQuery CredentialHeaderStyle = "query"
)

// Valid 报告取值是否为受支持的非零取值之一。
//
// 零值（未配置）返回 false：配置写入路径必须把「键缺失」与「键存在但取值非法」
// 分开处理，不得把零值当成一个可写入的取值。
func (s CredentialHeaderStyle) Valid() bool {
	switch s {
	case CredentialHeaderAuthorization, CredentialHeaderXAPIKey, CredentialHeaderXGoogAPIKey, CredentialHeaderQuery:
		return true
	default:
		return false
	}
}

// Route 是一次选路的结果，描述一个可用的上游渠道。
//
// UpstreamID / Protocol / UpstreamModel / BaseURL / Timeout / OutputLimit 是路由必需事实；
// CredentialRef 是不透明引用，明文凭据由装配层的请求头提供者按它解析后注入，Route 不得携带明文。
type Route struct {
	UpstreamID string
	// ChannelID 是上游渠道在 upstream_channel 表里的数字主键，供需要数字渠道 id 的场合使用
	// （用量流水、按渠道聚合的观测）。它与 UpstreamID 分工不同：后者是给日志与熔断键用的可读标识，
	// 不保证是数字，也不保证能反查回渠道行。
	ChannelID uint64
	// Protocol 是上游渠道使用的线协议。调用方据此判定客户端协议与上游协议是否一致，
	// 从而决定走同协议透传还是按内部统一格式重建。
	Protocol      Protocol
	UpstreamModel string
	BaseURL       string
	// Vendor 是渠道的厂商标签（`channel create --vendor`），由运营命名。
	// 它与 ChannelID 分工不同：后者是环境事实（重建库就换号），本字段是可读、可跨环境复现、
	// 可以写进配置的标识。中间件作用域就靠它认定 provider，不靠主键。
	Vendor string
	// CredentialRef 是不透明引用，装配层的请求头提供者按它取出明文凭据并注入请求头。
	// 它本身不携带明文，以免随日志泄露。
	CredentialRef string
	// Headers 是渠道级静态请求头，与凭据头合并后交适配器补协议内置必需头。
	// nil 表示该渠道没有额外请求头。
	Headers http.Header
	// RequestOverrides 是渠道 × 模型级别的请求参数覆盖 JSON，取自 upstream_model_map.request_overrides。
	// nil 表示该映射没有覆盖项。存储层不解释它的结构，合并进上游请求体的动作由请求定稿侧负责。
	RequestOverrides json.RawMessage
	// Timeout 是调用本渠道上游的超时预算。同一个字段按转发形态解释为两种语义：
	//
	//   - 非流式（UpstreamCaller.Complete）：整段上游调用的截止时间，覆盖连接、请求与响应体读取；
	//   - 流式（UpstreamCaller.Stream）：最长无进展时间（空闲超时），底层每有字节到达即重新计时。
	//     注释行与纯空块心跳不产生帧，但同属有进展，按字节到达重置计时。
	//
	// 流式不能用整段截止时间，否则长生成会因整段耗时超过预算而被误杀；
	// 空闲超时既能在上游挂死时及时取消，又不限制正常的长流。
	// 「整段流时长」属传输侧概念，本项目不设该预算。
	Timeout time.Duration
	// OutputLimit 是该渠道 × 该上游模型的有效输出上限；nil 表示未配置，
	// 形状与 RewriteOptions.MaxOutputTokens 一致。
	OutputLimit *int
	// RateLimitQPS 是调用本渠道的令牌桶速率（每秒允许开始的尝试数）；0 表示不限。
	// 限流状态由装配层按渠道 id 缓存，进程内生效；多实例部署时各实例独立计数，
	// 同一渠道的总放行量可达实例数 × 本取值。
	RateLimitQPS int
	// RateLimitConcurrency 是本渠道并发尝试上限；0 表示不限。
	// 流式尝试占位到流终态（成功、截断或客户端断开）才释放。
	RateLimitConcurrency int
	// CredentialHeaderStyle 是调用本渠道上游时凭据请求头的注入形态，由渠道配置给出；
	// 零值 CredentialHeaderAuto 表示未配置，按协议现状注入。
	CredentialHeaderStyle CredentialHeaderStyle
	// SigninHeader 是渠道在 config 里声明的登录态信标头映射；零值表示未声明，
	// 凭据类判定回退到状态码启发式。
	SigninHeader SigninHeader
	// OAuthProfile 是渠道在 config 里声明的 OAuth 端点画像；零值表示未声明，
	// 订阅型凭据取用时不做惰性续期，按原样注入。
	OAuthProfile OAuthProfile
}

// RouteResolver 返回按有效优先级升序排列的候选渠道列表。
//
// 契约：候选按有效优先级升序排列；同一优先级内顺序确定（同一实现重复调用必须给出完全相同的顺序），
// 但不要求两个实现给出同一顺序——装配层的内存实现同优先级按声明顺序。
//
// 返回多个候选是为了让流水线在可重试失败时按顺序回退。
// 返回空列表表示无可用渠道，调用方必须按不可重试错误处理。
type RouteResolver interface {
	Candidates(ctx context.Context, req *Request) ([]Route, error)
}

// UpstreamResult 是一次成功的上游非流式调用的结果。
//
// Raw 是上游返回的原始响应体字节（成功时非空），是同协议非流式路径面向客户端的
// 唯一字节来源；Response 是同一份字节经上游协议适配器 DecodeResponse 归一化的结果，
// 供用量记账与跨协议重建使用。两者表达同一份响应，不允许分别来自两次读取。
type UpstreamResult struct {
	Raw      []byte
	Response *Response
	// CredentialRenewed 报告本次 2xx 响应里上游声明本次凭据的登录态已续期：
	// 调用方据此解除该凭据的既有冷却。未声明信标头或取值未命中时为 false。
	CredentialRenewed bool
}

// UpstreamCaller 调用一个具体上游渠道，并返回归一化结果。
//
// 两个方法都必须把上游错误转换为 domain.Error，并据此标注可重试性。
type UpstreamCaller interface {
	// Complete 发送定稿后的上游请求体并返回归一化结果。
	//
	// body 是流水线定稿的上游请求体：同协议透传时是 RewriteRawBody 改写后的原始报文，
	// 跨协议时是 EncodeRequest 的重建结果。req 只用于观测归属与用量关联，
	// 调用方不得用 req 重新构造请求体。
	//
	// 返回的 UpstreamResult 同时携带上游原始响应体字节与归一化响应。面向客户端的响应体字节由流水线生产
	// （同协议写出 Raw、跨协议写出 EncodeResponse(Response)），本方法不写客户端字节。
	Complete(ctx context.Context, route Route, req *Request, body []byte) (*UpstreamResult, error)
	// Stream 与 Complete 的入参含义相同，改为把上游流式分片写入 sink。
	Stream(ctx context.Context, route Route, req *Request, body []byte, sink ChunkSink) error
}

// CredentialRotation 在一次渠道尝试内按序取用组内凭据，并在凭据类失败后推进到下一条。
//
// 之所以把这件事抽成端口而不是让流水线直接管凭据：组内有哪些凭据、轮换起点、冷却与
// 试用上限都是装配层（凭据来源 + 进程内状态）的事实，流水线只消费两个结果——
// 本次尝试该在哪个上下文里发请求、失败后能否原地再试一条。流水线因此不导入凭据包，
// 也不感知凭据来源（数据库、内存表或密钥管理）。
type CredentialRotation interface {
	// Begin 开始一次渠道尝试的轮换，返回携带本次尝试所选凭据的上下文。
	// 后续的凭据解析与上游请求都必须使用该上下文。
	Begin(ctx context.Context, route Route) context.Context
	// Advance 在本次尝试失败后推进到组内下一条可用凭据，返回携带新选择的上下文。
	//
	// 返回 false 表示该失败不属凭据类，或组内已无未试过的可用凭据；
	// 两种情形调用方都按「本次渠道尝试失败」处理，不得原地重试。
	Advance(ctx context.Context, route Route, failure error) (context.Context, bool)
}

// ChannelLimiter 在进入渠道尝试前获取该渠道的令牌与并发位。
//
// 限流状态是进程内的：多实例部署时每个实例各自按配置放行，同一渠道的实际放行量
// 可达实例数 × 配置上限。需要全局一致时必须在实现侧引入外部存储，本端口不承担该职责。
//
// 实现按渠道缓存，流水线只消费「这次尝试能不能开始」这一个结果，
// 不感知令牌桶与信号量的实现。
type ChannelLimiter interface {
	// Acquire 获取 route 对应渠道的令牌与并发位。
	//
	// 返回的 release 必须在本次渠道尝试（含流式全程）结束后调用且只调用一次：
	// 流式请求要一直占着并发位到流终态。err 非 nil 时 release 为 nil，调用方不得调用。
	//
	// 令牌不足时在 ctx 与实现自身的等待上限内短暂等待；等待超时返回可重试错误，
	// 调用方据此换下一条候选，而不是把失败直接回给客户端。
	//
	// waited 是本次在限流器上等待的时长，供尝试日志记录是否命中限流；未等待时为 0。
	Acquire(ctx context.Context, route Route) (release func(), waited time.Duration, err error)
}

// UpstreamRequestBuilder 把内部统一请求转换为上游协议请求体。
//
// 之所以新增独立接口而不继续给 Adapter 加方法：Adapter 的方法集合是四个协议实现
// 共同遵守的公共契约，且已有四个协议实现；请求侧的构建能力
// 只有共享转发层需要，扩大 Adapter 会让四个协议实现一起被迫改动。
type UpstreamRequestBuilder interface {
	// EncodeRequest 按内部统一格式重建上游请求体，用于跨协议转发路径。
	//
	// 模型名取 options.UpstreamModel：非空时用它，空串时用 req.Model。
	// 调用方不再靠修改 req.Model 传递上游模型名。
	//
	// 第二个返回值报告本次重建相对客户端请求实际改写过的部分，填报口径与RewriteRawBody 相同。
	EncodeRequest(req *Request, options RewriteOptions) ([]byte, RewriteParts, error)
	// RewriteRawBody 对同协议透传的原始报文做字段级改写。
	//
	// 没有任何改写项启用时必须逐字节返回入参。启用改写时只改动 RewriteOptions
	// 明确列出的字段，其余字段的取值保持原样。
	//
	// 改写项当前包含：
	//
	//   - 输出上限（max_tokens 等字段的钳制与补齐）；
	//   - 索取用量开关（stream_options 等字段的注入）；
	//   - 模型名（上游模型名的替换与补齐）。
	//
	// 模型名一项的口径：options.UpstreamModel 非空时替换或补齐原始报文的顶层模型字段。
	//
	// 第二个返回值由产生产物的适配器按实际发生的变化填报，调用方只负责把它与
	// 响应侧标注合并后写入 AttemptRecord.RewrittenParts。
	// 调用方不能按「产物与入参逐字节比对」自行判定：
	//
	//   - 要区分改写项必须知道各协议的字段名；
	//   - 「客户端是否已声明索取用量」只有解析过请求体的适配器知道。
	//
	// 填报判据：
	//
	//   - 只在产物相对客户端原始请求确实发生变化时才填对应取值；
	//   - 没有任何改写项启用、或启用了但实际未触发时，逐字节返回入参且不留任何标注；
	//   - 无法判定是否变化时不得猜测填写（宁缺勿假）。
	RewriteRawBody(body []byte, options RewriteOptions) ([]byte, RewriteParts, error)
}

// UpstreamHeaderProvider 提供调用上游时必须携带的请求头。
//
// 上游必需的请求头属于协议自身的表达能力（例如 Anthropic Messages 必须携带版本头），
// 不能由共享转发层硬编码；渠道配置可以在其之上覆盖或补充。
type UpstreamHeaderProvider interface {
	// UpstreamHeaders 返回调用上游时应携带的请求头：适配器内置必需头与channelHeaders（渠道配置）合并，
	// 同名时 channelHeaders 覆盖内置值，其余 channelHeaders 原样补充。返回新 map，不修改入参。
	//
	// channelHeaders 为 nil 时按空请求头处理，返回值必须是非 nil 的新 map 且调用方可直接写入。
	//
	// 使用标准库 http.Header 承载，避免 domain 依赖具体 HTTP 客户端库。
	UpstreamHeaders(channelHeaders http.Header) http.Header
}

// CloneHeader 返回标准库 http.Header 的一份独立拷贝。
//
// nil 入参返回非 nil 的空 map：http.Header 的 Clone 方法在接收者为 nil 时返回 nil，
// 调用方写入会 panic，与 UpstreamHeaderProvider「返回新 map」的契约不符。
func CloneHeader(header http.Header) http.Header {
	cloned := make(http.Header, len(header))
	for key, values := range header {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

// StreamErrorEncoder 把错误编码为协议自身的流式错误帧。
//
// 上游在结束标记出现前断开连接时，网关需向客户端下发该协议的流式错误帧而非直接关闭连接。
type StreamErrorEncoder interface {
	// EncodeStreamError 把 err 编码为本协议在流式阶段的错误帧。
	//
	// supported 为 false 表示该协议没有流式错误帧；此时 frame 为 nil，调用方必须按
	// 「无法下发结构化错误」降级处理（结束后关闭连接，或改用 EncodeError 的非流式错误响应），
	// 不得把 nil 当成已下发成功。
	EncodeStreamError(err error) (frame []byte, supported bool)
}

// ChunkSink 接收归一化流式分片。
//
// Send 返回错误表示下游已不可写（例如客户端断开）；调用方必须立即取消上游且不得继续读取。
type ChunkSink interface {
	Send(ctx context.Context, chunk Chunk) error
}

// FrameSink 接收上游一帧的原始字节与该帧解出的全部分片，用于响应侧按帧粒度原样透传。
//
// 下沉模式由 sink 是否实现本接口决定：实现时 UpstreamCaller.Stream 对每一帧只调用
// SendFrame，一次交付一帧的原始字节与该帧解出的全部分片（无分片时为空）；未实现时
// 只逐分片调用 Send。EOF 收尾分片（Adapter.FinishStream 的返回值）没有对应的上游帧、
// 没有原始字节可交付，两种模式下一律经 Send 交付，因此实现本接口时 Send 也并非
// 完全不被调用。消费者（共享转发流水线）按本接口命名该能力，不得在包内自造同义接口。
//
// 契约：
//   - raw 取自 SSE（Server-Sent Events，服务器发送事件）帧的原始字节，含字段行与结尾空行；
//     心跳与纯注释块不产生帧、不会被交付。
//   - raw 是面向客户端的唯一字节来源；chunks 只用于记账与观测，不得再被编码给客户端。
//   - 实现方不得就地修改该切片。
type FrameSink interface {
	ChunkSink
	// SendFrame 接收上游一帧的原始字节与该帧解出的全部分片。
	SendFrame(ctx context.Context, raw []byte, chunks []Chunk) error
}

// RoutedModelSink 接收本次实际履约的上游模型名。
//
// 跨渠道或跨协议转发后，响应体里的模型名可能不等于客户端请求的模型名；面向客户端的
// 写出目标实现本接口时，流水线把最终履约的模型名注入它（例如写成响应标头），
// 使客户端能读到网关实际履约的模型。
type RoutedModelSink interface {
	// SetRoutedModel 注入最终履约模型名；实现可忽略空串。
	SetRoutedModel(model string)
}

// CredentialRenewalSink 接收「上游在一次 2xx 响应里声明本次凭据登录态已续期」这一事实。
//
// 与 RoutedModelSink 同一机制：该事实只有上游客户端知道，而解除冷却的动作在流水线侧；
// 流式路径没有承载该事实的返回值，经下沉目标回流可避免为它引入请求级全局状态。
// 下沉目标不实现本接口时，上游客户端跳过回流，不影响转发。
type CredentialRenewalSink interface {
	// SetCredentialRenewed 记录本次凭据的登录态已续期。
	SetCredentialRenewed()
}

// UpstreamAttemptSink 接收一次上游尝试的归属事实，供入口层汇总访问日志。
//
// 它与 RoutedModelSink 同一机制：渠道 id 与上游状态码只有流水线知道，而汇总日志的时机在
// 入口层请求结束处；经写出目标回流可以避免引入请求级全局状态。写出目标不实现本接口时，
// 流水线跳过回流，不影响转发。
//
// 一次请求可能产生多次尝试（重试与回退），实现方只需保留最后一次的取值：
// 访问日志记录的是最终履约（或最终失败）的那次。
type UpstreamAttemptSink interface {
	// SetAttemptChannel 记录本次尝试命中的渠道数字主键；0 表示尚未确定。
	SetAttemptChannel(channelID uint64)
	// SetUpstreamStatus 记录本次尝试的上游 HTTP 状态码；0 表示未取得（例如连接失败）。
	SetUpstreamStatus(status int)
	// SetCrossProtocol 记录本次尝试的上游协议是否与客户端协议不同：
	// 为真即表示本次转发走了跨协议重建。两侧协议名与其他归属事实同属尝试级事实，
	// 由流水线在同一时机回流，访问日志据此标记本次请求是否被跨协议降级。
	SetCrossProtocol(cross bool)
}

// AttemptOutcome 是一次上游尝试的结果分类。
//
// AttemptSkipped 用于被熔断器过滤、未发起上游调用的候选：它占一条尝试记录的位置，
// 但不代表发生过上游调用，用量与耗时都是零值。
type AttemptOutcome string

const (
	AttemptOK        AttemptOutcome = "ok"
	AttemptFailed    AttemptOutcome = "failed"
	AttemptCancelled AttemptOutcome = "cancelled"
	// AttemptSkipped 表示该候选因熔断打开被跳过，没有发起上游调用。
	// 与 failed 的区别是：failed 代表一次真实的上游调用失败，skipped 只说明候选在调度阶段
	// 就被熔断器过滤，排障时据此区分「上游真的坏了」与「网关主动不再打它」。
	AttemptSkipped AttemptOutcome = "skipped"
)

// AttemptRecord 是一次上游尝试的可观测记录。
//
// 一次请求可能产生多条 AttemptRecord（重试与回退），与请求级记录共同构成排障所需的时间线。
//
// 本类型是「这次尝试到底发了什么、上游回了什么」的完整事实来源：没有数据库之后，
// 排查只能依靠这些字段，因此协议两侧、模型名两侧与上游用量都要照实记录，
// 而不是只留一个笼统的结果。
//
// Outcome 为 AttemptSkipped 的记录代表候选在调度阶段就被熔断器过滤，没有发起上游调用；
// 这类记录的用量、耗时与上游状态码都是零值，其存在只为回答「网关为什么没有打这条渠道」。
type AttemptRecord struct {
	RequestID string
	// Attempt 是本次尝试的序号；被跳过的候选取「本来会排到的序号」。
	Attempt int
	// ClientProtocol 是客户端使用的线协议，UpstreamProtocol 是本次尝试发往上游的线协议。
	// 两者不同即表示网关做了跨协议重建，使用方据此判定「这次转发转换过协议」。
	ClientProtocol   Protocol
	UpstreamProtocol Protocol
	// CrossProtocol 是上述两个协议是否不同的显式结论，由生产端在两者都能拿到的地方计算
	// （否则消费方必须自己再比一次）。为真表示本次尝试的上游渠道说的是另一种方言，
	// 请求与响应都由适配器按统一内部格式重建；消费方据此标记跨协议降级而不必重复推导。
	CrossProtocol bool
	// RequestedModel 是客户端请求里的模型名，UpstreamModel 是实际写入上游请求体的模型名。
	// 两者不同即表示网关改写过模型名。
	RequestedModel string
	UpstreamID     string
	// ChannelID 是本次尝试命中的渠道数字主键，与 Route.ChannelID、UsageRecord.ChannelID 同源；
	// 0 表示未确定。它与 UpstreamID 分工不同：后者是给人看的可读标识，本字段供按渠道聚合
	// 的观测与用量对账使用，两者不得互相替代。
	ChannelID     uint64
	UpstreamModel string
	Outcome       AttemptOutcome
	// UpstreamStatus 是本次尝试取得的上游 HTTP 状态码：成功为 200，连接类失败等未取得时为 0。
	// 它与访问日志的 upstream_status 同源；上游返回的报文片段见 ErrorDetail。
	UpstreamStatus int
	// ChannelSwitched 报告本次尝试相对同一请求的上一条尝试是否换到了另一条候选渠道：
	// 首次尝试与同一渠道内的凭据轮换均为 false。它与 Attempt 编号结合，使「渠道回退」
	// 与「渠道内凭据轮换」在记录里可区分，而不必要求消费方自行跟踪上一条记录。
	ChannelSwitched bool
	// Usage 是上游本次尝试陈述的用量；未取得时为来源未知的零值（见 Usage.Known）。
	Usage Usage
	// ErrorCode 是本次失败尝试的错误码；成功时为空串。
	ErrorCode string
	// ErrorDetail 是本次失败尝试的排障细节：上游返回的 HTTP 状态码与响应体片段，
	// 流式路径上是上游错误帧给出的原文。成功尝试或上游未返回可读报文时为空串。
	// 内容来自上游响应，不含本网关注入的凭据与请求头；长度由各生产端自行约束。
	ErrorDetail string
	// FailureClass 是本次失败的分类名（如 quota、credit、rate_limit），由上游分类器给出，
	// 未归类时为空串。它只描述事实、不参与控制流，供按类聚合与后续调参。
	FailureClass string
	StartedAt    time.Time
	EndedAt      time.Time
	// RateLimitWait 是本次尝试在渠道限流器上等待令牌与并发位的时长；0 表示未等待
	// （令牌即时可用、渠道未配置限流或本次尝试未进入限流器）。
	// 等待超时的失败尝试另在 ErrorCode 上体现为 upstream_rate_limited。
	RateLimitWait time.Duration
	// RewrittenParts 列出本次尝试中被网关改写过的报文部分；为空表示未改动任何部分。
	// 用于让「网关动过哪些部分」在尝试记录与日志中可审计。
	RewrittenParts RewriteParts
}

// Observer 记录一次上游尝试。实现失败不得影响转发结果，调用方可忽略其错误。
type Observer interface {
	RecordAttempt(ctx context.Context, rec AttemptRecord) error
}

// UsageRecord 是一次进入终态的转发的用量归属。
//
// 流水线只交出与协议无关的事实：归属商家与账户由实现从请求上下文解析，
// 流水线不感知鉴权，也不需要知道流水要写进哪张表。
type UsageRecord struct {
	// RequestID 关联本次请求的观测记录。
	RequestID string
	// ChannelID 是本次实际履约的渠道数字主键。
	ChannelID uint64
	// Model 是实际发往上游的模型名：计费按履约模型而不是客户端请求的别名。
	Model string
	// RequestedModel 是客户端请求的模型名（对外别名）。定价按履约模型解析，
	// 渠道倍率（upstream_model_map.price_multiplier）按请求模型查，两者可能不同。
	RequestedModel string
	// Protocol 是客户端使用的线协议。
	Protocol Protocol
	// CrossProtocol 记录客户端协议与实际履约的上游协议是否不同。
	CrossProtocol bool
	// Usage 是本次请求进入终态时的用量；未取得时是来源未知的零值。
	Usage Usage
}

// UsageRecorder 记录一次转发的用量流水。
//
// 与 Observer 的分工：Observer 每次上游尝试都记一条（含重试），UsageRecorder 只在
// 一次请求进入终态时写一行流水，因此重试的失败尝试不产生流水。
// 实现失败不得影响转发结果，调用方忽略其返回值。
type UsageRecorder interface {
	RecordUsage(ctx context.Context, rec UsageRecord) error
}

// 上游错误的分类与处置统一由 internal/failure 给出：本包不再声明「哪些类型名不可重试」
// 这类判据。同一套字面量原先在这里被三个适配器共用，但它回答不了「可换渠道重试的
// 请求级错误」以外的情形（如把认证失败只当「不可重试」而不停用凭据），
// 而 per-class 的处置表才是调用方真正需要的东西。
