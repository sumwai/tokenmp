package requestlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/access"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/transport"
)

// 本文件覆盖请求记录落库实现：三个端口各自的字段映射、终态推导与失败语义。
// 存储层用替身，全链不经数据库。

// statsDelta 是一次计数累加的入参。
type statsDelta struct {
	AccountID uint64
	Day       time.Time
	Model     string
	APIKeyID  uint64
	Status    string
}

// fakeStore 是请求记录存储面的内存替身。
type fakeStore struct {
	attempts   []store.RequestAttempt
	attemptErr error

	usages   []store.RequestUsage
	usageErr error

	outcomes   []store.RequestOutcome
	outcomeErr error

	lastAttempt    *store.RequestAttempt
	lastAttemptErr error

	stats    []statsDelta
	statsErr error
}

func (f *fakeStore) InsertRequestAttempt(_ context.Context, a store.RequestAttempt) error {
	if f.attemptErr != nil {
		return f.attemptErr
	}
	f.attempts = append(f.attempts, a)
	return nil
}

func (f *fakeStore) UpsertRequestUsage(_ context.Context, u store.RequestUsage) error {
	if f.usageErr != nil {
		return f.usageErr
	}
	f.usages = append(f.usages, u)
	return nil
}

func (f *fakeStore) UpsertRequestOutcome(_ context.Context, o store.RequestOutcome) error {
	if f.outcomeErr != nil {
		return f.outcomeErr
	}
	f.outcomes = append(f.outcomes, o)
	return nil
}

func (f *fakeStore) LastRequestAttempt(_ context.Context, _ string) (*store.RequestAttempt, error) {
	if f.lastAttemptErr != nil {
		return nil, f.lastAttemptErr
	}
	if f.lastAttempt == nil {
		return nil, sql.ErrNoRows
	}
	return f.lastAttempt, nil
}

func (f *fakeStore) UpsertRequestStatsDaily(_ context.Context, accountID uint64, day time.Time,
	model string, apiKeyID uint64, status string) error {
	if f.statsErr != nil {
		return f.statsErr
	}
	f.stats = append(f.stats, statsDelta{AccountID: accountID, Day: day, Model: model,
		APIKeyID: apiKeyID, Status: status})
	return nil
}

// newRecorder 构造一个时钟固定的实现，便于断言按天分桶。
func newRecorder(t *testing.T, st Store, now time.Time) *Recorder {
	t.Helper()
	return New(Options{Store: st, Now: func() time.Time { return now }, Logf: func(string, ...any) {}})
}

// authedContext 返回带鉴权归属的上下文。
func authedContext() context.Context {
	return access.WithIdentity(context.Background(), access.Identity{AccountID: 42, MerchantID: 3, APIKeyID: 9})
}

// TestRecordAttemptMapsFields 断言尝试记录按契约字段落库。
func TestRecordAttemptMapsFields(t *testing.T) {
	st := &fakeStore{}
	rec := newRecorder(t, st, time.Now())
	started := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	err := rec.RecordAttempt(authedContext(), domain.AttemptRecord{
		RequestID:        "req-1",
		Attempt:          2,
		Outcome:          domain.AttemptFailed,
		UpstreamStatus:   429,
		FailureClass:     "rate_limit",
		ErrorCode:        "upstream_rate_limited",
		ClientProtocol:   domain.ProtocolOpenAIChat,
		UpstreamProtocol: domain.ProtocolAnthropicMessages,
		CrossProtocol:    true,
		RequestedModel:   "client-model",
		UpstreamModel:    "up-model",
		StartedAt:        started,
		EndedAt:          started.Add(80 * time.Millisecond),
		RewrittenParts:   domain.RewriteParts{domain.RewritePartRequestModel},
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(st.attempts) != 1 {
		t.Fatalf("尝试记录条数 = %d，期望 1", len(st.attempts))
	}
	got := st.attempts[0]
	if got.RequestID != "req-1" || got.Attempt != 2 || got.Outcome != string(domain.AttemptFailed) {
		t.Errorf("尝试记录 = %+v，与入参不符", got)
	}
	if got.UpstreamStatus != 429 || got.FailureClass != "rate_limit" || !got.CrossProtocol {
		t.Errorf("上游侧字段 = %+v，与入参不符", got)
	}
	if got.ClientProtocol != string(domain.ProtocolOpenAIChat) ||
		got.UpstreamProtocol != string(domain.ProtocolAnthropicMessages) {
		t.Errorf("协议字段 = %s / %s，与入参不符", got.ClientProtocol, got.UpstreamProtocol)
	}
	if got.DurationMS != 80 {
		t.Errorf("耗时 = %d，期望 80", got.DurationMS)
	}
	if string(got.RewrittenParts) != `["request_model"]` {
		t.Errorf("改写标注 = %s，期望 [\"model\"]", got.RewrittenParts)
	}
}

// TestRecordUsageRequiresIdentity 断言缺少鉴权上下文时不落库并返回错误：
// 没有归属的行谁都读不到，落库只会污染数据。
func TestRecordUsageRequiresIdentity(t *testing.T) {
	st := &fakeStore{}
	rec := newRecorder(t, st, time.Now())

	if err := rec.RecordUsage(context.Background(), domain.UsageRecord{RequestID: "req-1"}); err == nil {
		t.Errorf("缺少鉴权上下文应返回错误")
	}
	if len(st.usages) != 0 {
		t.Errorf("缺少鉴权上下文不应落库，得到 %+v", st.usages)
	}
}

// TestRecordUsageWritesOwnership 断言归属与用量落库；未取得用量时写 NULL。
func TestRecordUsageWritesOwnership(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	st := &fakeStore{}
	rec := newRecorder(t, st, now)

	err := rec.RecordUsage(authedContext(), domain.UsageRecord{
		RequestID: "req-1",
		Usage:     domain.Usage{Source: domain.UsageSourceUpstream, InputTokens: 10, OutputTokens: 4},
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(st.usages) != 1 {
		t.Fatalf("用量记录条数 = %d，期望 1", len(st.usages))
	}
	got := st.usages[0]
	if got.AccountID != 42 || got.MerchantID != 3 || got.APIKeyID != 9 {
		t.Errorf("归属 = %+v，与鉴权上下文不符", got)
	}
	var usage domain.Usage
	if err := json.Unmarshal(got.Usage, &usage); err != nil {
		t.Fatalf("解析用量 JSON：%v", err)
	}
	if usage.InputTokens != 10 || usage.Source != domain.UsageSourceUpstream {
		t.Errorf("用量 = %+v，与入参不符", usage)
	}

	// 未取得用量：写 NULL 而不是零值对象，页面据此显示「未取得」。
	st = &fakeStore{}
	rec = newRecorder(t, st, now)
	if err := rec.RecordUsage(authedContext(), domain.UsageRecord{RequestID: "req-2"}); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if st.usages[0].Usage != nil {
		t.Errorf("未取得用量应写 NULL，得到 %s", st.usages[0].Usage)
	}
}

// TestLogAccessWritesStatusAndStats 断言终态、归属与计数累加落库。
func TestLogAccessWritesStatusAndStats(t *testing.T) {
	now := time.Date(2026, 10, 8, 23, 30, 0, 0, time.UTC)
	st := &fakeStore{lastAttempt: &store.RequestAttempt{
		UpstreamModel:    "up-model",
		UpstreamProtocol: "anthropic_messages",
		FailureClass:     "rate_limit",
		RewrittenParts:   json.RawMessage(`["request_model"]`),
	}}
	rec := newRecorder(t, st, now)

	rec.LogAccess(authedContext(), transport.AccessRecord{
		RequestID:      "req-1",
		Protocol:       domain.ProtocolOpenAIChat,
		Model:          "client-model",
		UpstreamStatus: 429,
		CrossProtocol:  true,
		Stream:         true,
		HTTPStatus:     429,
		DurationMS:     120,
		WrittenBytes:   2048,
		ErrorCode:      "upstream_rate_limited",
		RemoteAddr:     "203.0.113.7",
		UserAgent:      "client/1.0",
	})
	if len(st.outcomes) != 1 {
		t.Fatalf("终态记录条数 = %d，期望 1", len(st.outcomes))
	}
	got := st.outcomes[0]
	if got.Status != statusFailed || got.HTTPStatus != 429 || got.DurationMS != 120 {
		t.Errorf("终态 = %+v，与入参不符", got)
	}
	if got.UpstreamModel != "up-model" || got.UpstreamProtocol != "anthropic_messages" ||
		got.FailureClass != "rate_limit" {
		t.Errorf("上游侧事实 = %+v，应取自最后一次尝试", got)
	}
	if got.RequestedModel != "client-model" || got.ClientIP != "203.0.113.7" || !got.Stream {
		t.Errorf("客户端侧事实 = %+v，与入参不符", got)
	}
	if len(st.stats) != 1 {
		t.Fatalf("计数累加条数 = %d，期望 1", len(st.stats))
	}
	delta := st.stats[0]
	if delta.AccountID != 42 || delta.APIKeyID != 9 || delta.Model != "client-model" ||
		delta.Status != statusFailed {
		t.Errorf("计数入参 = %+v，与入参不符", delta)
	}
	// 按天分桶取记录时刻的日期，而不是当前时刻的日期：跨零点的请求只应记进它发生的那天。
	if want := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC); !delta.Day.Equal(want) {
		t.Errorf("日期桶 = %s，期望 %s", delta.Day, want)
	}
}

// TestLogAccessStatus 断言终态推导：客户端断开是独立终态，与网关返回错误分开。
func TestLogAccessStatus(t *testing.T) {
	tests := []struct {
		name       string
		httpStatus int
		cancelled  bool
		want       string
	}{
		{name: "成功", httpStatus: 200, want: statusSuccess},
		{name: "服务端错误", httpStatus: 500, want: statusFailed},
		{name: "客户端断开", httpStatus: 200, cancelled: true, want: statusCancelled},
		{name: "断开的失败请求", httpStatus: 499, cancelled: true, want: statusCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStore{}
			rec := newRecorder(t, st, time.Now())
			ctx := authedContext()
			if tt.cancelled {
				cancelledCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelledCtx
			}
			rec.LogAccess(ctx, transport.AccessRecord{RequestID: "req-1", HTTPStatus: tt.httpStatus})
			if len(st.outcomes) != 1 {
				t.Fatalf("终态记录条数 = %d，期望 1", len(st.outcomes))
			}
			if got := st.outcomes[0].Status; got != tt.want {
				t.Errorf("终态 = %q，期望 %q", got, tt.want)
			}
		})
	}
}

// TestLogAccessSkipsUnauthenticated 断言未鉴权的请求不落记录、不计入聚合。
func TestLogAccessSkipsUnauthenticated(t *testing.T) {
	st := &fakeStore{}
	rec := newRecorder(t, st, time.Now())
	rec.LogAccess(context.Background(), transport.AccessRecord{RequestID: "req-1", HTTPStatus: 401})
	if len(st.outcomes) != 0 || len(st.stats) != 0 {
		t.Errorf("未鉴权请求不应落库，得到终态 %d 条、计数 %d 条", len(st.outcomes), len(st.stats))
	}
}

// TestLogAccessSurvivesWriteFailure 断言落库失败不影响调用方：观测是转发的旁路。
func TestLogAccessSurvivesWriteFailure(t *testing.T) {
	st := &fakeStore{outcomeErr: errors.New("数据库不可达"), statsErr: errors.New("数据库不可达")}
	rec := newRecorder(t, st, time.Now())
	// 断言的是「不 panic 且不改变转发结果」：LogAccess 没有返回值，失败只记日志。
	rec.LogAccess(authedContext(), transport.AccessRecord{RequestID: "req-1", HTTPStatus: 200})
}

// TestObserverTeeWritesBothSides 断言副本与主落点都收到事实，副本失败被忽略。
func TestObserverTeeWritesBothSides(t *testing.T) {
	primary := &fakeStore{}
	secondary := &fakeStore{attemptErr: errors.New("副本失败")}
	tee := ObserverTee{Primary: New(Options{Store: primary, Logf: func(string, ...any) {}}),
		Secondary: New(Options{Store: secondary, Logf: func(string, ...any) {}})}

	if err := tee.RecordAttempt(authedContext(), domain.AttemptRecord{RequestID: "req-1", Attempt: 1}); err != nil {
		t.Fatalf("副本失败不应冒泡：%v", err)
	}
	if len(primary.attempts) != 1 || len(secondary.attempts) != 0 {
		t.Errorf("主落点 %d 条、副本 %d 条，期望 1 / 0", len(primary.attempts), len(secondary.attempts))
	}
}

// TestUsageTeeWritesBothSides 断言用量事实同样分发给两个落点。
func TestUsageTeeWritesBothSides(t *testing.T) {
	primary := &fakeStore{}
	secondary := &fakeStore{}
	tee := UsageTee{Primary: New(Options{Store: primary, Logf: func(string, ...any) {}}),
		Secondary: New(Options{Store: secondary, Logf: func(string, ...any) {}})}

	if err := tee.RecordUsage(authedContext(), domain.UsageRecord{RequestID: "req-1"}); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(primary.usages) != 1 || len(secondary.usages) != 1 {
		t.Errorf("主落点 %d 条、副本 %d 条，期望 1 / 1", len(primary.usages), len(secondary.usages))
	}
}

// TestAccessTeeWritesBothSides 断言访问记录同样分发给两个落点。
func TestAccessTeeWritesBothSides(t *testing.T) {
	primary := &fakeStore{}
	secondary := &fakeStore{}
	tee := AccessTee{Primary: New(Options{Store: primary, Logf: func(string, ...any) {}}),
		Secondary: New(Options{Store: secondary, Logf: func(string, ...any) {}})}

	tee.LogAccess(authedContext(), transport.AccessRecord{RequestID: "req-1", HTTPStatus: 200})
	if len(primary.outcomes) != 1 || len(secondary.outcomes) != 1 {
		t.Errorf("主落点 %d 条、副本 %d 条，期望 1 / 1", len(primary.outcomes), len(secondary.outcomes))
	}
}
