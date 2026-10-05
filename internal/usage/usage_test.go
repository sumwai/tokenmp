package usage

import (
	"context"
	"testing"

	"github.com/sumwai/tokenmp/internal/access"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/store"
)

// fakeStore 记录落库的占位流水行，供记账用例断言。
type fakeStore struct {
	rows      []store.UsageRow
	insertErr error
}

// InsertUsage 追加一行流水；insertErr 非 nil 时返回它。
func (f *fakeStore) InsertUsage(_ context.Context, row store.UsageRow) (uint64, error) {
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	f.rows = append(f.rows, row)
	return uint64(len(f.rows)), nil
}

// TestRecorderDropsUnmappedComponent 验证记账入口对没有计价口径的分量记日志、不落库。
func TestRecorderDropsUnmappedComponent(t *testing.T) {
	st := &fakeStore{}
	var logged []string
	recorder := NewRecorder(st, nil, 0, func(msg string, args ...any) {
		logged = append(logged, msg)
	})
	ctx := access.WithIdentity(context.Background(), access.Identity{AccountID: 2, MerchantID: 1})
	err := recorder.RecordUsage(ctx, domain.UsageRecord{
		RequestID: "req-1",
		ChannelID: 10,
		Model:     "up-model",
		Usage: domain.Usage{
			Source:         domain.UsageSourceUpstream,
			InputTokens:    5,
			ServerToolUses: 2,
		},
	})
	if err != nil {
		t.Fatalf("记账失败：%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("应记一条丢弃日志，得到 %v", logged)
	}
	if len(st.rows) != 1 {
		t.Fatalf("流水行数 = %d，期望 1", len(st.rows))
	}
	if _, ok := st.rows[0].Usage["server_tool_uses"]; ok {
		t.Errorf("无法映射的分量不应落库，得到 %#v", st.rows[0].Usage)
	}
	if st.rows[0].Usage[billing.MetricInputToken] != 5 {
		t.Errorf("input_token = %d，期望 5", st.rows[0].Usage[billing.MetricInputToken])
	}
}

// TestRecorderLogsCacheWriteConflict 验证两套缓存写口径同时非零时记日志、以分档为准落库。
func TestRecorderLogsCacheWriteConflict(t *testing.T) {
	st := &fakeStore{}
	var logged []string
	recorder := NewRecorder(st, nil, 0, func(msg string, args ...any) {
		logged = append(logged, msg)
	})
	ctx := access.WithIdentity(context.Background(), access.Identity{AccountID: 2, MerchantID: 1})
	err := recorder.RecordUsage(ctx, domain.UsageRecord{
		RequestID: "req-1",
		ChannelID: 10,
		Model:     "up-model",
		Usage: domain.Usage{
			Source:             domain.UsageSourceUpstream,
			InputTokens:        100,
			CacheWriteTokens:   7,
			CacheWrite5mTokens: 6,
		},
	})
	if err != nil {
		t.Fatalf("记账失败：%v", err)
	}
	if len(logged) != 1 || logged[0] != "缓存写口径冲突，已按分档为准" {
		t.Fatalf("应记一条口径冲突日志，得到 %v", logged)
	}
	if len(st.rows) != 1 {
		t.Fatalf("流水行数 = %d，期望 1", len(st.rows))
	}
	if st.rows[0].Usage[billing.MetricCacheWrite5m] != 6 {
		t.Errorf("cache_write_5m = %d，期望 6", st.rows[0].Usage[billing.MetricCacheWrite5m])
	}
	if _, ok := st.rows[0].Usage[billing.MetricCacheWriteToken]; ok {
		t.Errorf("冲突时不应落不分档分量，得到 %#v", st.rows[0].Usage)
	}
}

// TestRecorderRequiresIdentity 验证未鉴权上下文不写脏流水。
func TestRecorderRequiresIdentity(t *testing.T) {
	st := &fakeStore{}
	recorder := NewRecorder(st, nil, 0, func(string, ...any) {})
	if err := recorder.RecordUsage(context.Background(), domain.UsageRecord{ChannelID: 10, Model: "m"}); err == nil {
		t.Fatal("缺少鉴权上下文应报错")
	}
	if len(st.rows) != 0 {
		t.Errorf("不应留下流水，得到 %#v", st.rows)
	}
}
