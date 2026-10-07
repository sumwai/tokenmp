package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/adapters/openaichat"
	"github.com/sumwai/tokenmp/internal/circuit"
	"github.com/sumwai/tokenmp/internal/credential"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/failure"
	"github.com/sumwai/tokenmp/internal/upstream"
)

// 本文件把「分类 → 处置」走到真实实现：模拟上游逐类回错误，接真实的凭据轮换器与熔断器，
// 断言的是网关**做了什么**——哪把 key 被停用、换不换渠道、渠道健康度怎么变、停用多久。
//
// 与 internal/upstream 的分类矩阵分开：那里证明「错误被归成什么类」，这里证明「归类之后
// 真的那么做了」。两者之间是装配缝隙，而缝隙正是回归的常见居所。
//
// 一条贯穿全篇的判据：**不能只看「这次用了哪把 key」**。轮换器每 Resolve 一次就把游标推进
// 一位，所以换渠道、换请求都会让 key 变化。真正能区分「因失败而换」的只有一件事：
// 冷却窗口内那把失败的 key 会不会回来。

// successBody 是一份最简可解码的 Chat Completions 应答。
const successBody = `{"id":"1","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

// failureFor 表示「请求携带这把凭据时回该错误，其余凭据一律成功」。
//
// 用它而不按调用次数编脚本：调用次数受换渠道与换凭据影响，而按凭据编脚本能让
// 「哪把 key 被停用」直接变成可断言的事实。
type failureFor struct {
	key    string
	status int
	body   string
}

// dispositionUpstream 是按脚本应答的假上游，并记录每次请求携带的凭据。
type dispositionUpstream struct {
	server *httptest.Server

	mu    sync.Mutex
	keys  []string
	calls int

	failure *failureFor
}

// newDispositionUpstream 起一个假上游；failure 非 nil 时只对该凭据回错误。
func newDispositionUpstream(t *testing.T, failure *failureFor) *dispositionUpstream {
	t.Helper()
	upstream := &dispositionUpstream{failure: failure}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		upstream.mu.Lock()
		upstream.keys = append(upstream.keys, key)
		upstream.calls++
		failure := upstream.failure
		upstream.mu.Unlock()

		if failure != nil && (failure.key == key || failure.key == anyKey) {
			w.WriteHeader(failure.status)
			_, _ = w.Write([]byte(failure.body))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(successBody))
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

// keySequence 返回上游依次看到的凭据。
func (u *dispositionUpstream) keySequence() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.keys...)
}

// firstKeys 返回每次请求使用的第一把凭据。
//
// 一次请求可能因为失败而反复调用上游，取第一次即可回答「这次请求从哪把 key 开始」。
func (u *dispositionUpstream) keysSince(mark int) []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if mark >= len(u.keys) {
		return nil
	}
	return append([]string(nil), u.keys[mark:]...)
}

// callCount 返回上游收到的请求数。
func (u *dispositionUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

// staticGroupLoader 按固定顺序返回同一组凭据。
type staticGroupLoader struct{ group credential.Group }

func (l staticGroupLoader) LoadGroup(context.Context, domain.Route) (credential.Group, error) {
	return l.group, nil
}

// advanceableClock 是可推进的时钟，使凭据冷却无需真实等待。
type advanceableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *advanceableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *advanceableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// dispositionFixture 是一次处置用例的装配结果。
type dispositionFixture struct {
	pipeline *Pipeline
	breaker  *circuit.Breaker
	clock    *advanceableClock
	writer   *recordingWriter
}

// newDispositionFixture 用真实轮换器、真实熔断器与真实上游客户端装配一条流水线。
//
// urls 按候选顺序给出每条渠道的上游地址（用例通常传多个假上游，以便分辨调用落在哪条渠道）。
// cooldown 是凭据类失败未给出类别时长时的兜底值。
// 退避的等待被替换成空实现：本文件断言的是换不换、换哪把，不是等了多久。
func newDispositionFixture(t *testing.T, urls []string, cooldown time.Duration) *dispositionFixture {
	t.Helper()
	clock := &advanceableClock{now: time.Unix(1_700_000_000, 0)}

	loader := staticGroupLoader{group: credential.Group{
		Scope: "merchant-1",
		Entries: []credential.NamedCredential{
			{ID: 1, Name: "a", APIKey: "sk-a"},
			{ID: 2, Name: "b", APIKey: "sk-b"},
			{ID: 3, Name: "c", APIKey: "sk-c"},
		},
	}}
	rotator, err := credential.NewRotator(credential.RotationOptions{
		Loader:   loader,
		Cooldown: cooldown,
		Clock:    clock.Now,
	})
	if err != nil {
		t.Fatalf("构造凭据轮换器失败：%v", err)
	}

	upstreamClient, err := upstream.New(upstream.Options{
		Headers:        credential.NewWithResolver(rotator),
		Adapters:       func(domain.Protocol) (domain.Adapter, error) { return openaichat.New(), nil },
		DefaultTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("构造上游客户端失败：%v", err)
	}

	routes := make([]domain.Route, 0, len(urls))
	for index, url := range urls {
		routes = append(routes, domain.Route{
			ChannelID:     uint64(index + 1),
			UpstreamID:    string(rune('1' + index)),
			Protocol:      domain.ProtocolOpenAIChat,
			UpstreamModel: "up-model",
			BaseURL:       url,
			CredentialRef: "group-a",
		})
	}

	breaker := circuit.NewBreaker(circuit.Options{})
	pipeline, err := New(Options{
		Adapters:    func(domain.Protocol) (domain.Adapter, error) { return openaichat.New(), nil },
		Upstream:    upstreamClient,
		Routes:      fakeRouteResolver{routes: routes},
		Credentials: rotator,
		Breaker:     breaker,
		MaxAttempts: 4,
		Backoff: BackoffOptions{
			Jitter: func(int64) int64 { return 0 },
			Wait:   func(context.Context, time.Duration) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("构造流水线失败：%v", err)
	}
	return &dispositionFixture{
		pipeline: pipeline,
		breaker:  breaker,
		clock:    clock,
		writer:   &recordingWriter{events: &[]string{}},
	}
}

// run 发一次非流式请求并返回结果错误。
func (f *dispositionFixture) run(t *testing.T) error {
	t.Helper()
	return f.pipeline.Forward(context.Background(), openaichat.New(), newChatRequest(false), f.writer)
}

// TestDispositionKeepsFailedCredentialCooling 断言「哪些类别会真的停用凭据」。
//
// 判据是冷却窗口内那把失败的 key 会不会回来：会回来说明只是游标轮转，不会回来才说明
// 它被停用了。只看「这次用了哪把 key」分辨不出这两者。
func TestDispositionKeepsFailedCredentialCooling(t *testing.T) {
	// 组内三把：失败的那把停用后，其余两把轮换，因此在冷却期内最多出现 2 把。
	const (
		authFailure  = `{"error":{"type":"authentication_error"}}`
		quotaFailure = `{"error":{"message":"当前套餐配额已用尽"}}`
		creditFailed = `{"error":{"message":"insufficient credits","type":"insufficient_credits"}}` //nolint:gosec // G101：上游错误报文里的字面量，不是凭据。
		rateFailure  = `{"error":{"message":"Rate limit reached"}}`
		reqFailure   = `{"error":{"type":"invalid_request_error"}}`
		upFailure    = `{"error":{"message":"boom"}}`
	)

	cases := []struct {
		name string
		fail failureFor
		// cooling 为真时，失败的那把 key 在冷却期内不得再被使用。
		cooling bool
		// firstFails 为真是说：该类别不换凭据，而单渠道下又无处可退，首请求只能失败。
		// 它会换凭据时，组内下一把接管，首请求因此整体成功。
		firstFails bool
		wantClass  failure.Class
	}{
		{name: "认证失败停用凭据", fail: failureFor{key: "sk-a", status: 401, body: authFailure}, cooling: true, wantClass: failure.ClassAuth},
		{name: "额度耗尽停用凭据", fail: failureFor{key: "sk-a", status: 400, body: quotaFailure}, cooling: true, wantClass: failure.ClassQuota},
		{name: "余额耗尽停用凭据", fail: failureFor{key: "sk-a", status: 400, body: creditFailed}, cooling: true, wantClass: failure.ClassCredit},
		// 限流说明渠道忙，不说明这把 key 坏了：不停用、也不换凭据，单渠道下只能失败。
		{name: "限流不停用凭据", fail: failureFor{key: "sk-a", status: 429, body: rateFailure}, firstFails: true, wantClass: failure.ClassRateLimit},
		// 请求级错误与凭据无关，一次都不重试。
		{name: "请求级失败不停用凭据", fail: failureFor{key: "sk-a", status: 400, body: reqFailure}, firstFails: true, wantClass: failure.ClassRequest},
		// 上游自身故障不说明凭据有问题：换渠道而不换凭据，单渠道下只能失败。
		{name: "上游故障不停用凭据", fail: failureFor{key: "sk-a", status: 500, body: upFailure}, firstFails: true, wantClass: failure.ClassUpstream},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstreamServer := newDispositionUpstream(t, &tc.fail)
			fixture := newDispositionFixture(t, []string{upstreamServer.server.URL}, 30*time.Second)

			firstErr := fixture.run(t)
			if fixtureFailed(t, tc.firstFails, firstErr, tc.wantClass) {
				return
			}
			// 游标从 0 开始，首次请求必定先从 sk-a 起手。
			if keys := upstreamServer.keySequence(); len(keys) == 0 || keys[0] != "sk-a" {
				t.Fatalf("首次请求未使用 sk-a：%v", keys)
			}

			// 后续三次请求都在冷却窗口内（时钟未推进），只看用了哪些 key。
			mark := upstreamServer.callCount()
			for i := 0; i < 3; i++ {
				_ = fixture.run(t)
			}
			seen := upstreamServer.keysSince(mark)
			cameBack := containsString(seen, "sk-a")
			if tc.cooling && cameBack {
				t.Fatalf("冷却期内失败的凭据回到轮换：%v", seen)
			}
			if !tc.cooling && !cameBack {
				t.Fatalf("不该停用的凭据在三次请求内未回到轮换：%v", seen)
			}
		})
	}
}

// fixtureFailed 按 firstFails 断言首请求的结果，返回用例是否应当提前结束。
func fixtureFailed(t *testing.T, firstFails bool, err error, wantClass failure.Class) bool {
	t.Helper()
	if !firstFails {
		if err != nil {
			t.Fatalf("该类别会换凭据，组内下一把应接管，实际失败：%v", err)
		}
		return false
	}
	if err == nil {
		t.Fatal("该类别不换凭据且单渠道无处可退，首请求应失败")
	}
	if got := failure.ClassOf(err); got != wantClass {
		t.Fatalf("首请求的类别 = %s，期望 %s", failure.ClassName(got), failure.ClassName(wantClass))
	}
	return true
}

// TestDispositionSuspendsByClassDuration 断言「按类别给停用时长」真的作用在后续请求上。
//
// 三类停用时长分别是额度 15 分钟、余额 30 分钟、认证沿用配置兜底值。它们只在
// 「冷却到期后那把 key 是否回到轮换」这件事上有意义，光看错误里的字段证明不了。
func TestDispositionSuspendsByClassDuration(t *testing.T) {
	cases := []struct {
		name     string
		fail     failureFor
		suspend  time.Duration
		fallback time.Duration
	}{
		{
			name:     "额度耗尽按类别停 15 分钟",
			fail:     failureFor{key: "sk-a", status: 400, body: `{"error":{"message":"当前套餐配额已用尽"}}`},
			suspend:  15 * time.Minute,
			fallback: 30 * time.Second,
		},
		{
			name:     "余额耗尽按类别停 30 分钟",
			fail:     failureFor{key: "sk-a", status: 400, body: `{"error":{"message":"insufficient credits","type":"insufficient_credits"}}`},
			suspend:  30 * time.Minute,
			fallback: 30 * time.Second,
		},
		{
			// 认证类不另立数值：由调用方配置的兜底冷却决定，类别只给出「该停用」。
			name:     "认证失败沿用配置的兜底冷却",
			fail:     failureFor{key: "sk-a", status: 401, body: `{"error":{"type":"authentication_error"}}`},
			suspend:  30 * time.Second,
			fallback: 30 * time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstreamServer := newDispositionUpstream(t, &tc.fail)
			fixture := newDispositionFixture(t, []string{upstreamServer.server.URL}, tc.fallback)

			// 首请求会因换凭据而整体成功，但它必定先从 sk-a 起手（游标从 0 开始），
			// 于是 sk-a 被标上冷却 —— 这正是本用例要观察的状态。
			_ = fixture.run(t)
			if keys := upstreamServer.keySequence(); len(keys) == 0 || keys[0] != "sk-a" {
				t.Fatalf("首次请求未使用 sk-a：%v", keys)
			}

			// 距到期还差 1 秒：失败的 key 仍不得出现。
			fixture.clock.advance(tc.suspend - time.Second)
			mark := upstreamServer.callCount()
			for i := 0; i < 3; i++ {
				_ = fixture.run(t)
			}
			if seen := upstreamServer.keysSince(mark); containsString(seen, "sk-a") {
				t.Fatalf("冷却未到期时失败的凭据回到轮换：%v", seen)
			}

			// 到期后：最多再轮换两把就应回到它。
			fixture.clock.advance(2 * time.Second)
			mark = upstreamServer.callCount()
			for i := 0; i < 3; i++ {
				_ = fixture.run(t)
			}
			if seen := upstreamServer.keysSince(mark); !containsString(seen, "sk-a") {
				t.Fatalf("冷却到期后失败的凭据未回到轮换：%v", seen)
			}
		})
	}
}

// TestDispositionSwitchesRouteOnUpstreamFailure 断言上游故障换渠道，并落在另一条渠道上。
func TestDispositionSwitchesRouteOnUpstreamFailure(t *testing.T) {
	// 两条渠道都与凭据无关：一条稳定 500，一条稳定成功。考察的是渠道回退，不是凭据处置。
	broken := newDispositionUpstream(t, &failureFor{key: anyKey, status: 500, body: `{"error":{"message":"boom"}}`})
	healthy := newDispositionUpstream(t, nil)
	fixture := newDispositionFixture(t, []string{broken.server.URL, healthy.server.URL}, 30*time.Second)

	if err := fixture.run(t); err != nil {
		t.Fatalf("第一条渠道故障后应回退到第二条，实际 %v", err)
	}
	if got := healthy.callCount(); got != 1 {
		t.Fatalf("健康渠道收到 %d 次调用，期望 1", got)
	}
	// 上游故障不停用凭据，但组内下一把 key 会被游标带出来；对同一渠道只记一次尝试失败，
	// 因此「尝试上限 4」与「组内 3 把」不会把它变成反复重试同一渠道。
	if got := broken.callCount(); got == 0 {
		t.Fatal("故障渠道应被尝试过至少一次")
	}
}

// TestDispositionBreakerOpensAndSkips 断言连续上游故障把渠道判死后，候选遍历会跳过它。
//
// 与「是否上报了失败」不同：这里断言的是跳过这个可观察后果。用两条渠道，
// 一条稳定故障、一条稳定成功——不开熔断时每次请求都会先撞故障渠道。
func TestDispositionBreakerOpensAndSkips(t *testing.T) {
	const upstreamFailure = `{"error":{"message":"boom"}}`
	broken := newDispositionUpstream(t, &failureFor{key: anyKey, status: 500, body: upstreamFailure})
	healthy := newDispositionUpstream(t, nil)
	fixture := newDispositionFixture(t, []string{broken.server.URL, healthy.server.URL}, 30*time.Second)

	const openings = 5
	for i := 0; i < openings; i++ {
		if err := fixture.run(t); err != nil {
			t.Fatalf("第 %d 次请求期望成功（回退到健康渠道），实际 %v", i+1, err)
		}
	}
	if status := fixture.breaker.Status("1"); status.State != circuit.StateOpen {
		t.Fatalf("连续 %d 次上游故障后渠道 1 状态 = %s，期望 open", openings, status.State)
	}
	brokenCalls := broken.callCount()

	// 熔断打开后：候选遍历应跳过渠道 1，请求直接落在渠道 2。
	if err := fixture.run(t); err != nil {
		t.Fatalf("熔断打开后仍应能由健康渠道履约，实际 %v", err)
	}
	if got := broken.callCount(); got != brokenCalls {
		t.Fatalf("熔断打开后仍调用了故障渠道：%d → %d", brokenCalls, got)
	}
	if status := fixture.breaker.Status("2"); status.ConsecutiveFailures != 0 {
		t.Fatalf("健康渠道的连续失败 = %d，期望 0", status.ConsecutiveFailures)
	}
}

// anyKey 表示脚本命中任何凭据。
const anyKey = "*"

// containsString 报告切片里是否含某个值。
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
