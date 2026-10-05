package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件覆盖管理面新增的读写入口的参数组装与可读错误，不连数据库。

func TestDescribeWriteError(t *testing.T) {
	duplicate := &mysql.MySQLError{Number: mysqlDuplicateEntry, Message: "Duplicate entry"}
	err := describeWriteError("merchant", duplicate)
	if !strings.Contains(err.Error(), "唯一键冲突") {
		t.Errorf("唯一键冲突应给可读提示，得到 %v", err)
	}
	if !errors.Is(err, duplicate) {
		t.Errorf("原始错误应可追溯，得到 %v", err)
	}

	other := describeWriteError("merchant", errExecFailure)
	if strings.Contains(other.Error(), "唯一键冲突") {
		t.Errorf("非冲突错误不应报唯一键冲突：%v", other)
	}
}

func TestMerchantKindWhitelist(t *testing.T) {
	for _, kind := range []MerchantKind{MerchantKindPlatform, MerchantKindPartner} {
		if err := ValidateMerchantKind(kind); err != nil {
			t.Errorf("已知类型 %q 不应报错：%v", kind, err)
		}
	}
	if err := ValidateMerchantKind(MerchantKind("reseller")); err == nil {
		t.Error("未知商家类型应当被拒绝")
	}
	if got := MerchantKindFromDB("reseller"); got != MerchantKind("reseller") {
		t.Errorf("MerchantKindFromDB 应原样返回，得到 %q", got)
	}
}

func TestInsertMerchant(t *testing.T) {
	fake := &recordedExec{id: 7}
	id, err := insertMerchant(context.Background(), fake, Merchant{
		Code: "partner-1", Name: "入驻", Kind: MerchantKindPartner,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if id != 7 {
		t.Errorf("自增 id = %d，期望 7", id)
	}
	// 未显式给状态时取 active。
	want := []any{"partner-1", "入驻", MerchantKindPartner, StatusActive}
	if !reflect.DeepEqual(fake.args, want) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, want)
	}
}

func TestInsertMerchantRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		give Merchant
	}{
		{name: "code 为空", give: Merchant{Name: "n", Kind: MerchantKindPartner}},
		{name: "name 为空", give: Merchant{Code: "c", Kind: MerchantKindPartner}},
		{name: "未知 kind", give: Merchant{Code: "c", Name: "n", Kind: "reseller"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &recordedExec{}
			if _, err := insertMerchant(context.Background(), fake, tt.give); err == nil {
				t.Fatal("应当被拒绝")
			}
			if fake.calls != 0 {
				t.Errorf("校验失败不应触达驱动，实际 %d 次", fake.calls)
			}
		})
	}
}

func TestInsertMerchantDuplicateIsReadable(t *testing.T) {
	fake := &recordedExec{err: &mysql.MySQLError{Number: mysqlDuplicateEntry, Message: "Duplicate entry"}}
	_, err := insertMerchant(context.Background(), fake, Merchant{Code: "c", Name: "n", Kind: MerchantKindPartner})
	if err == nil || !strings.Contains(err.Error(), "唯一键冲突") {
		t.Fatalf("唯一键冲突应可读，得到 %v", err)
	}
}

func TestInsertChannelRejectsBadInput(t *testing.T) {
	fake := &recordedExec{}
	if _, err := insertChannel(context.Background(), fake, Channel{
		MerchantID: 1, Name: "n", Type: "gemini", CredGroup: "g", BaseURL: "u",
	}); err == nil {
		t.Fatal("未知协议应当被拒绝")
	}
	if fake.calls != 0 {
		t.Errorf("校验失败不应触达驱动，实际 %d 次", fake.calls)
	}
}

func TestUpsertModelMapSQLAndArgs(t *testing.T) {
	fake := &recordedExec{}
	if _, err := upsertModelMap(context.Background(), fake, ModelMap{
		ChannelID: 3, Model: "alias", UpstreamModel: "up", PriceMultiplier: "1.5",
		RequestOverrides: []byte(`{"temperature":0.2}`),
	}); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if !strings.Contains(fake.query, "ON DUPLICATE KEY UPDATE") {
		t.Errorf("set 语义应依赖 upsert：%s", fake.query)
	}
	want := []any{uint64(3), "alias", "up", "1.5", []byte(`{"temperature":0.2}`)}
	if !reflect.DeepEqual(fake.args, want) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, want)
	}

	if _, err := upsertModelMap(context.Background(), &recordedExec{}, ModelMap{
		ChannelID: 3, Model: "alias", UpstreamModel: "up", PriceMultiplier: "1",
		RequestOverrides: []byte(`not-json`),
	}); err == nil {
		t.Error("非法 overrides 应当被拒绝")
	}
}

func TestInsertBucketRejectsBadEnums(t *testing.T) {
	base := BucketRow{
		AccountID: 1, MerchantID: 1, Unit: billing.UnitSettleToken, Total: "10", Remaining: "10",
		Fallback: billing.FallbackReject, Source: billing.SourceGrant,
	}
	tests := []struct {
		name string
		give BucketRow
	}{
		{name: "未知单位", give: func() BucketRow { b := base; b.Unit = "usd"; return b }()},
		{name: "未知处置", give: func() BucketRow { b := base; b.Fallback = "ignore"; return b }()},
		{name: "未知来路", give: func() BucketRow { b := base; b.Source = "airdrop"; return b }()},
		{name: "账户为零", give: func() BucketRow { b := base; b.AccountID = 0; return b }()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &recordedExec{}
			if _, err := insertBucket(context.Background(), fake, tt.give); err == nil {
				t.Fatal("应当被拒绝")
			}
			if fake.calls != 0 {
				t.Errorf("校验失败不应触达驱动，实际 %d 次", fake.calls)
			}
		})
	}
}

func TestInsertAPIKeyArgs(t *testing.T) {
	expires := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	fake := &recordedExec{id: 4}
	if _, err := insertAPIKey(context.Background(), fake, APIKey{
		AccountID: 2, Name: "default", KeyHash: "hash", KeyPrefix: "sk-12345…", ExpiresAt: &expires,
	}); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	want := []any{uint64(2), nil, "default", "hash", "sk-12345…", expires}
	if !reflect.DeepEqual(fake.args, want) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, want)
	}
	if _, err := insertAPIKey(context.Background(), &recordedExec{}, APIKey{AccountID: 2, KeyPrefix: "p"}); err == nil {
		t.Error("空哈希应当被拒绝")
	}
}

func TestListMerchantsScan(t *testing.T) {
	created := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	fake := &recordedQuery{rows: [][]any{
		{uint64(1), "platform", "平台自营", "platform", StatusActive, scanTime{Time: created, Valid: true}},
	}}
	got, err := listMerchants(context.Background(), fake)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if fake.query != listMerchantsSQL {
		t.Errorf("SQL = %q，期望 %q", fake.query, listMerchantsSQL)
	}
	if len(got) != 1 || got[0].Code != "platform" || got[0].Kind != MerchantKindPlatform || !got[0].CreatedAt.Equal(created) {
		t.Errorf("结果不符：%+v", got)
	}
}

func TestListBucketsFilter(t *testing.T) {
	fake := &recordedQuery{}
	if _, err := listBuckets(context.Background(), fake, 0); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if strings.Contains(fake.query, "WHERE") {
		t.Errorf("account 为 0 时不应过滤：%s", fake.query)
	}

	fake = &recordedQuery{}
	if _, err := listBuckets(context.Background(), fake, 7); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if !strings.Contains(fake.query, "WHERE account_id = ?") {
		t.Errorf("应按账户过滤：%s", fake.query)
	}
	if !reflect.DeepEqual(fake.args, []any{uint64(7)}) {
		t.Errorf("参数 = %#v，期望 [7]", fake.args)
	}
}

func TestListBucketsScan(t *testing.T) {
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := &recordedQuery{rows: [][]any{
		{uint64(1), uint64(2), uint64(8), "token", "100", "40",
			scanTime{Time: expires, Valid: true}, "reject", "purchase", 100},
	}}
	got, err := listBuckets(context.Background(), fake, 2)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(got) != 1 {
		t.Fatalf("行数 = %d，期望 1", len(got))
	}
	b := got[0]
	if b.Unit != billing.UnitSettleToken || b.Fallback != billing.FallbackReject ||
		b.Source != billing.SourcePurchase || b.Remaining != "40" || b.ExpiresAt == nil {
		t.Errorf("结果不符：%+v", b)
	}
}

func TestListPricingFilters(t *testing.T) {
	fake := &recordedQuery{}
	if _, err := listPricing(context.Background(), fake, 0, ""); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if strings.Contains(fake.query, "WHERE") {
		t.Errorf("无过滤条件时不应拼 WHERE：%s", fake.query)
	}

	fake = &recordedQuery{}
	if _, err := listPricing(context.Background(), fake, 1, "gpt-x"); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if !strings.Contains(fake.query, "merchant_id = ?") || !strings.Contains(fake.query, "model = ?") {
		t.Errorf("应同时按商家与模型过滤：%s", fake.query)
	}
	if !reflect.DeepEqual(fake.args, []any{uint64(1), "gpt-x"}) {
		t.Errorf("参数 = %#v", fake.args)
	}
}

func TestListPricingScan(t *testing.T) {
	effective := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	retired := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	fake := &recordedQuery{rows: [][]any{
		{uint64(1), uint64(1), "gpt-x", 1, scanTime{Time: effective, Valid: true}, scanTime{Time: retired, Valid: true}},
		{uint64(2), uint64(1), "gpt-x", 2, scanTime{Time: retired, Valid: true}, nil},
	}}
	got, err := listPricing(context.Background(), fake, 1, "gpt-x")
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(got) != 2 || got[0].RetiredAt == nil || got[1].RetiredAt != nil {
		t.Errorf("结果不符：%+v", got)
	}
}

func TestListUsageFilters(t *testing.T) {
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fake := &recordedQuery{}
	if _, err := listUsage(context.Background(), fake, 3, since); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if !strings.Contains(fake.query, "account_id = ?") || !strings.Contains(fake.query, "created_at >= ?") {
		t.Errorf("应同时按账户与时间过滤：%s", fake.query)
	}
	if !reflect.DeepEqual(fake.args, []any{uint64(3), since}) {
		t.Errorf("参数 = %#v", fake.args)
	}
}

func TestPublishPricingValidatesBeforeTx(t *testing.T) {
	s := &Store{}
	at := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	if _, err := s.PublishPricing(context.Background(), 0, "m", at, nil); err == nil {
		t.Error("商家为 0 应当被拒绝")
	}
	if _, err := s.PublishPricing(context.Background(), 1, "  ", at, nil); err == nil {
		t.Error("模型为空应当被拒绝")
	}
	if _, err := s.PublishPricing(context.Background(), 1, "m", time.Time{}, nil); err == nil {
		t.Error("生效时刻为零值应当被拒绝")
	}
}

func TestListCalendarDaysRejectsEmptyCalendar(t *testing.T) {
	if _, err := listCalendarDays(context.Background(), &recordedQuery{}, "  "); err == nil {
		t.Error("空日历名应当被拒绝")
	}
}
