package credential

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/oauth"
)

// 本文件实现订阅型凭据的惰性续期：取用凭据时若访问令牌临近过期，先用刷新令牌换新。
//
// 三个约束在实现里各自有一处落点：
//
//   - 单飞行：同一凭据上的并发请求等待同一次续期，一次性刷新令牌不会被消耗两次；
//   - 20 秒上限：单次续期有截止时间，等待者也不会无限等；
//   - invalid_grant 与其余失败分流：前者把凭据判为失效（交给轮换冷却），后者保留旧令牌照常发请求。

const (
	// defaultOAuthRefreshWindow 是提前续期窗口：访问令牌剩余有效期不足该值时先续期。
	defaultOAuthRefreshWindow = 5 * time.Minute
	// defaultOAuthRefreshTimeout 是单次续期与等待续期的上限。
	defaultOAuthRefreshTimeout = 15 * time.Second
)

// OAuthCredential 是订阅型凭据的续期相关字段。
type OAuthCredential struct {
	// Access 是访问令牌，注入上游请求头。
	Access string
	// Refresh 是刷新令牌。
	Refresh string
	// Expires 是访问令牌的过期时刻；零值表示未知。
	Expires time.Time
	// Account 是账户标识，仅用于展示与排障。
	Account string
}

// String 实现 fmt.Stringer：令牌经 %v 打印时只出现占位符。
func (c OAuthCredential) String() string { return redactedSecret }

// LogValue 实现 slog.LogValuer：凭据作为日志字段时只出现占位符。
func (c OAuthCredential) LogValue() slog.Value { return slog.StringValue(redactedSecret) }

// OAuthRefreshInput 是一次续期请求，令牌字段不落日志。
type OAuthRefreshInput struct {
	// CredentialID 是凭据行 id，单飞行按它归并。
	CredentialID uint64
	// Name 是凭据名，只用于日志。
	Name string
	// Profile 是渠道声明的 OAuth 端点画像。
	Profile domain.OAuthProfile
	// Access、Refresh、Expires、Account 是续期前的当前值。
	Access  string
	Refresh string
	Expires time.Time
	Account string
}

// String 实现 fmt.Stringer：续期输入经 %v 打印时只出现占位符。
func (in OAuthRefreshInput) String() string { return redactedSecret }

// LogValue 实现 slog.LogValuer：续期输入作为日志字段时只出现占位符。
func (in OAuthRefreshInput) LogValue() slog.Value { return slog.StringValue(redactedSecret) }

// OAuthRefreshOutput 是一次续期的成功结果。
type OAuthRefreshOutput struct {
	Access  string
	Refresh string
	Expires time.Time
}

// String 实现 fmt.Stringer：续期结果经 %v 打印时只出现占位符。
func (out OAuthRefreshOutput) String() string { return redactedSecret }

// LogValue 实现 slog.LogValuer：续期结果作为日志字段时只出现占位符。
func (out OAuthRefreshOutput) LogValue() slog.Value { return slog.StringValue(redactedSecret) }

// OAuthRefresher 执行一次真正的续期：换令牌并把新值写回凭据行。
//
// 实现必须把 oauth.ErrInvalidGrant 原样返回（或包在其中），使调用方能区分
// 「凭据失效」与「端点暂时不可用」。
type OAuthRefresher interface {
	Refresh(ctx context.Context, in OAuthRefreshInput) (OAuthRefreshOutput, error)
}

// errOAuthRefreshTimeout 报告等待他人续期超过了上限。
var errOAuthRefreshTimeout = errors.New("oauth: 等待续期超时")

// RenewalOptions 是续期的可调参数。
type RenewalOptions struct {
	// Window 是提前续期窗口；<= 0 时取 defaultOAuthRefreshWindow。
	Window time.Duration
	// Timeout 是单次续期与等待续期的上限；<= 0 时取 defaultOAuthRefreshTimeout。
	Timeout time.Duration
	// Refresher 执行续期；nil 时不做续期。
	Refresher OAuthRefresher
}

// renewalState 是续期的进程内状态：单飞行表与两个时长。
type renewalState struct {
	refresher OAuthRefresher
	window    time.Duration
	timeout   time.Duration

	mu      sync.Mutex
	flights map[uint64]*refreshFlight
}

// refreshFlight 是一次进行中的续期。
//
// 结果字段在 close(done) 之前写入；等待者从 done 收到零值即建立 happens-before。
type refreshFlight struct {
	done   chan struct{}
	output OAuthRefreshOutput
	err    error
}

// newRenewalState 构造续期状态；缺少刷新器时返回 nil，表示不做续期。
func newRenewalState(opts *RenewalOptions) *renewalState {
	if opts == nil || opts.Refresher == nil {
		return nil
	}
	window := opts.Window
	if window <= 0 {
		window = defaultOAuthRefreshWindow
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultOAuthRefreshTimeout
	}
	return &renewalState{
		refresher: opts.Refresher,
		window:    window,
		timeout:   timeout,
		flights:   make(map[uint64]*refreshFlight),
	}
}

// due 报告访问令牌是否已进入提前续期窗口。
//
// 零值（有效期未知）不续期：没有依据判断该不该换，贸然续期会在每次请求上多打一次端点。
func (s *renewalState) due(expires, now time.Time) bool {
	if s == nil || expires.IsZero() {
		return false
	}
	return !expires.After(now.Add(s.window))
}

// refresh 执行或等待一次续期。
//
// 同一凭据 id 上的并发调用只发一次请求：首位调用者是执行者，其余等待它的结果。
// 等待者与执行者都受 timeout 约束；执行者的超时作用在 refresher 调用上，
// 等待者的超时只决定「等不到就不等」，两者都返回非 invalid_grant 错误，调用方据此保留旧令牌。
func (s *renewalState) refresh(ctx context.Context, in OAuthRefreshInput) (OAuthRefreshOutput, error) {
	s.mu.Lock()
	if flight, ok := s.flights[in.CredentialID]; ok {
		s.mu.Unlock()
		return s.wait(ctx, flight)
	}
	flight := &refreshFlight{done: make(chan struct{})}
	s.flights[in.CredentialID] = flight
	s.mu.Unlock()

	output, err := s.run(ctx, in)

	s.mu.Lock()
	delete(s.flights, in.CredentialID)
	s.mu.Unlock()
	flight.output, flight.err = output, err
	close(flight.done)
	return output, err
}

// wait 等待一次进行中的续期；超过上限或 ctx 取消时返回非 invalid_grant 错误。
func (s *renewalState) wait(ctx context.Context, flight *refreshFlight) (OAuthRefreshOutput, error) {
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case <-flight.done:
		return flight.output, flight.err
	case <-ctx.Done():
		return OAuthRefreshOutput{}, ctx.Err()
	case <-timer.C:
		return OAuthRefreshOutput{}, errOAuthRefreshTimeout
	}
}

// run 在超时约束下执行一次续期。
func (s *renewalState) run(ctx context.Context, in OAuthRefreshInput) (OAuthRefreshOutput, error) {
	runCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.refresher.Refresh(runCtx, in)
}

// oauthInvalidGrantError 报告刷新令牌已被端点拒绝。
//
// 它同时满足 domain.CredentialRejection，使流水线把它当作凭据类失败：
// 本次渠道尝试随后换组内下一条凭据，被拒的那条进入冷却。
type oauthInvalidGrantError struct {
	name string
}

// Error 实现 error 接口；只给出凭据名，不含任何令牌。
func (e *oauthInvalidGrantError) Error() string {
	return fmt.Sprintf("OAuth 凭据 %q 的刷新令牌已失效", e.name)
}

// CredentialRejected 报告本次失败属凭据类。
func (e *oauthInvalidGrantError) CredentialRejected() bool { return true }

// isInvalidGrant 报告续期失败是否属于端点明确拒绝刷新令牌。
func isInvalidGrant(err error) bool {
	return errors.Is(err, oauth.ErrInvalidGrant)
}
