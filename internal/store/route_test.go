package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// recordedQuery 是 querier 的记录型假实现：不连数据库，只记下收到的 SQL 与参数，
// 并按预设行集返回结果。与 billing_test.go 的 recordedExec 同一思路，差异只在读路径。
type recordedQuery struct {
	calls int
	query string
	args  []any
	rows  [][]any
	err   error
}

func (f *recordedQuery) QueryContext(_ context.Context, query string, args ...any) (rowIter, error) {
	f.calls++
	f.query = query
	f.args = append([]any(nil), args...)
	if f.err != nil {
		return nil, f.err
	}
	return &fakeRows{rows: f.rows}, nil
}

// fakeRows 是 rowIter 的内存实现。
type fakeRows struct {
	rows [][]any
	idx  int
	err  error
}

func (r *fakeRows) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	if r.idx == 0 || r.idx > len(r.rows) {
		return errors.New("Scan 在 Next 之前调用")
	}
	row := r.rows[r.idx-1]
	if len(row) != len(dest) {
		return fmt.Errorf("列数不匹配：行有 %d 列，目标有 %d 列", len(row), len(dest))
	}
	for i := range dest {
		if err := assignScan(dest[i], row[i]); err != nil {
			return fmt.Errorf("第 %d 列: %w", i, err)
		}
	}
	return nil
}

func (r *fakeRows) Err() error { return r.err }

func (r *fakeRows) Close() error { return nil }

// assignScan 把一行里的一个值写入 Scan 目标指针。
//
// 用反射而不是逐类型断言：读路径的 Scan 目标包含指针、可空类型与字节切片三类，
// 逐类型写一遍会让测试假实现随表结构变更而反复膨胀。
func assignScan(dest any, src any) error {
	target := reflect.ValueOf(dest)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("Scan 目标必须是非空指针，得到 %T", dest)
	}
	elem := target.Elem()
	if src == nil {
		elem.Set(reflect.Zero(elem.Type()))
		return nil
	}
	value := reflect.ValueOf(src)
	switch {
	case value.Type().AssignableTo(elem.Type()):
		elem.Set(value)
	case value.Type().ConvertibleTo(elem.Type()):
		elem.Set(value.Convert(elem.Type()))
	default:
		return fmt.Errorf("无法把 %T 赋给 %s", src, elem.Type())
	}
	return nil
}

func TestLookupAPIKey(t *testing.T) {
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	fake := &recordedQuery{rows: [][]any{
		{uint64(7), uint64(3), "active", sql.NullInt64{Int64: 9, Valid: true}, sql.NullInt64{Int64: 5, Valid: true}},
	}}

	got, err := lookupAPIKey(context.Background(), fake, "abc123", now)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if fake.query != lookupAPIKeySQL {
		t.Errorf("SQL = %q，期望 %q", fake.query, lookupAPIKeySQL)
	}
	wantArgs := []any{"abc123", now}
	if !reflect.DeepEqual(fake.args, wantArgs) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, wantArgs)
	}
	want := APIKeyAuth{APIKeyID: 7, AccountID: 3, AccountStatus: "active", MerchantID: 9}
	if *got != want {
		t.Errorf("结果 = %+v，期望 %+v", *got, want)
	}
	// 过滤条件写在 SQL 里：启用、未过期。语句一旦被改掉这里立刻能发现。
	for _, wantClause := range []string{"k.enabled = 1", "k.expires_at IS NULL", "k.expires_at > ?"} {
		if !strings.Contains(fake.query, wantClause) {
			t.Errorf("SQL 缺少过滤条件 %q：%s", wantClause, fake.query)
		}
	}
}

func TestLookupAPIKeyNoRows(t *testing.T) {
	fake := &recordedQuery{}
	_, err := lookupAPIKey(context.Background(), fake, "abc", time.Now())
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("无匹配时应能被 errors.Is(err, sql.ErrNoRows) 识别，得到 %v", err)
	}
}

func TestLookupAPIKeyRejectsEmptyHash(t *testing.T) {
	fake := &recordedQuery{}
	if _, err := lookupAPIKey(context.Background(), fake, "   ", time.Now()); err == nil {
		t.Fatal("空哈希应当被拒绝")
	}
	if fake.calls != 0 {
		t.Errorf("校验失败不应触达驱动，实际调用 %d 次", fake.calls)
	}
}

func TestLookupAPIKeyWrapsDriverError(t *testing.T) {
	fake := &recordedQuery{err: errExecFailure}
	_, err := lookupAPIKey(context.Background(), fake, "abc", time.Now())
	if !errors.Is(err, errExecFailure) {
		t.Errorf("驱动错误应被包装可辨识，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "account_api_key") {
		t.Errorf("错误信息应含表名，得到 %v", err)
	}
}

func TestResolveMerchantID(t *testing.T) {
	valid := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	invalid := sql.NullInt64{}
	tests := []struct {
		name            string
		keyMerchant     sql.NullInt64
		accountMerchant sql.NullInt64
		want            uint64
	}{
		{name: "密钥绑定优先", keyMerchant: valid(9), accountMerchant: valid(5), want: 9},
		{name: "密钥无绑定时用账户默认", keyMerchant: invalid, accountMerchant: valid(5), want: 5},
		{name: "两者都无则平台自营", keyMerchant: invalid, accountMerchant: invalid, want: platformMerchantID},
		{name: "密钥绑定为零时跳过", keyMerchant: valid(0), accountMerchant: valid(5), want: 5},
		{name: "账户默认为零时归平台", keyMerchant: invalid, accountMerchant: valid(0), want: platformMerchantID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveMerchantID(tt.keyMerchant, tt.accountMerchant); got != tt.want {
				t.Errorf("生效商家 = %d，期望 %d", got, tt.want)
			}
		})
	}
}

func TestRouteCandidates(t *testing.T) {
	fake := &recordedQuery{rows: [][]any{
		{uint64(11), "openai_chat", "https://up.example.com/api/v3", "group-a", 200, 200, 10, 4, []byte(`{"headers":{"x":"y"}}`), "glm-5", []byte(`{"temperature":0.2}`)},
		{uint64(12), "openai_chat", "https://up.example.com", "group-b", 100, 100, 0, 0, nil, "glm-4", nil},
	}}

	got, err := routeCandidates(context.Background(), fake, ChannelTypeOpenAIChat, "alias", 7)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if fake.query != routeCandidatesSQL {
		t.Errorf("SQL = %q，期望 %q", fake.query, routeCandidatesSQL)
	}
	wantArgs := []any{ChannelTypeOpenAIChat, uint64(7), "alias"}
	if !reflect.DeepEqual(fake.args, wantArgs) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, wantArgs)
	}
	if !strings.Contains(fake.query, "ORDER BY c.priority DESC") {
		t.Errorf("SQL 缺少优先级降序：%s", fake.query)
	}
	want := []RouteCandidate{
		{
			ChannelID: 11, ChannelType: ChannelTypeOpenAIChat,
			BaseURL: "https://up.example.com/api/v3", CredGroup: "group-a",
			Priority: 200, Weight: 200, RateLimitQPS: 10, RateLimitConcurrency: 4,
			Config: []byte(`{"headers":{"x":"y"}}`), UpstreamModel: "glm-5",
			RequestOverrides: []byte(`{"temperature":0.2}`),
		},
		{
			ChannelID: 12, ChannelType: ChannelTypeOpenAIChat,
			BaseURL: "https://up.example.com", CredGroup: "group-b",
			Priority: 100, Weight: 100, UpstreamModel: "glm-4",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("结果 = %#v，期望 %#v", got, want)
	}
}

func TestRouteCandidatesRejectsBadInput(t *testing.T) {
	tests := []struct {
		name        string
		channelType ChannelType
		model       string
		merchantID  uint64
	}{
		{name: "未知协议方言", channelType: ChannelType("openai_embeddings"), model: "m", merchantID: 1},
		{name: "模型为空", channelType: ChannelTypeOpenAIChat, model: "  ", merchantID: 1},
		{name: "商家为零", channelType: ChannelTypeOpenAIChat, model: "m", merchantID: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &recordedQuery{}
			if _, err := routeCandidates(context.Background(), fake, tt.channelType, tt.model, tt.merchantID); err == nil {
				t.Fatal("应当被拒绝")
			}
			if fake.calls != 0 {
				t.Errorf("校验失败不应触达驱动，实际调用 %d 次", fake.calls)
			}
		})
	}
}

// TestRouteCandidatesAnyType 覆盖不限协议候选查询：SQL 不得出现 type 谓词，
// 并如实给出每行的协议方言。
func TestRouteCandidatesAnyType(t *testing.T) {
	fake := &recordedQuery{rows: [][]any{
		{uint64(21), "anthropic_messages", "https://claude.example.com", "group-c", 300, 100, 0, 0, nil, "claude-sonnet", nil},
		{uint64(22), "openai_chat", "https://up.example.com", "group-a", 200, 100, 5, 2, nil, "glm-5", nil},
	}}

	got, err := routeCandidatesAnyType(context.Background(), fake, "alias", 7)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if fake.query != routeCandidatesAnyTypeSQL {
		t.Errorf("SQL = %q，期望 %q", fake.query, routeCandidatesAnyTypeSQL)
	}
	if strings.Contains(fake.query, "c.type = ?") {
		t.Errorf("不限协议查询不应限定 type：%s", fake.query)
	}
	wantArgs := []any{uint64(7), "alias"}
	if !reflect.DeepEqual(fake.args, wantArgs) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, wantArgs)
	}
	want := []RouteCandidate{
		{
			ChannelID: 21, ChannelType: ChannelTypeAnthropicMessages,
			BaseURL: "https://claude.example.com", CredGroup: "group-c",
			Priority: 300, Weight: 100, UpstreamModel: "claude-sonnet",
		},
		{
			ChannelID: 22, ChannelType: ChannelTypeOpenAIChat,
			BaseURL: "https://up.example.com", CredGroup: "group-a",
			Priority: 200, Weight: 100, RateLimitQPS: 5, RateLimitConcurrency: 2, UpstreamModel: "glm-5",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("结果 = %#v，期望 %#v", got, want)
	}
}

// TestRouteCandidatesAnyTypeRejectsBadInput 守护不限协议查询同样校验模型与商家。
func TestRouteCandidatesAnyTypeRejectsBadInput(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		merchantID uint64
	}{
		{name: "模型为空", model: "  ", merchantID: 1},
		{name: "商家为零", model: "m", merchantID: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &recordedQuery{}
			if _, err := routeCandidatesAnyType(context.Background(), fake, tt.model, tt.merchantID); err == nil {
				t.Fatal("应当被拒绝")
			}
			if fake.calls != 0 {
				t.Errorf("校验失败不应触达驱动，实际调用 %d 次", fake.calls)
			}
		})
	}
}

// TestRouteCandidatesAnyTypeKeepsUnknownChannelType 守护读方向的宽容：
// 库表出现本版本不认识的协议方言时原样带出，由上层跳过该行而不是让整批查询失败。
func TestRouteCandidatesAnyTypeKeepsUnknownChannelType(t *testing.T) {
	fake := &recordedQuery{rows: [][]any{
		{uint64(31), "gemini_generate", "https://gemini.example.com", "group-d", 100, 100, 0, 0, nil, "gemini-2", nil},
	}}

	got, err := routeCandidatesAnyType(context.Background(), fake, "alias", 1)
	if err != nil {
		t.Fatalf("未知方言不应让整批查询失败：%v", err)
	}
	if len(got) != 1 || got[0].ChannelType != ChannelType("gemini_generate") {
		t.Fatalf("未知方言应原样保留，实际 %#v", got)
	}
}

func TestRouteCandidatesEmptyIsNoop(t *testing.T) {
	fake := &recordedQuery{}
	got, err := routeCandidates(context.Background(), fake, ChannelTypeOpenAIChat, "m", 1)
	if err != nil {
		t.Fatalf("无候选不应报错：%v", err)
	}
	if got != nil {
		t.Errorf("无候选应返回 nil，实际 %#v", got)
	}
}

func TestRouteCandidatesWrapsDriverError(t *testing.T) {
	fake := &recordedQuery{err: errExecFailure}
	_, err := routeCandidates(context.Background(), fake, ChannelTypeOpenAIChat, "m", 1)
	if !errors.Is(err, errExecFailure) {
		t.Errorf("驱动错误应被包装可辨识，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "upstream_channel") {
		t.Errorf("错误信息应含表名，得到 %v", err)
	}
}

func TestCredentialsByGroup(t *testing.T) {
	fake := &recordedQuery{rows: [][]any{
		{uint64(11), "primary", []byte(`{"api_key":"sk-1"}`)},
		{uint64(12), "backup", []byte(`{"api_key":"sk-2"}`)},
	}}

	got, err := credentialsByGroup(context.Background(), fake, "group-a", 7)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if fake.query != credentialsByGroupSQL {
		t.Errorf("SQL = %q，期望 %q", fake.query, credentialsByGroupSQL)
	}
	wantArgs := []any{uint64(7), "group-a"}
	if !reflect.DeepEqual(fake.args, wantArgs) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, wantArgs)
	}
	want := []Credential{
		{ID: 11, Name: "primary", Secret: []byte(`{"api_key":"sk-1"}`)},
		{ID: 12, Name: "backup", Secret: []byte(`{"api_key":"sk-2"}`)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("结果 = %#v，期望 %#v", got, want)
	}
	if !strings.Contains(fake.query, "enabled = 1") {
		t.Errorf("SQL 缺少启用过滤：%s", fake.query)
	}
}

func TestCredentialsByGroupRejectsBadInput(t *testing.T) {
	tests := []struct {
		name       string
		credGroup  string
		merchantID uint64
	}{
		{name: "分组为空", credGroup: "  ", merchantID: 1},
		{name: "商家为零", credGroup: "g", merchantID: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &recordedQuery{}
			if _, err := credentialsByGroup(context.Background(), fake, tt.credGroup, tt.merchantID); err == nil {
				t.Fatal("应当被拒绝")
			}
			if fake.calls != 0 {
				t.Errorf("校验失败不应触达驱动，实际调用 %d 次", fake.calls)
			}
		})
	}
}

func TestCredentialsByGroupWrapsDriverError(t *testing.T) {
	fake := &recordedQuery{err: errExecFailure}
	_, err := credentialsByGroup(context.Background(), fake, "g", 1)
	if !errors.Is(err, errExecFailure) {
		t.Errorf("驱动错误应被包装可辨识，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "upstream_credential") {
		t.Errorf("错误信息应含表名，得到 %v", err)
	}
}
