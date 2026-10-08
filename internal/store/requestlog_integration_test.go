//go:build integration

// 请求记录的真实 MySQL 验证：迁移 0009 的三张表、两次写入的列合并、列表/详情/聚合
// 的过滤与作用域，以及终态未落库的行不可见。与其它集成验证同属「需要数据库」的一类。
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
)

func TestRequestLogIntegration(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过请求记录验证", envTestDSN)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := store.Open(ctx, store.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("关闭连接失败：%v", err)
		}
	})

	dropKnownTables(ctx, t, s.DB())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cleanupCancel()
		dropKnownTables(cleanupCtx, t, s.DB())
	})

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	for _, table := range []string{"request_log", "request_attempt", "request_stats_daily"} {
		if !tableExists(ctx, t, s.DB(), table) {
			t.Fatalf("迁移 0009 后表 %s 不存在", table)
		}
	}
	// 0009 幂等：重复迁移不应因表已存在而中断，也不应重复登记版本。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移失败：%v", err)
	}
	assertMigrationVersionCount(ctx, t, s.DB(), migrationCount(t))

	const (
		accountID  = 42
		otherAccID = 43
		merchantID = 1
		apiKeyID   = 9
	)
	now := time.Now().Truncate(time.Second)

	// 一次成功请求：尝试 → 用量 → 终态，三步落在同一行上。
	if err := s.InsertRequestAttempt(ctx, store.RequestAttempt{
		RequestID: "req-ok", Attempt: 1, Outcome: "ok", UpstreamStatus: 200,
		ClientProtocol: "openai_chat", UpstreamProtocol: "openai_chat",
		DurationMS: 40, RequestedModel: "client-model", UpstreamModel: "up-model",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("写尝试失败：%v", err)
	}
	if err := s.UpsertRequestUsage(ctx, store.RequestUsage{
		RequestID: "req-ok", MerchantID: merchantID, AccountID: accountID,
		APIKeyID: apiKeyID, CreatedAt: now,
		Usage: []byte(`{"source":"upstream","input_tokens":10,"output_tokens":4,"server_tool_uses":1}`),
	}); err != nil {
		t.Fatalf("写用量失败：%v", err)
	}
	if err := s.UpsertRequestOutcome(ctx, store.RequestOutcome{
		RequestID: "req-ok", MerchantID: merchantID, AccountID: accountID,
		APIKeyID: apiKeyID, CreatedAt: now.Add(time.Second), Status: "success", HTTPStatus: 200,
		UpstreamStatus: 200, DurationMS: 40, RequestedModel: "client-model", UpstreamModel: "up-model",
		Protocol: "openai_chat", UpstreamProtocol: "openai_chat",
		ClientIP: "203.0.113.7", UserAgent: "client/1.0", WrittenBytes: 128,
	}); err != nil {
		t.Fatalf("写终态失败：%v", err)
	}
	if err := s.UpsertRequestStatsDaily(ctx, accountID, now, "client-model", apiKeyID, "success"); err != nil {
		t.Fatalf("累加计数失败：%v", err)
	}

	// 反序写入：终态先到，用量后到。后到的写入只补自己的列，不覆盖终态。
	if err := s.UpsertRequestOutcome(ctx, store.RequestOutcome{
		RequestID: "req-late-usage", MerchantID: merchantID, AccountID: accountID,
		APIKeyID: 0, CreatedAt: now, Status: "failed", HTTPStatus: 502,
		FailureClass: "upstream", ErrorCode: "upstream_unavailable", RequestedModel: "client-model",
	}); err != nil {
		t.Fatalf("写终态失败：%v", err)
	}
	if err := s.UpsertRequestUsage(ctx, store.RequestUsage{
		RequestID: "req-late-usage", MerchantID: merchantID, AccountID: accountID,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("补写用量失败：%v", err)
	}
	if err := s.UpsertRequestStatsDaily(ctx, accountID, now, "client-model", 0, "failed"); err != nil {
		t.Fatalf("累加计数失败：%v", err)
	}

	// 只有归属与用量、没有终态的行：表示这次请求中断在写终态之前。
	if err := s.UpsertRequestUsage(ctx, store.RequestUsage{
		RequestID: "req-incomplete", MerchantID: merchantID, AccountID: accountID,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("写用量失败：%v", err)
	}

	// 另一账户的记录：任何读路径都不该看到它。
	if err := s.UpsertRequestOutcome(ctx, store.RequestOutcome{
		RequestID: "req-other", MerchantID: merchantID, AccountID: otherAccID,
		CreatedAt: now, Status: "success", HTTPStatus: 200, RequestedModel: "client-model",
	}); err != nil {
		t.Fatalf("写终态失败：%v", err)
	}

	// 列表：只返回本账户且进入终态的行，未取得的上游侧取值读回零值。
	rows, total, err := s.ListRequestLogs(ctx, store.RequestLogFilter{AccountID: accountID, Limit: 20})
	if err != nil {
		t.Fatalf("列出请求记录失败：%v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("记录条数 = %d（total %d），期望 2", len(rows), total)
	}
	// 主键倒序：后写的在前。
	if rows[0].RequestID != "req-late-usage" || rows[1].RequestID != "req-ok" {
		t.Errorf("返回顺序 = %s / %s，期望 req-late-usage / req-ok", rows[0].RequestID, rows[1].RequestID)
	}
	late := rows[0]
	if late.FailureClass != "upstream" || late.ErrorCode != "upstream_unavailable" ||
		late.UpstreamStatus != 0 || late.Usage != nil {
		t.Errorf("终态先到的行 = %+v，与写入不符", late)
	}
	if late.CreatedAt.IsZero() {
		t.Errorf("请求时刻为空，期望写入时的取值")
	}

	// 详情：列表不读的报文列在详情里读到，未写的报文列回空。
	detail, err := s.RequestLogByRequestID(ctx, accountID, "req-ok")
	if err != nil {
		t.Fatalf("读请求详情失败：%v", err)
	}
	if detail.APIKeyID != apiKeyID || detail.Status != "success" || detail.HTTPStatus != 200 {
		t.Errorf("详情归属/终态 = %+v，与写入不符", detail)
	}
	if !jsonEqual(detail.Usage, []byte(`{"source":"upstream","input_tokens":10,"output_tokens":4,"server_tool_uses":1}`)) {
		t.Errorf("用量 = %s，与写入不符", detail.Usage)
	}
	if detail.PayloadAvailable {
		t.Errorf("未落报文时 payload_available 应为假，得到真")
	}
	if len(detail.RequestShape) != 0 || len(detail.ErrorResponseShape) != 0 {
		t.Errorf("未落报文时三列应为空，得到 %s / %s", detail.RequestShape, detail.ErrorResponseShape)
	}

	// 作用域：不属于本账户的标识按不存在处理。
	if _, err := s.RequestLogByRequestID(ctx, otherAccID, "req-ok"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("跨账户读应回 sql.ErrNoRows，得到 %v", err)
	}
	if _, err := s.RequestLogByRequestID(ctx, accountID, "req-incomplete"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("未进入终态的记录不应可读，得到 %v", err)
	}

	// 尝试时间线：按序号升序读回，两侧协议与改写标注原样保留。
	if err := s.InsertRequestAttempt(ctx, store.RequestAttempt{
		RequestID: "req-ok", Attempt: 2, Outcome: "failed", UpstreamStatus: 429,
		FailureClass: "rate_limit", ErrorCode: "upstream_rate_limited", CrossProtocol: true,
		ClientProtocol: "openai_chat", UpstreamProtocol: "anthropic_messages", DurationMS: 30,
		RewrittenParts: []byte(`["request_model"]`), CreatedAt: now.Add(-time.Second),
	}); err != nil {
		t.Fatalf("写尝试失败：%v", err)
	}
	attempts, err := s.RequestAttempts(ctx, "req-ok")
	if err != nil {
		t.Fatalf("读尝试时间线失败：%v", err)
	}
	if len(attempts) != 2 || attempts[0].Attempt != 1 || attempts[1].Attempt != 2 {
		t.Fatalf("尝试时间线 = %+v，期望按序号升序的两条", attempts)
	}
	if attempts[1].FailureClass != "rate_limit" || !attempts[1].CrossProtocol ||
		attempts[1].UpstreamProtocol != "anthropic_messages" {
		t.Errorf("第二条尝试 = %+v，与写入不符", attempts[1])
	}
	if !jsonEqual(attempts[1].RewrittenParts, []byte(`["request_model"]`)) {
		t.Errorf("改写标注 = %s，与写入不符", attempts[1].RewrittenParts)
	}
	// 同一 (请求, 序号) 重复上报按覆盖处理。
	if err := s.InsertRequestAttempt(ctx, store.RequestAttempt{
		RequestID: "req-ok", Attempt: 2, Outcome: "failed", ErrorCode: "upstream_rejected",
		CreatedAt: now.Add(-time.Second),
	}); err != nil {
		t.Fatalf("重复写尝试失败：%v", err)
	}
	attempts, err = s.RequestAttempts(ctx, "req-ok")
	if err != nil {
		t.Fatalf("读尝试时间线失败：%v", err)
	}
	if len(attempts) != 2 || attempts[1].ErrorCode != "upstream_rejected" {
		t.Errorf("重复上报应按覆盖处理，得到 %+v", attempts)
	}

	// 最后一次尝试：终态写入靠它补齐上游侧事实。
	last, err := s.LastRequestAttempt(ctx, "req-ok")
	if err != nil {
		t.Fatalf("读最后一次尝试失败：%v", err)
	}
	if last.Attempt != 2 || last.UpstreamStatus != 0 || last.FailureClass != "" {
		t.Errorf("最后一次尝试 = %+v，应为覆盖后的那一行", last)
	}
	if _, err := s.LastRequestAttempt(ctx, "req-none"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("无尝试应回 sql.ErrNoRows，得到 %v", err)
	}

	// 过滤：模型、密钥、终态与时刻区间。
	filtered, filteredTotal, err := s.ListRequestLogs(ctx, store.RequestLogFilter{
		AccountID: accountID, Status: "failed", RequestedModel: "client-model", Limit: 20,
	})
	if err != nil {
		t.Fatalf("过滤查询失败：%v", err)
	}
	if filteredTotal != 1 || len(filtered) != 1 || filtered[0].RequestID != "req-late-usage" {
		t.Errorf("按终态过滤 = %+v（total %d），期望只剩 req-late-usage", filtered, filteredTotal)
	}
	byKey, _, err := s.ListRequestLogs(ctx, store.RequestLogFilter{
		AccountID: accountID, APIKeyID: apiKeyID, Limit: 20,
	})
	if err != nil {
		t.Fatalf("按密钥过滤失败：%v", err)
	}
	if len(byKey) != 1 || byKey[0].RequestID != "req-ok" {
		t.Errorf("按密钥过滤 = %+v，期望只剩 req-ok", byKey)
	}
	// 区间按闭区间匹配：起点晚一毫秒即排除全部记录。
	none, noneTotal, err := s.ListRequestLogs(ctx, store.RequestLogFilter{
		AccountID: accountID, Since: now.Add(time.Millisecond), Limit: 20,
	})
	if err != nil {
		t.Fatalf("按时刻过滤失败：%v", err)
	}
	if noneTotal != 0 || len(none) != 0 {
		t.Errorf("起点晚于全部记录时应为空，得到 %+v（total %d）", none, noneTotal)
	}

	// 聚合：三个分组维度都从同一份按天缓存求和。
	byDay, err := s.RequestStats(ctx, store.RequestStatsQuery{AccountID: accountID, GroupBy: "day"})
	if err != nil {
		t.Fatalf("按天聚合失败：%v", err)
	}
	if len(byDay) != 1 || byDay[0].Total != 2 || byDay[0].Success != 1 || byDay[0].Failed != 1 {
		t.Errorf("按天聚合 = %+v，期望一天内两条（成功 1 失败 1）", byDay)
	}
	if want := now.Format("2006-01-02"); byDay[0].Key != want {
		t.Errorf("日期键 = %q，期望 %q", byDay[0].Key, want)
	}
	byModel, err := s.RequestStats(ctx, store.RequestStatsQuery{AccountID: accountID, GroupBy: "model"})
	if err != nil {
		t.Fatalf("按模型聚合失败：%v", err)
	}
	if len(byModel) != 1 || byModel[0].Key != "client-model" || byModel[0].Total != 2 {
		t.Errorf("按模型聚合 = %+v，期望一个模型两条", byModel)
	}
	byStatus, err := s.RequestStats(ctx, store.RequestStatsQuery{AccountID: accountID, GroupBy: "status"})
	if err != nil {
		t.Fatalf("按终态聚合失败：%v", err)
	}
	if len(byStatus) != 2 || byStatus[0].Key != "failed" || byStatus[1].Key != "success" {
		t.Errorf("按终态聚合 = %+v，期望 failed 与 success 各一条", byStatus)
	}
	// 密钥维度：只算 req-ok 那条。
	byKeyStats, err := s.RequestStats(ctx, store.RequestStatsQuery{
		AccountID: accountID, GroupBy: "status", APIKeyID: apiKeyID,
	})
	if err != nil {
		t.Fatalf("按密钥聚合失败：%v", err)
	}
	if len(byKeyStats) != 1 || byKeyStats[0].Key != "success" || byKeyStats[0].Total != 1 {
		t.Errorf("按密钥聚合 = %+v，期望只剩 success 一条", byKeyStats)
	}
	// 同一四维组合重复累加：命中唯一键时计数递增而不是新增一行。
	if err := s.UpsertRequestStatsDaily(ctx, accountID, now, "client-model", apiKeyID, "success"); err != nil {
		t.Fatalf("重复累加计数失败：%v", err)
	}
	repeatedDay, err := s.RequestStats(ctx, store.RequestStatsQuery{AccountID: accountID, GroupBy: "day"})
	if err != nil {
		t.Fatalf("按天聚合失败：%v", err)
	}
	if len(repeatedDay) != 1 || repeatedDay[0].Total != 3 || repeatedDay[0].Success != 2 {
		t.Errorf("重复累加后按天聚合 = %+v，期望总 3 条（成功 2 失败 1）", repeatedDay)
	}
	repeatedKey, err := s.RequestStats(ctx, store.RequestStatsQuery{
		AccountID: accountID, GroupBy: "status", APIKeyID: apiKeyID,
	})
	if err != nil {
		t.Fatalf("按密钥聚合失败：%v", err)
	}
	if len(repeatedKey) != 1 || repeatedKey[0].Total != 2 {
		t.Errorf("重复累加后按密钥聚合 = %+v，期望密钥维度 2 条", repeatedKey)
	}

	// 另一账户的聚合为空：作用域由账户收敛。
	otherStats, err := s.RequestStats(ctx, store.RequestStatsQuery{AccountID: otherAccID, GroupBy: "day"})
	if err != nil {
		t.Fatalf("另一账户聚合失败：%v", err)
	}
	if len(otherStats) != 0 {
		t.Errorf("另一账户未累加计数时应为空，得到 %+v", otherStats)
	}
}
