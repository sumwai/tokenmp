package failure

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
)

// 本文件用手挑之外的输入验证不变量：**生成**报文（带拼错、乱码、截断、超长、只有标点），
// 以及用 fuzz 引擎去探索手写生成器想不到的形状。
//
// 与 mockupstream_test.go 的分工：那边断言「这份报文被归成什么」，报文是照着词表挑的；
// 这里只断言「无论收到什么，处置都自洽」，因此不依赖任何具体措辞，也不由实现驱动。

// assertPolicyInvariants 断言任意输入下都必须成立的结构性质。
//
// 这些都是消费方依赖的内容：动作集非空、重试与不重试二居其一、停用凭据必须同时换凭据、
// 计入熔断必须能换渠道、错误码落在文档化的四个上游码内。
func assertPolicyInvariants(t *testing.T, class Class) {
	t.Helper()
	policy := PolicyOf(class)
	if policy.Name == "" {
		t.Fatalf("类别 %d 没有策略名", class)
	}
	actions := policy.Actions
	if actions == 0 {
		t.Fatalf("%s 的动作集为空：消费方读到零值只能各自猜默认", policy.Name)
	}
	if actions.Retryable() == actions.Has(ActionSurface) {
		t.Fatalf("%s 的动作集必须「或可重试、或明确不重试」二居其一：%s", policy.Name, actions)
	}
	if actions.Has(ActionSuspendAccount) && !actions.Has(ActionRetryNextAccount) {
		t.Fatalf("%s 停用凭据却不换下一条：坏凭据会被反复选中", policy.Name)
	}
	if actions.Has(ActionCountBreaker) && !actions.Has(ActionRetryNextRoute) {
		t.Fatalf("%s 计入熔断却不换渠道：判死了渠道却无处可退", policy.Name)
	}
	suspends := actions.Has(ActionSuspendAccount)
	if !suspends && policy.SuspendFor != 0 {
		t.Fatalf("%s 没声明停用却带停用时长 %v", policy.Name, policy.SuspendFor)
	}
	if suspends && policy.SuspendFor < 0 {
		t.Fatalf("%s 的停用时长为负：%v", policy.Name, policy.SuspendFor)
	}

	switch code := CodeForClass(class); code {
	case domain.CodeUpstreamRateLimited, domain.CodeUpstreamTimeout,
		domain.CodeUpstreamUnavailable, domain.CodeUpstreamRejected:
	default:
		t.Fatalf("%s 推导出文档外的上游错误码 %s", policy.Name, code)
	}
	switch status := domain.HTTPStatus(NewError(CodeForClass(class), "x", "", class)); status {
	case 502, 503, 504:
	default:
		t.Fatalf("%s 推导出文档外的对外状态码 %d", policy.Name, status)
	}
}

// TestPolicyTableCoversEveryClass 断言类别枚举没有缺口。
//
// 新增一个类别却忘了登记策略，会让它静默落到 ClassOther 的处置上（不重试），
// 而调用方无从察觉。
func TestPolicyTableCoversEveryClass(t *testing.T) {
	for class := ClassOther; class <= ClassTimeout; class++ {
		policy, registered := policyTable[class]
		if !registered {
			t.Fatalf("类别 %d 未登记策略", class)
		}
		if policy.Name == "" {
			t.Fatalf("类别 %d 的策略缺少分类名", class)
		}
		assertPolicyInvariants(t, class)
	}
	// 枚举之外的取值也必须落到一个自洽的处置上，而不是零值。
	assertPolicyInvariants(t, Class(999))
}

// TestCodeRulesAgreeWithTable 断言机器码表的每个取值都能被读回同一个类别。
func TestCodeRulesAgreeWithTable(t *testing.T) {
	for value, want := range codeRules {
		if got := ClassifyCode(value); got != want {
			t.Errorf("ClassifyCode(%q) = %s，期望 %s", value, ClassName(got), ClassName(want))
		}
		// 大小写与空白不该改变结论：上游的大小写习惯很杂。
		if got := ClassifyCode("  " + strings.ToUpper(value) + "  "); got != want {
			t.Errorf("大小写/空白变体后 ClassifyCode(%q) = %s，期望 %s", value, ClassName(got), ClassName(want))
		}
	}
}

// TestGeneratedBodiesKeepDispositionCoherent 用生成的报文验证处置自洽。
//
// 生成器把厂商措辞的构件随机组合：已知关键词、结构化类型名、拼错的词、乱码、截断的 JSON、
// 只有标点的串、超长串。断言的是**与措辞无关的性质**：
//
//   - 命中了词表就一定按词表归类，无论状态码是什么（报文优先）；
//   - 5xx 上不会出现「凭据不被接受」——凭据字面量只扫 4xx；
//   - 类别落在枚举内，且处置自洽。
func TestGeneratedBodiesKeepDispositionCoherent(t *testing.T) {
	// 固定种子：失败可复现。迭代次数与填充长度按「能在门禁里跑完」取舍，
	// 更广的探索交给下面的 fuzz。
	//nolint:gosec // G404：测试输入生成器，不用于任何安全用途。
	rng := rand.New(rand.NewPCG(0x5eed, 0x1234))
	for i := 0; i < 3000; i++ {
		status := pickStatus(rng)
		body := generateBody(rng)

		class := ClassifyHTTP(status, []byte(body))
		assertPolicyInvariants(t, class)

		if class != ClassOther && class != ClassRequest && class != ClassAuth &&
			class != ClassQuota && class != ClassCredit && class != ClassRateLimit &&
			class != ClassContextLength && class != ClassModelUnavailable &&
			class != ClassUpstream && class != ClassTimeout {
			t.Fatalf("类别 %d 不在枚举内（状态码 %d，报文 %q）", class, status, body)
		}

		// 报文优先：命中词表时必须按词表归类，与状态码无关。
		if byPattern := matchPatterns([]byte(body)); byPattern != ClassOther && class != byPattern {
			t.Fatalf("报文命中词表（%s）却归成 %s（状态码 %d，报文 %q）",
				ClassName(byPattern), ClassName(class), status, body)
		}

		// 凭据字面量只扫 4xx：5xx 上不该出现「凭据不被接受」。
		if status >= 500 && class == ClassAuth {
			t.Fatalf("5xx 上归成凭据类（状态码 %d，报文 %q）", status, body)
		}
		if class == ClassAuth && (status < 400 || status > 499) {
			t.Fatalf("凭据类出现在 4xx 之外（状态码 %d，报文 %q）", status, body)
		}
	}
}

// FuzzClassifyHTTP 让 fuzz 引擎探索手写生成器想不到的输入形状。
//
// 这不替代上面的生成式用例：生成器能覆盖「像报文的东西」，fuzz 能覆盖「不像报文的东西」。
func FuzzClassifyHTTP(f *testing.F) {
	seeds := []struct {
		status int
		body   string
	}{
		{429, `{"error":{"message":"Rate limit reached"}}`},
		{401, `{}`},
		{500, ``},
		{200, `{"error":{"type":"insufficient_quota"}}`},
		{408, `quota`},
		{529, `{"error":{"message":"busy"}}`},
		{400, "\xff\xfe\x00garbage"},
	}
	for _, seed := range seeds {
		f.Add(seed.status, seed.body)
	}
	f.Fuzz(func(t *testing.T, status int, body string) {
		class := ClassifyHTTP(status, []byte(body))
		assertPolicyInvariants(t, class)
		// 同样的断言也过一遍结构化取值路径。
		assertPolicyInvariants(t, ClassifyCode(body))
	})
}

// BenchmarkClassifyHTTPLargeBody 量一下分类在大报文上的开销。
//
// 分类正则跑在**完整报文**上，而上游响应体上限是 32MB：也就是说极端情况下一次失败的
// 转发要在错误路径上扫几十 MB。这个基准把开销记下来，供以后决定是否给扫描定一个上限
// 时对照——错误信封通常只有几百字节，扫全量是为兼容把关键词埋在深处的上游。
func BenchmarkClassifyHTTPLargeBody(b *testing.B) {
	sizes := []int{1 << 10, 1 << 20, 32 << 20}
	for _, size := range sizes {
		body := []byte(`{"error":{"message":"` + strings.Repeat("x", size) + `"}}`)
		b.Run(byteSizeLabel(size), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				ClassifyHTTP(400, body)
			}
		})
	}
}

// byteSizeLabel 把字节数写成人读的标签。
func byteSizeLabel(size int) string {
	switch {
	case size >= 1<<20:
		return strconv.Itoa(size>>20) + "MiB"
	default:
		return strconv.Itoa(size>>10) + "KiB"
	}
}

// pickStatus 取一个状态码：真实取值与边界值混在一起。
func pickStatus(rng *rand.Rand) int {
	realistic := []int{
		200, 201, 400, 401, 403, 404, 408, 409, 413, 422, 429, 499,
		500, 501, 502, 503, 504, 520, 522, 524, 529, 599,
	}
	edges := []int{-1, 0, 99, 100, 199, 204, 299, 300, 301, 304, 399, 418, 451, 600, 999, 65535}
	if rng.IntN(4) == 0 {
		return edges[rng.IntN(len(edges))]
	}
	return realistic[rng.IntN(len(realistic))]
}

// generateBody 生成一份报文，构件取自「厂商措辞」而不是实现词表的原样复制。
func generateBody(rng *rand.Rand) string {
	signals := []string{
		// 已知语义的关键词（各厂商常见写法）
		"Rate limit reached for requests", "Too many requests", "overloaded", "busy",
		"insufficient_quota", "insufficient credits", "you exceeded your current quota",
		"billing", "payment required", "account suspended", "余额不足", "配额已用尽",
		"请求过于频繁", "当前并发数过高", "限流", "context length", "prompt is too long",
		"model not found", "ModelProtocolUnsupported", "invalid api key", "bad key",
		"API key not valid", "permission denied", "invalid_request_error", "not_found_error",
		// 拼错与变体：词表的近邻，用来探边界
		"rate-limit reached", "RateLimitReached", "overloaded_error", "rate_limit_error",
		"insufficient_credit", "quotaexceeded", "context_lenght_exceeded", "authenticationerror",
		// 与任何语义都无关的串
		"", "x", "...", "{}", "[]", "null", "\xff\xfe", "a b c",
	}
	var builder strings.Builder
	switch rng.IntN(5) {
	case 0:
		// 裸文本
		builder.WriteString(signals[rng.IntN(len(signals))])
	case 1:
		// 包成 JSON 错误对象
		builder.WriteString(`{"error":{"message":"`)
		builder.WriteString(signals[rng.IntN(len(signals))])
		builder.WriteString(`"}}`)
	case 2:
		// 把信号放进 type 或 code
		field := []string{"type", "code"}[rng.IntN(2)]
		builder.WriteString(`{"error":{"`)
		builder.WriteString(field)
		builder.WriteString(`":"`)
		builder.WriteString(strings.ReplaceAll(signals[rng.IntN(len(signals))], `"`, ``))
		builder.WriteString(`"}}`)
	case 3:
		// 多个信号拼在一起：模拟报文里同时出现多种线索
		builder.WriteString(`{"error":{"message":"`)
		for i := 0; i < 1+rng.IntN(3); i++ {
			builder.WriteString(signals[rng.IntN(len(signals))])
			builder.WriteString(" ")
		}
		builder.WriteString(`"}}`)
	default:
		// 截断的 JSON：上游断流或长度上限
		full := `{"error":{"message":"` + signals[rng.IntN(len(signals))] + `"}}`
		builder.WriteString(full[:1+rng.IntN(len(full))])
	}
	// 随机追加无意义填充：把关键词推到深层嵌套之后
	if rng.IntN(4) == 0 {
		builder.WriteString(strings.Repeat(" ", 1+rng.IntN(512)))
	}
	return builder.String()
}
