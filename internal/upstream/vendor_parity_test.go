package upstream

import (
	"context"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/failure"
)

// 本文件做的是**语义等价组的差分测试**：同一件事实（上游在说「你太快了」「你的额度没了」
// 「这把 key 不认」）在不同上游下有不同表达，而网关对它们的处置必须一致。
//
// 与 mockupstream_test.go 的区别很重要：那边逐类断言的是「这份报文被归成什么」，报文是我
// 照着分类器的词表挑的，等于用实现自己的词汇去测实现。这里从**语义**出发，措辞取自真实
// 厂商的写法（含非标准状态码、中文措辞、结构化字段的变体），因此能测出词表覆盖不到的地方。
//
// 断言的是**处置**（动作集 + 停用时长）而不是类别名：类别只是诊断标签，同一个动作集的
// 两个类别对客户端与渠道健康度没有差别。类别名的不一致单独记录、不判失败。

// vendorVariant 是某厂商对某件事实的一种表达。
type vendorVariant struct {
	name   string
	status int
	body   string
	// frame 非空时按流式错误帧下发（状态码为 200）。
	frame string
}

// semanticGroup 是「同一件事实」的一组厂商表达。
type semanticGroup struct {
	meaning  string
	variants []vendorVariant
}

// observed 是一次分类观察的结果。
type observed struct {
	class   failure.Class
	actions failure.Action
	suspend int64
	retry   bool
}

// observe 把一种厂商表达跑成一条上游错误并读出分类与处置。
func observe(t *testing.T, variant vendorVariant) observed {
	t.Helper()
	var script mockScript
	protocol := domain.ProtocolOpenAIChat
	if variant.frame != "" {
		script = mockScript{status: 200, sse: []string{variant.frame}}
	} else {
		script = mockScript{status: variant.status, body: variant.body}
	}
	upstream := newMockUpstream(t, script)
	client := newMockClient(t, Options{})
	route := mockRoute(protocol, upstream.URL())

	var err error
	if variant.frame != "" {
		err = client.Stream(context.Background(), route, &domain.Request{Model: "mock-model"},
			[]byte(`{"model":"mock-model","stream":true}`), &collectSink{})
	} else {
		_, err = client.Complete(context.Background(), route, &domain.Request{Model: "mock-model"}, []byte(`{}`))
	}
	if err == nil {
		t.Fatalf("%s：期望失败，实际成功", variant.name)
	}
	return observed{
		class:   failure.ClassOf(err),
		actions: failure.ActionsOf(err),
		suspend: int64(failure.SuspendFor(err)),
		retry:   failure.ActionsOf(err).Retryable(),
	}
}

// vendorSemantics 是真实厂商对同一件事实的多种写法。
//
// 措辞刻意不照抄分类器词表：带非标准状态码（529）、中文措辞、厂商特有的结构化类型名后缀
// （overloaded_error）、以及不带任何关键词的服务不可用报文。
var vendorSemantics = []semanticGroup{
	{
		meaning: "限流或过载（渠道还活着，等一会儿就好）",
		variants: []vendorVariant{
			{name: "openai 429 标准措辞", status: 429, body: `{"error":{"message":"Rate limit reached for requests","type":"requests"}}`},
			{name: "deepseek 429 另一种措辞", status: 429, body: `{"error":{"message":"Your account is rate limited. Please slow down."}}`},
			{name: "anthropic 529 过载", status: 529, body: `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
			{name: "cloudflare 风格 529 无关键词", status: 529, body: `{"error":{"message":"The origin web server is busy, try again shortly"}}`},
			{name: "中文厂商 429 并发超限", status: 429, body: `{"error":{"message":"当前并发数过高，已触发限流"}}`},
			{name: "gemini 429 资源耗尽", status: 429, body: `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted"}}`},
			// 529 是非标准状态码，但 Cloudflare 已把它的含义定为「站点过载」：
			// 同一语义的无关键词报文也算忙，而不是渠道故障。
			{name: "cloudflare 529 而无关键词", status: 529, body: `{}`},
			{name: "另一家 529 无关键词", status: 529, body: `{"error":{"message":"The origin web server is busy, try again shortly"}}`},
			{name: "流式首帧报过载（结构化类型带后缀）", frame: `{"error":{"type":"overloaded_error","message":"Overloaded"}}`},
			{name: "流式首帧报限流（机器码）", frame: `{"error":{"code":"rate_limit_exceeded"}}`},
		},
	},
	{
		// 与「余额或欠费」刻意分开：额度等窗口滚动，余额等充值，停用时长不同（15 分钟对 30 分钟）。
		meaning: "套餐或窗口额度用尽（等窗口滚动）",
		variants: []vendorVariant{
			{name: "openai 429 insufficient_quota", status: 429, body: `{"error":{"type":"insufficient_quota","message":"You exceeded your current quota"}}`},
			{name: "中文厂商 400 套餐用尽", status: 400, body: `{"error":{"message":"当前套餐配额已用尽，请升级套餐"}}`},
			{name: "另一家 400 周限额", status: 400, body: `{"error":{"message":"You have hit your weekly limit. limit reached|1791291355"}}`},
			{name: "流式首帧 insufficient_quota", frame: `{"error":{"type":"insufficient_quota","message":"quota"}}`},
			// 同一个机器码，只因报文里提到 billing（厂商在让你去付钱）就归余额、停更久。
			// 这是有意的启发式，也是本清单里唯一保留的处置差异，理由见下。
			{name: "同一机器码但报文提到账单", status: 429, body: `{"error":{"type":"insufficient_quota","message":"You exceeded your current quota, please check your plan and billing details."}}`},
		},
	},
	{
		meaning: "余额或欠费（等充值）",
		variants: []vendorVariant{
			{name: "commandcode 400 insufficient credits", status: 400, body: `{"error":{"message":"You have insufficient credits to make this request."}}`},
			{name: "另一家 403 欠费停服", status: 403, body: `{"error":{"message":"Your account is overdue and has been suspended."}}`},
			{name: "中文厂商 403 账号停用", status: 403, body: `{"error":{"message":"您的账号已被停用，请联系管理员"}}`},
			{name: "中文厂商 400 余额不足", status: 400, body: `{"error":{"message":"余额不足，请充值后再试"}}`},
			{name: "流式首帧 insufficient_credits", frame: `{"error":{"type":"insufficient_credits","message":"no credits"}}`},
		},
	},
	{
		meaning: "凭据不被接受（这把 key 不能用）",
		variants: []vendorVariant{
			{name: "401 空体", status: 401},
			{name: "403 permission_error", status: 403, body: `{"error":{"type":"permission_error"}}`},
			{name: "400 invalid_api_key", status: 400, body: `{"error":{"code":"invalid_api_key"}}`},
			{name: "400 自然语言 API key not valid", status: 400, body: `{"error":{"message":"API key not valid. Please pass a valid API key."}}`},
			{name: "流式首帧 authentication_error", frame: `{"error":{"type":"authentication_error","message":"bad key"}}`},
		},
	},
	{
		meaning: "请求本身有问题（换 key 换渠道都没用）",
		variants: []vendorVariant{
			{name: "400 invalid_request_error", status: 400, body: `{"error":{"type":"invalid_request_error","message":"messages: field required"}}`},
			{name: "400 结构化类型但没有关键词", status: 400, body: `{"error":{"type":"missing_required_parameter"}}`},
			{name: "404 资源不存在", status: 404, body: `{"error":{"type":"not_found_error"}}`},
			{name: "流式首帧 invalid_request_error", frame: `{"error":{"type":"invalid_request_error"}}`},
			{name: "流式首帧 not_found_error", frame: `{"error":{"code":"not_found_error"}}`},
		},
	},
	{
		meaning: "上游自身故障（换渠道可能好，且该计入渠道健康度）",
		variants: []vendorVariant{
			{name: "500 空体", status: 500},
			{name: "502 网关坏", status: 502, body: `{"error":{"message":"bad gateway"}}`},
			{name: "503 服务不可用（无关键词）", status: 503, body: `{"error":{"message":"Service Unavailable"}}`},
			{name: "520 云厂商专有码", status: 520, body: `{}`},
			{name: "流式首帧认不出的错误", frame: `{"error":{"type":"brand_new_failure_mode"}}`},
		},
	},
}

// TestVendorSemanticParity 断言同一语义的多种厂商表达得到同一处置。
//
// 这条测试的价值在于它**不由实现驱动**：措辞来自厂商而不是分类器的词表，因此每一条失败
// 都是词表覆盖不到的地方。类别名的不一致只记录不判失败 —— 处置一致就没有行为差别。
func TestVendorSemanticParity(t *testing.T) {
	// knownDivergence 记录已知且已知原因的处置差异；出现新差异或已知差异消失都会失败，
	// 使这份清单只能被收窄、不会被悄悄扩大。
	knownDivergence := map[string]string{
		"套餐或窗口额度用尽（等窗口滚动）/同一机器码但报文提到账单": "两类都会停用并换凭据，差别只在停用时长（额度 15 分钟对余额 30 分钟）。" +
			"报文提到 billing/plan 时厂商在让你去付钱，按余额停更贴近事实。",
	}

	for _, group := range vendorSemantics {
		t.Run(group.meaning, func(t *testing.T) {
			// 以第一个变体为基准，比较其余变体的处置。
			base := observe(t, group.variants[0])
			for _, variant := range group.variants[1:] {
				got := observe(t, variant)
				key := group.meaning + "/" + variant.name
				reason, known := knownDivergence[key]
				diverges := got.actions != base.actions || got.suspend != base.suspend
				switch {
				case diverges && !known:
					t.Errorf("处置与基准（%s：%s）不一致：%s 得到 %s（停用 %v）\n  —— 这是词表覆盖不到的一种表达",
						group.variants[0].name, base.actions, variant.name, got.actions, got.suspend)
				case !diverges && known:
					t.Errorf("%s 的处置已与基准一致（%s），请把它从已知差异清单里移除（原记录的理由：%s）",
						variant.name, got.actions, reason)
				case diverges:
					t.Logf("已知差异：%s 得到 %s；理由：%s", variant.name, got.actions, reason)
				}
				if got.class != base.class {
					t.Logf("类别名不同（处置一致时不影响行为）：%s = %s，基准 = %s",
						variant.name, failure.ClassName(got.class), failure.ClassName(base.class))
				}
			}
		})
	}
}

// TestAmbiguous5xxIsCountedAsUpstreamFault 固定住「无关键词的 5xx」这一取舍。
//
// 503 既可能是上游真的坏了（该计入渠道健康度），也可能只是暂时忙（不该计入）。
// 只看状态码区分不了，而报文没给线索。定这一侧的理由：把「渠道不稳定」漏记的代价是
// 持续把流量打到坏渠道，而误记的代价只是多熔断一条其实还能用的渠道——前者更难发现，
// 因为它表现为偶发失败而不是明确错误。
//
// 529 是例外，且这个例外有定义依据而不是猜测：Cloudflare 把 529 定义为「站点过载」，
// 按定义就是忙，因此不进熔断计数。
func TestAmbiguous5xxIsCountedAsUpstreamFault(t *testing.T) {
	for _, variant := range []vendorVariant{
		{name: "503", status: 503, body: `{"error":{"message":"Service Unavailable"}}`},
		{name: "520", status: 520, body: `{}`},
	} {
		got := observe(t, variant)
		if got.class != failure.ClassUpstream {
			t.Errorf("%s 的类别 = %s，期望 upstream", variant.name, failure.ClassName(got.class))
		}
		if !got.actions.Has(failure.ActionCountBreaker) {
			t.Errorf("%s 未计入渠道健康度：%s", variant.name, got.actions)
		}
		if !got.retry {
			t.Errorf("%s 应允许换渠道重试：%s", variant.name, got.actions)
		}
	}

	// 529 按定义归「忙」：可换渠道但不计入健康度。
	overloaded := observe(t, vendorVariant{name: "529", status: 529, body: `{}`})
	if overloaded.class != failure.ClassRateLimit {
		t.Errorf("529 的类别 = %s，期望 rate_limit（Cloudflare 定义为站点过载）", failure.ClassName(overloaded.class))
	}
	if overloaded.actions.Has(failure.ActionCountBreaker) {
		t.Errorf("529 不该计入渠道健康度：%s", overloaded.actions)
	}
}

// TestStreamErrorFrameWithoutCluesCountsAsUpstreamFault 记录流式路径无处可退的一处分寸。
//
// 非 2xx 上「报文没线索」还有状态码可兜：429 就是限流。而流式帧的状态码恒为 200
// （走到这里就说明上游已经进了正常响应），认不出就只能归上游故障。
//
// 定这一侧的理由：流中途挂掉而看不出原因，对转发而言与渠道不稳定无法区分；
// 不计入健康度会让一条持续在流中途断开的渠道永远不被熔断。
func TestStreamErrorFrameWithoutCluesCountsAsUpstreamFault(t *testing.T) {
	frame := observe(t, vendorVariant{name: "无线索错误帧", frame: `{"error":{}}`})
	if frame.class != failure.ClassUpstream {
		t.Errorf("无线索的流式错误帧类别 = %s，期望 upstream", failure.ClassName(frame.class))
	}
	if !frame.actions.Has(failure.ActionCountBreaker) {
		t.Errorf("无线索的流式错误帧未计入渠道健康度：%s", frame.actions)
	}

	// 非 2xx 上的同一件事有状态码可依：不归上游故障。
	// 报文选一个任何规则都跑不中的串，确保测的是状态码分支。
	response := observe(t, vendorVariant{name: "429 无报文", status: 429, body: `{}`})
	if response.class != failure.ClassRateLimit {
		t.Errorf("429 无报文的类别 = %s，期望 rate_limit", failure.ClassName(response.class))
	}
	if response.actions.Has(failure.ActionCountBreaker) {
		t.Errorf("429 不该计入渠道健康度：%s", response.actions)
	}
}

// TestStructuredCodeVariantsMatchWordingTable 守住「结构化取值也走词表」。
//
// 结构化取值常是词表词汇的变体（overloaded_error 对 overloaded、rate_limit_error 对
// rate limit）。只认精确取值会让这些变体退化成「认不出」，从而把一件「渠道还活着」的
// 事记成渠道故障并计入健康度。
//
// 本用例替换掉原先记录该口径差的用例：差异修掉后，记录性用例应当失效并被删除。
func TestStructuredCodeVariantsMatchWordingTable(t *testing.T) {
	cases := []struct {
		frame string
		want  failure.Class
	}{
		{`{"error":{"type":"overloaded"}}`, failure.ClassRateLimit},
		{`{"error":{"type":"overloaded_error"}}`, failure.ClassRateLimit},
		{`{"error":{"type":"Overloaded"}}`, failure.ClassRateLimit},
		{`{"error":{"type":"rate_limit_error"}}`, failure.ClassRateLimit},
		{`{"error":{"type":"server_overloaded"}}`, failure.ClassRateLimit},
		{`{"error":{"type":"context_length_exceeded"}}`, failure.ClassContextLength},
		{`{"error":{"type":"model_not_found"}}`, failure.ClassModelUnavailable},
		{`{"error":{"type":"authentication_error"}}`, failure.ClassAuth},
		// 认不出的取值仍按错误信封兜底为上游故障。
		{`{"error":{"type":"brand_new_failure_mode"}}`, failure.ClassUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.frame, func(t *testing.T) {
			got := observe(t, vendorVariant{name: tc.frame, frame: tc.frame})
			if got.class != tc.want {
				t.Fatalf("类别 = %s，期望 %s", failure.ClassName(got.class), failure.ClassName(tc.want))
			}
		})
	}
}
