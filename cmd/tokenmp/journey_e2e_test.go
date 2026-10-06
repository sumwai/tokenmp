//go:build e2e

// 端到端运营剧本：从商家入驻到对账，一条贯穿全流程的可重复验证。
//
// 覆盖 7 步运营路径（详见各步骤的测试名与注释）：
//
//  1. 迁移就位 + admin 服务层全链（商家→渠道→凭据→模型映射→定价→开户→发 key→充值→买 token 包）
//  2. 启动 serve（真实监听端口）+ 进程内假上游
//  3. 四方言 × 流式/非流式调用：200、流水落库、结算五字段、账本可复算
//  4. 限额演练：input_token 超限 429 quota_exceeded → reset → 放行；
//     request 计数限额前 3 次放行、第 4 次 429
//  5. 凭据轮换：第一把 401、第二把成功，流水仅一行
//  6. 跨协议：客户端方言与渠道方言不一致时成功转换
//  7. admin 回读：usage list 与 bucket list 数值与剧本断言一致
//
// 为什么放在 cmd/tokenmp 包内：剧本是 make e2e 的入口，直接以二进制所在的包为运行单位；
// serve 的装配与运行已下沉到 internal/gateway，本文件经导出入口 gateway.New/gateway.Run
// 复用生产装配路径，不另写一份装配件。
// 取舍是用构建标签隔离（//go:build e2e），因此不进 make check；需要真实 MySQL，
// DSN 经 TOKENMP_TEST_MYSQL_DSN 传入，与 check-integration 同一口径，未设置时整体 SKIP。
//
// 运行：make e2e。测试自带数据清理，连跑两次不会因残留冲突。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sumwai/tokenmp/internal/access"
	"github.com/sumwai/tokenmp/internal/adapters/anthropic"
	"github.com/sumwai/tokenmp/internal/adapters/gemini"
	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/adapters/openairesponses"
	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/gateway"
	"github.com/sumwai/tokenmp/internal/store"
)

// e2eDSNEnv 与 check-integration 同一环境变量名：都指向可丢弃的库。
const e2eDSNEnv = "TOKENMP_TEST_MYSQL_DSN"

// 剧本使用的模型别名与对应的上游模型名。别名是客户端请求里的名字，
// 上游模型名是网关改写后写入上游、也是定价解析所用的名字。
const (
	e2eBasicModel          = "e2e-basic"
	e2eBasicUpstreamModel  = "up-e2e-basic"
	e2eCrossModel          = "e2e-cross"
	e2eCrossUpstreamModel  = "up-e2e-cross"
	e2eRotateModel         = "e2e-rotate"
	e2eRotateUpstreamModel = "up-e2e-rotate"
)

// 假上游收到的凭据取值。rejected 用于轮换演练，其余为各渠道的正常凭据。
const (
	e2eChatKey      = "e2e-upstream-chat"
	e2eResponsesKey = "e2e-upstream-responses"
	e2eMessagesKey  = "e2e-upstream-messages"
	e2eGeminiKey    = "e2e-upstream-gemini"
	e2eRejectedKey  = "e2e-upstream-rejected"
	e2eRotateOKKey  = "e2e-upstream-rotate-ok"
)

// 记账口径，全部为常量：断言与账本复算共用同一份数字。
const (
	// 假上游固定上报的用量。
	e2eInputTokens  = 12
	e2eOutputTokens = 7
	// 定价分量：input 1 token/个、output 2 token/个，结算单位都是 token。
	e2eInputUnitPrice  = "1"
	e2eOutputUnitPrice = "2"
	// 渠道倍率（模型映射的 price_multiplier）；账户倍率保持默认 1。
	e2eChannelMultiplier = "1.5"
	// 单次请求的基础消费 = 12×1 + 7×2 = 26 token，乘 1.5 后应扣 39 token。
	e2eGrossAmount      = "26"
	e2eMultiplier       = "1.5"
	e2eTokensPerRequest = "39"
	// token 包每份数量与充值金额。
	e2eTokenPackageQty = "1000000"
	e2eRechargeAmount  = "100"
	// 限额演练的额度：等于单次请求的输入 token 数，故一次即用满、第二次被拦。
	e2eQuotaLimit = "12"
	// request 维度限额演练的额度：前 3 次放行、第 4 次被拦。
	e2eRequestQuotaLimit = "3"
	// 假上游回答的正文，用于确认客户端拿到的是本次上游应答而不是空响应。
	e2eUpstreamText = "e2e-response"
	// http.StatusOK 一类的字面量不在此重复，直接用标准库取值。
)

// journeyDialect 是一个客户端方言及其请求构造入口。
type journeyDialect struct {
	name     string
	protocol domain.Protocol
}

// journeyDialects 是剧本要覆盖的四种客户端方言。
var journeyDialects = []journeyDialect{
	{name: "openai_chat", protocol: domain.ProtocolOpenAIChat},
	{name: "openai_responses", protocol: domain.ProtocolOpenAIResponses},
	{name: "anthropic_messages", protocol: domain.ProtocolAnthropicMessages},
	{name: "gemini_generate", protocol: domain.ProtocolGeminiGenerate},
}

// e2eUpstream 是进程内假上游：按请求路径判定方言，用同一套适配器编码应答，
// 并记录收到的凭据与路径，供轮换与跨协议断言。
type e2eUpstream struct {
	server   *httptest.Server
	adapters map[domain.Protocol]domain.Adapter

	mu       sync.Mutex
	keyHits  map[string]int
	pathHits map[string]int
	// modelHits 按上游实际收到的模型名计数，供剧本断言模型名替换。
	modelHits map[string]int
}

// newE2EUpstream 起一个假上游并注册关闭；调用方必须传入生命周期覆盖全部步骤的 t。
func newE2EUpstream(t *testing.T) *e2eUpstream {
	t.Helper()
	u := &e2eUpstream{
		adapters: map[domain.Protocol]domain.Adapter{
			domain.ProtocolOpenAIChat:        openaichat.New(),
			domain.ProtocolOpenAIResponses:   openairesponses.New(),
			domain.ProtocolAnthropicMessages: anthropic.New(),
			domain.ProtocolGeminiGenerate:    gemini.New(),
		},
		keyHits:   map[string]int{},
		pathHits:  map[string]int{},
		modelHits: map[string]int{},
	}
	u.server = httptest.NewServer(http.HandlerFunc(u.handle))
	t.Cleanup(u.server.Close)
	return u
}

// handle 是假上游的请求处理：认证失败回 401，否则按 stream 开关回非流式或 SSE。
func (u *e2eUpstream) handle(w http.ResponseWriter, r *http.Request) {
	protocol, ok := e2eProtocolForPath(r.URL.Path)
	if !ok {
		http.Error(w, "假上游不支持该路径 "+r.URL.Path, http.StatusNotFound)
		return
	}
	adapter := u.adapters[protocol]

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "假上游读取请求体失败", http.StatusInternalServerError)
		return
	}
	var fields struct {
		Stream bool   `json:"stream"`
		Model  string `json:"model"`
	}
	if unmarshalErr := json.Unmarshal(raw, &fields); unmarshalErr != nil {
		http.Error(w, "假上游无法解析请求体", http.StatusBadRequest)
		return
	}
	// Gemini 的模型名与流式形态在路径上，其余方言在请求体里；统一在此收敛成两个变量。
	model, stream := fields.Model, fields.Stream
	if protocol == domain.ProtocolGeminiGenerate {
		if upstreamModel, ok := e2eGeminiUpstreamModel(r.URL.Path); ok {
			model = upstreamModel
		}
		stream = strings.Contains(r.URL.Path, ":streamGenerateContent")
	}

	key := e2eUpstreamKey(r.Header, r.URL.Query())
	u.record(u.keyHits, key)
	u.record(u.pathHits, r.URL.Path)
	u.record(u.modelHits, model)
	if key == e2eRejectedKey {
		w.Header().Set("Content-Type", access.JSONContentType)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"上游拒绝本次凭据"}}`)
		return
	}

	resp := e2eFakeResponse(model)
	if !stream {
		body, encodeErr := adapter.EncodeResponse(resp)
		if encodeErr != nil {
			http.Error(w, "假上游编码响应失败", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", adapter.ContentType())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}

	frames, err := e2eBuildStreamFrames(adapter.NewStream(), model, resp)
	if err != nil {
		http.Error(w, "假上游编码流式响应失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", adapter.StreamContentType())
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, frame := range frames {
		if _, err := w.Write(frame); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// record 在互斥锁下累加一次计数。
func (u *e2eUpstream) record(counter map[string]int, key string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	counter[key]++
}

// keyHit 返回某份凭据被上游收到的次数。
func (u *e2eUpstream) keyHit(key string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.keyHits[key]
}

// pathHit 返回某条上游端点路径被收到的次数。
func (u *e2eUpstream) pathHit(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.pathHits[path]
}

// modelHit 返回某个上游模型名被收到的次数。
func (u *e2eUpstream) modelHit(model string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.modelHits[model]
}

// e2eProtocolForPath 由上游地址的路径判定方言，与网关拼上游地址的口径一致。
// 固定端点方言比对端点段；Gemini 的上游路径是 /models/{model}:generateContent 形态，
// 与客户端前缀不同，故按方法后缀识别并顺带取出模型名。
func e2eProtocolForPath(path string) (domain.Protocol, bool) {
	if _, ok := e2eGeminiUpstreamModel(path); ok {
		return domain.ProtocolGeminiGenerate, true
	}
	for _, dialect := range journeyDialects {
		if dialect.protocol == domain.ProtocolGeminiGenerate {
			continue
		}
		if strings.HasSuffix(path, dialect.protocol.EndpointSegment()) {
			return dialect.protocol, true
		}
	}
	return "", false
}

// e2eGeminiUpstreamModel 从上游路径 /models/{model}:generateContent 里取出模型名。
func e2eGeminiUpstreamModel(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/models/")
	if !ok {
		return "", false
	}
	model, method, ok := strings.Cut(rest, ":")
	if !ok || model == "" {
		return "", false
	}
	switch method {
	case "generateContent", "streamGenerateContent":
		return model, true
	default:
		return "", false
	}
}

// e2eUpstreamKey 取上游请求里的凭据：OpenAI 系用 Authorization，Anthropic 用 x-api-key，
// Gemini 用 x-goog-api-key，查询参数形态回退到 ?key=。
func e2eUpstreamKey(header http.Header, query url.Values) string {
	if auth := header.Get("Authorization"); strings.HasPrefix(auth, access.AuthSchemePrefix) {
		return strings.TrimPrefix(auth, access.AuthSchemePrefix)
	}
	if key := header.Get("x-api-key"); key != "" {
		return key
	}
	if key := header.Get("x-goog-api-key"); key != "" {
		return key
	}
	return query.Get("key")
}

// e2eFakeResponse 构造假上游的归一化应答；用量固定，供账本复算。
func e2eFakeResponse(model string) *domain.Response {
	return &domain.Response{
		Model: model,
		Message: domain.Message{
			Role:  domain.RoleAssistant,
			Parts: []domain.Part{{Kind: domain.PartText, Text: e2eUpstreamText}},
		},
		FinishReason: domain.FinishStop,
		Usage: domain.Usage{
			Source:       domain.UsageSourceUpstream,
			InputTokens:  e2eInputTokens,
			OutputTokens: e2eOutputTokens,
		},
	}
}

// e2eBuildStreamFrames 用适配器把一次应答编码为 SSE 帧序列：
// 开始帧（协议需要时）→ 文本增量 → 携带用量与结束原因的结束帧。
//
// 刻意复用适配器的编码方法而不是手写各协议帧：手写帧与解码器会各自漂移，
// 复用编码器后「假上游说的话」与网关认识的格式同源。
func e2eBuildStreamFrames(adapter domain.Adapter, model string, resp *domain.Response) ([][]byte, error) {
	var frames [][]byte
	start, err := adapter.EncodeStreamStart(model)
	if err != nil {
		return nil, err
	}
	if len(start) > 0 {
		frames = append(frames, start)
	}
	delta, err := adapter.EncodeChunk(domain.Chunk{Kind: domain.ChunkTextDelta, Model: model, TextDelta: e2eUpstreamText})
	if err != nil {
		return nil, err
	}
	if len(delta) > 0 {
		frames = append(frames, delta)
	}
	end, err := adapter.EncodeStreamEnd(domain.Chunk{
		Kind:         domain.ChunkStreamEnd,
		Model:        model,
		Usage:        &resp.Usage,
		FinishReason: resp.FinishReason,
	})
	if err != nil {
		return nil, err
	}
	if len(end) > 0 {
		frames = append(frames, end)
	}
	return frames, nil
}

// e2eJourney 承载一次剧本的共享状态。
type e2eJourney struct {
	ctx         context.Context
	st          *store.Store
	svc         *admin.Service
	upstream    *e2eUpstream
	upstreamURL string

	gatewayURL string
	stopServe  func()

	merchantID      uint64
	accountID       uint64
	mainKey         string
	quotaKey        string
	quotaKeyID      uint64
	quotaID         uint64
	currencyBucket  uint64
	tokenBucket     uint64
	rotateChannelID uint64
	// settledRequests 是剧本已确认落库的流水行数，账本余量由它复算。
	settledRequests int
}

// TestE2EOperatorJourney 是端到端运营剧本的入口。
func TestE2EOperatorJourney(t *testing.T) {
	dsn := os.Getenv(e2eDSNEnv)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过端到端运营剧本", e2eDSNEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	st, err := store.Open(ctx, store.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	// 清理注册顺序（LIFO）：先停服务端、再关假上游、再删表、最后关连接池。
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("关闭连接失败：%v", err)
		}
	})

	// 数据自建自清：开跑前先清空全库，跑完再清一次，连跑两次不带残留。
	e2eDropAllTables(t, st)
	t.Cleanup(func() { e2eDropAllTables(t, st) })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	upstream := newE2EUpstream(t)
	journey := &e2eJourney{
		ctx:         ctx,
		st:          st,
		svc:         admin.New(st),
		upstream:    upstream,
		upstreamURL: upstream.server.URL,
	}

	runStep := func(name string, fn func(*testing.T)) {
		t.Helper()
		if !t.Run(name, fn) {
			t.Fatalf("%s 未通过，后续步骤依赖其状态，剧本终止", name)
		}
	}
	runStep("步骤1_迁移就位与入驻发key充值买包", journey.step1Onboarding)
	runStep("步骤2_启动serve与假上游", journey.step2StartServe)
	t.Cleanup(journey.stopServe)
	runStep("步骤3_四方言流式与非流式", journey.step3Dialects)
	runStep("步骤4_限额拦截与重置", journey.step4Quota)
	runStep("步骤5_凭据轮换", journey.step5CredentialRotation)
	runStep("步骤6_跨协议转换", journey.step6CrossProtocol)
	runStep("步骤7_admin回读对账", journey.step7AdminReadback)
	runStep("步骤8_自助查询端点", journey.step8MeAccount)
}

// e2eDropAllTables 删除当前库的全部表。
//
// 不复用 check-integration 的表名硬清单：清单会随迁移增长，漏一张就让「连跑两次」带上残留。
// 从 information_schema 取全库，清理口径不随新增迁移漂移；DSN 必须指向可丢弃的库。
func e2eDropAllTables(t *testing.T, st *store.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := st.DB().QueryContext(ctx,
		"SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()")
	if err != nil {
		t.Fatalf("查询全库表名失败：%v", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			t.Fatalf("解析表名失败：%v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("遍历表名失败：%v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("关闭表名结果集失败：%v", err)
	}

	for _, name := range names {
		//nolint:gosec // G201：表名取自 information_schema 且用反引号包裹；DDL 标识符无法参数化。
		if _, err := st.DB().ExecContext(ctx, "DROP TABLE IF EXISTS `"+name+"`"); err != nil {
			t.Fatalf("删除表 %s 失败：%v", name, err)
		}
	}
}

// e2eMust 在错误非 nil 时让当前用例失败。
func e2eMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("剧本步骤失败：%v", err)
	}
}

// e2eRequest 构造一次客户端请求体。
func e2eRequest(protocol domain.Protocol, model string, stream bool) string {
	switch protocol {
	case domain.ProtocolOpenAIChat:
		return fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"你好"}],"stream":%t}`, model, stream)
	case domain.ProtocolOpenAIResponses:
		return fmt.Sprintf(`{"model":%q,"input":"你好","stream":%t}`, model, stream)
	case domain.ProtocolAnthropicMessages:
		return fmt.Sprintf(
			`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":"你好"}],"stream":%t}`, model, stream)
	case domain.ProtocolGeminiGenerate:
		// 模型名与流式形态在路径上，请求体里没有这两个字段。
		return `{"contents":[{"role":"user","parts":[{"text":"你好"}]}]}`
	default:
		panic("剧本要求未知协议")
	}
}

// e2eDialectEndpoint 返回某方言在本次调用里使用的客户端端点路径。
// 固定端点方言用注册路径，Gemini 的端点含模型名与流式后缀。
func e2eDialectEndpoint(protocol domain.Protocol, model string, stream bool) string {
	if protocol == domain.ProtocolGeminiGenerate {
		if stream {
			return "/v1beta/models/" + model + ":streamGenerateContent?alt=sse"
		}
		return "/v1beta/models/" + model + ":generateContent"
	}
	return protocol.EndpointPath()
}

// e2eHTTPResult 是一次客户端调用的结果快照。
type e2eHTTPResult struct {
	status      int
	contentType string
	body        []byte
}

// e2ePost 向网关发一次带密钥的 POST 并读完响应体。
func e2ePost(t *testing.T, endpoint, key, body string) e2eHTTPResult {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", access.JSONContentType)
	if key != "" {
		req.Header.Set(access.AuthorizationHeader, access.AuthSchemePrefix+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("发送请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败：%v", err)
	}
	return e2eHTTPResult{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), body: raw}
}

// e2eErrorCode 解析统一错误体里的错误码。
func e2eErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var envelope access.ErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("响应体不是统一错误体：%v，原文 %s", err, body)
	}
	return envelope.Error.Code
}

// e2eDecimalEqual 按数值比较两个十进制文本：数据库会把写入值补到列标度。
func e2eDecimalEqual(got, want string) bool {
	g, err := decimal.NewFromString(got)
	if err != nil {
		return false
	}
	w, err := decimal.NewFromString(want)
	if err != nil {
		return false
	}
	return g.Equal(w)
}

// step1Onboarding 覆盖运营路径第 1 步：迁移就位后的 admin 服务层全链。
func (j *e2eJourney) step1Onboarding(t *testing.T) {
	ctx := j.ctx
	var err error

	// 入驻商家：剧本用 partner 商家而不是平台自营。
	j.merchantID, err = j.svc.CreateMerchant(ctx, "e2e-partner", "E2E 入驻商家", store.MerchantKindPartner)
	e2eMust(t, err)

	// 渠道：四方言各一条，都指向同一个假上游；各自一份凭据与模型映射。
	channels := map[domain.Protocol]uint64{}
	for _, dialect := range journeyDialects {
		group := "e2e-" + dialect.name + "-group"
		channelID, channelErr := j.svc.CreateChannel(ctx, admin.ChannelInput{
			MerchantID: j.merchantID,
			Name:       "e2e-" + dialect.name,
			Vendor:     "fake",
			Type:       store.ChannelType(dialect.protocol),
			CredGroup:  group,
			BaseURL:    j.upstreamURL,
		})
		e2eMust(t, channelErr)
		channels[dialect.protocol] = channelID

		_, err = j.svc.AddCredential(ctx, admin.CredentialInput{
			MerchantID: j.merchantID,
			Group:      group,
			Name:       "primary",
			APIKey:     e2eUpstreamKeyFor(dialect.protocol),
		})
		e2eMust(t, err)

		_, err = j.svc.SetModelMap(ctx, admin.ModelMapInput{
			ChannelID:       channelID,
			Model:           e2eBasicModel,
			UpstreamModel:   e2eBasicUpstreamModel,
			PriceMultiplier: e2eChannelMultiplier,
		})
		e2eMust(t, err)
	}

	// 跨协议：客户端 anthropic 请求只有一条 chat 渠道映射 e2e-cross，
	// 同协议查询为空、跨协议补段命中它。
	_, err = j.svc.SetModelMap(ctx, admin.ModelMapInput{
		ChannelID:       channels[domain.ProtocolOpenAIChat],
		Model:           e2eCrossModel,
		UpstreamModel:   e2eCrossUpstreamModel,
		PriceMultiplier: e2eChannelMultiplier,
	})
	e2eMust(t, err)

	// 凭据轮换演练的专用渠道：同一分组内先坏后好两把 key。
	rotateGroup := "e2e-rotate-group"
	j.rotateChannelID, err = j.svc.CreateChannel(ctx, admin.ChannelInput{
		MerchantID: j.merchantID,
		Name:       "e2e-rotate",
		Vendor:     "fake",
		Type:       store.ChannelTypeOpenAIChat,
		CredGroup:  rotateGroup,
		BaseURL:    j.upstreamURL,
	})
	e2eMust(t, err)
	_, err = j.svc.AddCredential(ctx, admin.CredentialInput{
		MerchantID: j.merchantID, Group: rotateGroup, Name: "old", APIKey: e2eRejectedKey,
	})
	e2eMust(t, err)
	_, err = j.svc.AddCredential(ctx, admin.CredentialInput{
		MerchantID: j.merchantID, Group: rotateGroup, Name: "new", APIKey: e2eRotateOKKey,
	})
	e2eMust(t, err)
	_, err = j.svc.SetModelMap(ctx, admin.ModelMapInput{
		ChannelID:       j.rotateChannelID,
		Model:           e2eRotateModel,
		UpstreamModel:   e2eRotateUpstreamModel,
		PriceMultiplier: e2eChannelMultiplier,
	})
	e2eMust(t, err)

	// 定价发布：按上游模型名发布，三个模型的定价口径完全一致。
	// 生效时刻显式回拨一分钟：effective_at 列是秒精度 DATETIME，写入带小数的当前时刻会被
	// 舍入到下一秒，紧随其后的结算就会暂时查不到生效定价。回拨后定价已确定早于本次调用。
	effectiveAt := time.Now().Add(-time.Minute)
	for _, model := range []string{e2eBasicUpstreamModel, e2eCrossUpstreamModel, e2eRotateUpstreamModel} {
		_, err = j.svc.PublishPricing(ctx, admin.PublishPricingInput{
			MerchantID:  j.merchantID,
			Model:       model,
			EffectiveAt: effectiveAt,
			Components:  e2ePricingComponents(),
		})
		e2eMust(t, err)
	}

	// 开户：默认商家指向入驻商家，使密钥鉴权解析出的商家与渠道归属一致。
	j.accountID, err = j.svc.CreateAccount(ctx, admin.AccountInput{
		Code:              "e2e-account",
		Name:              "E2E 账户",
		DefaultMerchantID: &j.merchantID,
	})
	e2eMust(t, err)

	// 发 key：主密钥用于第 3、5、6 步；配额演练另发一把，避免污染主密钥的用量口径。
	issued, err := j.svc.IssueKey(ctx, admin.IssueKeyInput{
		AccountID: j.accountID, MerchantID: &j.merchantID, Name: "e2e-main",
	})
	e2eMust(t, err)
	j.mainKey = issued.Plaintext

	quotaIssued, err := j.svc.IssueKey(ctx, admin.IssueKeyInput{
		AccountID: j.accountID, MerchantID: &j.merchantID, Name: "e2e-quota",
	})
	e2eMust(t, err)
	j.quotaKey = quotaIssued.Plaintext
	j.quotaKeyID = quotaIssued.ID

	// 充值：一笔可透支的货币账本，兜住 402 预检并作为后付兜底。
	j.currencyBucket, err = j.svc.CreditBucket(ctx, admin.CreditBucketInput{
		AccountID:  j.accountID,
		MerchantID: j.merchantID,
		Unit:       billing.UnitSettleCurrency,
		Amount:     e2eRechargeAmount,
		Fallback:   billing.FallbackChargeBalance,
		Source:     billing.SourceRecharge,
	})
	e2eMust(t, err)

	// 购买 token 包：商品派生出 token 账本，是本次扣费的落点。
	productID, err := j.svc.CreateProduct(ctx, admin.ProductInput{
		MerchantID:   j.merchantID,
		Name:         "e2e-token-package",
		Unit:         billing.UnitSettleToken,
		Qty:          e2eTokenPackageQty,
		Price:        "10",
		ValidityDays: 30,
	})
	e2eMust(t, err)
	purchase, err := j.svc.Buy(ctx, admin.BuyInput{
		AccountID: j.accountID,
		ProductID: productID,
		Qty:       "1",
		Fallback:  billing.FallbackChargeBalance,
	})
	e2eMust(t, err)
	j.tokenBucket = purchase.BucketID
}

// e2ePricingComponents 是剧本共用的定价分量：结算单位是 token，input 1/个、output 2/个。
func e2ePricingComponents() []store.PriceComponent {
	return []store.PriceComponent{
		{
			Metric:     billing.MetricInputToken,
			UnitSettle: billing.UnitSettleToken,
			UnitPrice:  e2eInputUnitPrice,
			BasisQty:   "1",
		},
		{
			Metric:     billing.MetricOutputToken,
			UnitSettle: billing.UnitSettleToken,
			UnitPrice:  e2eOutputUnitPrice,
			BasisQty:   "1",
		},
	}
}

// e2eUpstreamKeyFor 返回某方言渠道在剧本里使用的上游凭据。
func e2eUpstreamKeyFor(protocol domain.Protocol) string {
	switch protocol {
	case domain.ProtocolOpenAIChat:
		return e2eChatKey
	case domain.ProtocolOpenAIResponses:
		return e2eResponsesKey
	case domain.ProtocolGeminiGenerate:
		return e2eGeminiKey
	default:
		return e2eMessagesKey
	}
}

// step2StartServe 覆盖运营路径第 2 步：用生产装配函数起真实监听。
//
// 复用 cmdServe 内部的 newGateway 与 runServer，而不是另写一份装配：
// 剧本要验证的是生产装配路径。端口取 127.0.0.1:0 由内核分配，不依赖固定端口。
func (j *e2eJourney) step2StartServe(t *testing.T) {
	gw, err := gateway.New(j.st, gateway.Options{CompleteTimeout: 10 * time.Second})
	e2eMust(t, err)

	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		gw.Close()
		t.Fatalf("监听临时端口失败：%v", err)
	}
	server := &http.Server{Handler: gw.Handler(), ReadHeaderTimeout: gateway.ReadHeaderTimeout}
	serveCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- gateway.Run(serveCtx, server, ln) }()

	j.gatewayURL = "http://" + ln.Addr().String()
	j.stopServe = func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("关闭服务端失败：%v", err)
		}
		gw.Close()
	}
	e2eWaitForOK(t, j.gatewayURL+gateway.HealthzPath)
}

// e2eWaitForOK 轮询健康检查直到 200 或超时。
func e2eWaitForOK(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("构造健康检查请求失败：%v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 %s 就绪超时", url)
}

// step3Dialects 覆盖运营路径第 3 步：四方言 × 流式/非流式，
// 断言 200、流水落库、结算五字段非占位、账本按定价下降可复算。
func (j *e2eJourney) step3Dialects(t *testing.T) {
	for _, dialect := range journeyDialects {
		// 记录调用前的计数，调用后断言模型名被替换成上游模型名、凭据按该方言注入。
		modelBefore := j.upstream.modelHit(e2eBasicUpstreamModel)
		keyBefore := j.upstream.keyHit(e2eUpstreamKeyFor(dialect.protocol))
		for _, stream := range []bool{false, true} {
			endpoint := j.gatewayURL + e2eDialectEndpoint(dialect.protocol, e2eBasicModel, stream)
			result := e2ePost(t, endpoint, j.mainKey, e2eRequest(dialect.protocol, e2eBasicModel, stream))
			if result.status != http.StatusOK {
				t.Fatalf("方言 %s stream=%t 状态码 = %d，期望 200，响应体 %s",
					dialect.name, stream, result.status, result.body)
			}
			if len(result.body) == 0 {
				t.Fatalf("方言 %s stream=%t 响应体为空", dialect.name, stream)
			}
			if !strings.Contains(string(result.body), e2eUpstreamText) {
				t.Fatalf("方言 %s stream=%t 响应体不含假上游正文：%s", dialect.name, stream, result.body)
			}
		}
		if got := j.upstream.modelHit(e2eBasicUpstreamModel) - modelBefore; got != 2 {
			t.Fatalf("方言 %s 上游收到上游模型名 %q %d 次，期望 2（流式与非流式各一次）",
				dialect.name, e2eBasicUpstreamModel, got)
		}
		if got := j.upstream.keyHit(e2eUpstreamKeyFor(dialect.protocol)) - keyBefore; got != 2 {
			t.Fatalf("方言 %s 上游收到凭据 %q %d 次，期望 2",
				dialect.name, e2eUpstreamKeyFor(dialect.protocol), got)
		}
	}

	rows := j.loadUsageRows(t)
	want := len(journeyDialects) * 2
	if len(rows) != want {
		t.Fatalf("流水行数 = %d，期望 %d（四方言 × 流式/非流式）", len(rows), want)
	}
	for i, row := range rows {
		e2eAssertSettledRow(t, row, i)
	}
	j.settledRequests = want
	j.assertTokenLedger(t, want)
}

// e2eUsageRow 是直接查库得到的结算字段视图。
//
// 管理面的 ListUsage 不返回 pricing_id 与 pricing_snapshot，故「结算五字段非占位」
// 这一步用直接查询；第 7 步的管理面回读是另一层验证。
type e2eUsageRow struct {
	apiKeyID        uint64
	channelID       uint64
	pricingID       uint64
	pricingSnapshot []byte
	grossAmount     string
	multiplier      string
	settlement      []byte
	usage           []byte
}

// loadUsageRows 读出本账户的全部流水结算字段。
func (j *e2eJourney) loadUsageRows(t *testing.T) []e2eUsageRow {
	t.Helper()
	rows, err := j.st.DB().QueryContext(j.ctx,
		"SELECT api_key_id, channel_id, pricing_id, pricing_snapshot, gross_amount, multiplier, settlement, `usage` "+
			"FROM billing_usage WHERE account_id = ? ORDER BY id", j.accountID)
	if err != nil {
		t.Fatalf("查询 billing_usage 失败：%v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []e2eUsageRow
	for rows.Next() {
		var row e2eUsageRow
		if err := rows.Scan(&row.apiKeyID, &row.channelID, &row.pricingID, &row.pricingSnapshot,
			&row.grossAmount, &row.multiplier, &row.settlement, &row.usage); err != nil {
			t.Fatalf("解析 billing_usage 行失败：%v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 billing_usage 行失败：%v", err)
	}
	return out
}

// e2eAssertSettledRow 断言一行流水的结算五字段非占位，且用量与定价口径可复算。
func e2eAssertSettledRow(t *testing.T, row e2eUsageRow, index int) {
	t.Helper()
	if row.pricingID == 0 {
		t.Errorf("第 %d 行 pricing_id 为占位 0", index)
	}
	if len(row.pricingSnapshot) == 0 {
		t.Errorf("第 %d 行 pricing_snapshot 为空", index)
	}
	if !e2eDecimalEqual(row.grossAmount, e2eGrossAmount) {
		t.Errorf("第 %d 行 gross_amount = %s，期望 %s", index, row.grossAmount, e2eGrossAmount)
	}
	if !e2eDecimalEqual(row.multiplier, e2eMultiplier) {
		t.Errorf("第 %d 行 multiplier = %s，期望 %s", index, row.multiplier, e2eMultiplier)
	}
	if len(row.settlement) == 0 {
		t.Errorf("第 %d 行 settlement 为空", index)
	}
	var metrics map[string]int
	if err := json.Unmarshal(row.usage, &metrics); err != nil {
		t.Errorf("第 %d 行 usage 不是 JSON：%v，原文 %s", index, err, row.usage)
		return
	}
	if got := metrics[string(billing.MetricInputToken)]; got != e2eInputTokens {
		t.Errorf("第 %d 行 input_token = %d，期望 %d", index, got, e2eInputTokens)
	}
	if got := metrics[string(billing.MetricOutputToken)]; got != e2eOutputTokens {
		t.Errorf("第 %d 行 output_token = %d，期望 %d", index, got, e2eOutputTokens)
	}
	if got := metrics[string(billing.MetricRequest)]; got != 1 {
		t.Errorf("第 %d 行 request = %d，期望 1（一次请求一行）", index, got)
	}
}

// assertTokenLedger 断言 token 包账本按「单次 39 token × 流水行数」下降。
func (j *e2eJourney) assertTokenLedger(t *testing.T, settled int) {
	t.Helper()
	var remaining string
	err := j.st.DB().QueryRowContext(j.ctx,
		"SELECT remaining FROM account_bucket WHERE id = ?", j.tokenBucket).Scan(&remaining)
	if err != nil {
		t.Fatalf("查询 token 账本失败：%v", err)
	}
	start, err := decimal.NewFromString(e2eTokenPackageQty)
	e2eMust(t, err)
	perRequest, err := decimal.NewFromString(e2eTokensPerRequest)
	e2eMust(t, err)
	want := start.Sub(perRequest.Mul(decimal.NewFromInt(int64(settled))))
	if !e2eDecimalEqual(remaining, want.String()) {
		t.Fatalf("token 账本余量 = %s，期望 %s（%d 次结算 × %s）",
			remaining, want.String(), settled, e2eTokensPerRequest)
	}
}

// step4Quota 覆盖运营路径第 4 步：限额演练。
//
// 两段演练都用 key 维度限额，避免污染主密钥的用量口径：
//
//   - input_token：第一次放行、第二次 429、reset 后放行。限额是「达到即拦截」，
//     所以 limit 取单次输入 token 数时第一次 used=0 放行、第二次 used=12 拦截。
//   - request：另一把密钥上按请求次数设限，前 3 次放行、第 4 次 429。request 分量由
//     落库路径生成（一次请求一行），这段同时验证 usage JSON 逐行写了 request=1。
func (j *e2eJourney) step4Quota(t *testing.T) {
	ctx := j.ctx
	var err error
	j.quotaID, err = j.svc.CreateQuota(ctx, admin.QuotaInput{
		Scope:       billing.ScopeAPIKey,
		ScopeID:     j.quotaKeyID,
		Metric:      billing.MetricInputToken,
		WindowKind:  billing.WindowKindCalendar,
		Period:      billing.PeriodDay,
		LimitAmount: e2eQuotaLimit,
		Action:      billing.ActionReject,
	})
	e2eMust(t, err)

	endpoint := j.gatewayURL + domain.ProtocolOpenAIChat.EndpointPath()
	request := e2eRequest(domain.ProtocolOpenAIChat, e2eBasicModel, false)

	first := e2ePost(t, endpoint, j.quotaKey, request)
	if first.status != http.StatusOK {
		t.Fatalf("限额内首次请求状态码 = %d，期望 200，响应体 %s", first.status, first.body)
	}

	blocked := e2ePost(t, endpoint, j.quotaKey, request)
	if blocked.status != http.StatusTooManyRequests {
		t.Fatalf("超限请求状态码 = %d，期望 429，响应体 %s", blocked.status, blocked.body)
	}
	if code := e2eErrorCode(t, blocked.body); code != string(domain.CodeQuotaExceeded) {
		t.Fatalf("超限错误码 = %q，期望 %q", code, domain.CodeQuotaExceeded)
	}

	// 重置窗口基准：追加一条重置事件，已用量聚合下界推到当前时刻。
	if _, err := j.svc.ResetQuota(ctx, admin.ResetQuotaInput{
		QuotaID:  j.quotaID,
		Reason:   "E2E 剧本重置",
		Operator: "e2e",
	}); err != nil {
		t.Fatalf("重置限额失败：%v", err)
	}

	allowed := e2ePost(t, endpoint, j.quotaKey, request)
	if allowed.status != http.StatusOK {
		t.Fatalf("重置后状态码 = %d，期望 200，响应体 %s", allowed.status, allowed.body)
	}

	// 配额密钥放行 2 次（被拒的那次不落流水），账本按累计行数复算。
	j.settledRequests += 2
	j.assertTokenLedger(t, j.settledRequests)

	// 第二段：request 计数限额。另发一把密钥，使 request 聚合从 0 起算，
	// 不与上面 input_token 限额留下的流水混在一起。
	requestKey, err := j.svc.IssueKey(ctx, admin.IssueKeyInput{
		AccountID: j.accountID, MerchantID: &j.merchantID, Name: "e2e-quota-request",
	})
	e2eMust(t, err)
	_, err = j.svc.CreateQuota(ctx, admin.QuotaInput{
		Scope:       billing.ScopeAPIKey,
		ScopeID:     requestKey.ID,
		Metric:      billing.MetricRequest,
		WindowKind:  billing.WindowKindCalendar,
		Period:      billing.PeriodDay,
		LimitAmount: e2eRequestQuotaLimit,
		Action:      billing.ActionReject,
	})
	e2eMust(t, err)

	// 前 3 次已用量分别为 0/1/2，放行；第 4 次 used=3，达到限额即拦截。
	for i := 1; i <= 3; i++ {
		allowed := e2ePost(t, endpoint, requestKey.Plaintext, request)
		if allowed.status != http.StatusOK {
			t.Fatalf("request 限额内第 %d 次请求状态码 = %d，期望 200，响应体 %s", i, allowed.status, allowed.body)
		}
	}
	fourth := e2ePost(t, endpoint, requestKey.Plaintext, request)
	if fourth.status != http.StatusTooManyRequests {
		t.Fatalf("request 限额第 4 次请求状态码 = %d，期望 429，响应体 %s", fourth.status, fourth.body)
	}
	if code := e2eErrorCode(t, fourth.body); code != string(domain.CodeQuotaExceeded) {
		t.Fatalf("request 超限错误码 = %q，期望 %q", code, domain.CodeQuotaExceeded)
	}

	// 被拒的那次不落流水，只有放行的 3 次计入账本。
	j.settledRequests += 3
	j.assertTokenLedger(t, j.settledRequests)
}

// step5CredentialRotation 覆盖运营路径第 5 步：第一把 key 401、第二把成功、流水仅一行。
func (j *e2eJourney) step5CredentialRotation(t *testing.T) {
	before := len(j.loadUsageRows(t))
	result := e2ePost(t, j.gatewayURL+domain.ProtocolOpenAIChat.EndpointPath(), j.mainKey,
		e2eRequest(domain.ProtocolOpenAIChat, e2eRotateModel, false))
	if result.status != http.StatusOK {
		t.Fatalf("轮换后状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	if hits := j.upstream.keyHit(e2eRejectedKey); hits != 1 {
		t.Fatalf("被拒凭据被上游收到 %d 次，期望 1", hits)
	}
	if hits := j.upstream.keyHit(e2eRotateOKKey); hits != 1 {
		t.Fatalf("替补凭据被上游收到 %d 次，期望 1", hits)
	}

	rows := j.loadUsageRows(t)
	if len(rows) != before+1 {
		t.Fatalf("轮换演练新增流水 %d 行，期望 1（被 401 拒绝的那次不落流水）", len(rows)-before)
	}
	j.settledRequests = before + 1
	j.assertTokenLedger(t, j.settledRequests)
}

// step6CrossProtocol 覆盖运营路径第 6 步：客户端 anthropic、渠道 openai_chat 时成功转换。
func (j *e2eJourney) step6CrossProtocol(t *testing.T) {
	result := e2ePost(t, j.gatewayURL+domain.ProtocolAnthropicMessages.EndpointPath(), j.mainKey,
		e2eRequest(domain.ProtocolAnthropicMessages, e2eCrossModel, false))
	if result.status != http.StatusOK {
		t.Fatalf("跨协议状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	// 客户端拿到的是 Anthropic Messages 形态（网关按客户端方言重建），
	// 而上游实际被打到的是 chat 端点。
	if !strings.Contains(string(result.body), `"type":"message"`) {
		t.Fatalf("跨协议响应不是 Anthropic Messages 形态：%s", result.body)
	}
	if hits := j.upstream.pathHit(domain.ProtocolOpenAIChat.EndpointSegment()); hits == 0 {
		t.Fatalf("跨协议请求未落到 chat 上游端点 %s", domain.ProtocolOpenAIChat.EndpointSegment())
	}
	j.settledRequests++
	j.assertTokenLedger(t, j.settledRequests)
}

// step7AdminReadback 覆盖运营路径第 7 步：管理面回读与剧本断言一致。
func (j *e2eJourney) step7AdminReadback(t *testing.T) {
	ctx := j.ctx

	usages, err := j.svc.ListUsage(ctx, j.accountID, time.Time{})
	e2eMust(t, err)
	if len(usages) != j.settledRequests {
		t.Fatalf("admin usage list 行数 = %d，期望 %d", len(usages), j.settledRequests)
	}
	for i, row := range usages {
		if !e2eDecimalEqual(row.GrossAmount, e2eGrossAmount) {
			t.Errorf("第 %d 行 gross_amount = %s，期望 %s", i, row.GrossAmount, e2eGrossAmount)
		}
		if !e2eDecimalEqual(row.Multiplier, e2eMultiplier) {
			t.Errorf("第 %d 行 multiplier = %s，期望 %s", i, row.Multiplier, e2eMultiplier)
		}
		if len(row.Settlement) == 0 {
			t.Errorf("第 %d 行 settlement 为空", i)
		}
	}

	buckets, err := j.svc.ListBuckets(ctx, j.accountID)
	e2eMust(t, err)
	token, ok := e2eFindBucket(buckets, j.tokenBucket)
	if !ok {
		t.Fatalf("bucket list 里找不到 token 包 %d", j.tokenBucket)
	}
	start, err := decimal.NewFromString(e2eTokenPackageQty)
	e2eMust(t, err)
	perRequest, err := decimal.NewFromString(e2eTokensPerRequest)
	e2eMust(t, err)
	want := start.Sub(perRequest.Mul(decimal.NewFromInt(int64(j.settledRequests))))
	if !e2eDecimalEqual(token.Remaining, want.String()) {
		t.Fatalf("bucket list 的 token 余量 = %s，期望 %s", token.Remaining, want.String())
	}

	currency, ok := e2eFindBucket(buckets, j.currencyBucket)
	if !ok {
		t.Fatalf("bucket list 里找不到充值账本 %d", j.currencyBucket)
	}
	if !e2eDecimalEqual(currency.Remaining, e2eRechargeAmount) {
		t.Fatalf("充值账本余量 = %s，期望 %s（本次扣费落在 token 单位，货币账本不应被扣）",
			currency.Remaining, e2eRechargeAmount)
	}
}

// e2eFindBucket 按 id 在账本列表里找一行。
func e2eFindBucket(buckets []store.BucketRow, id uint64) (store.BucketRow, bool) {
	for _, bucket := range buckets {
		if bucket.ID == id {
			return bucket, true
		}
	}
	return store.BucketRow{}, false
}
