//go:build e2e

// 自助查询端点的端到端验证：在既有运营剧本之后，用一把新密钥验证
// 「调用前已用量 0 → 调用后已用量增长」，并与库内账本对账。
//
// 单独成文件而不塞进主剧本：主剧本的步骤按运营路径组织，本步骤是新端点的附加验证，
// 只在主剧本末尾追加一行调用。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/access"
	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/me"
)

// e2eMeAccount 是自助查询响应里的账户摘要。
type e2eMeAccount struct {
	ID   uint64 `json:"id"`
	Code string `json:"code"`
}

// e2eMeBucket 是自助查询响应里的一条可用包。
type e2eMeBucket struct {
	Unit      string     `json:"unit"`
	Remaining string     `json:"remaining"`
	Fallback  string     `json:"fallback"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// e2eMeQuota 是自助查询响应里的一条限额行。
type e2eMeQuota struct {
	Scope      string     `json:"scope"`
	Metric     string     `json:"metric"`
	WindowKind string     `json:"window_kind"`
	Period     string     `json:"period"`
	Used       string     `json:"used"`
	Limit      string     `json:"limit"`
	Action     string     `json:"action"`
	ResetsAt   *time.Time `json:"resets_at"`
}

// e2eMeRecent 是自助查询响应里的一条流水摘要。
type e2eMeRecent struct {
	Model         string    `json:"model"`
	CreatedAt     time.Time `json:"created_at"`
	ChargedAmount string    `json:"charged_amount"`
}

// e2eMeSummary 是自助查询响应的解码视图。
type e2eMeSummary struct {
	Account e2eMeAccount  `json:"account"`
	Buckets []e2eMeBucket `json:"buckets"`
	Quotas  []e2eMeQuota  `json:"quotas"`
	Recent  []e2eMeRecent `json:"recent"`
}

// e2eGet 向网关发一次带密钥的 GET 并读完响应体。
func e2eGet(t *testing.T, endpoint, key string) e2eHTTPResult {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
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

// e2eFetchSummary 请求自助查询端点并解码；非 200 直接失败。
func (j *e2eJourney) e2eFetchSummary(t *testing.T, key, query string) e2eMeSummary {
	t.Helper()
	result := e2eGet(t, j.gatewayURL+me.AccountPath+query, key)
	if result.status != http.StatusOK {
		t.Fatalf("自助查询状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	var summary e2eMeSummary
	if err := json.Unmarshal(result.body, &summary); err != nil {
		t.Fatalf("自助查询响应不是预期 JSON：%v，原文 %s", err, result.body)
	}
	return summary
}

// e2eFindMeBucket 按 unit 在自助查询结果里找一条可用包；找不到即失败。
func e2eFindMeBucket(t *testing.T, summary e2eMeSummary, unit string) e2eMeBucket {
	t.Helper()
	for _, bucket := range summary.Buckets {
		if bucket.Unit == unit {
			return bucket
		}
	}
	t.Fatalf("自助查询结果里没有 unit=%s 的可用包：%+v", unit, summary.Buckets)
	return e2eMeBucket{}
}

// step8MeAccount 覆盖运营路径附加步骤：自助查询端点。
func (j *e2eJourney) step8MeAccount(t *testing.T) {
	ctx := j.ctx

	// 401 与数据面同口径：不带 Authorization 头。
	unauthorized := e2eGet(t, j.gatewayURL+me.AccountPath, "")
	if unauthorized.status != http.StatusUnauthorized {
		t.Fatalf("无凭据自助查询状态码 = %d，期望 401，响应体 %s", unauthorized.status, unauthorized.body)
	}
	if code := e2eErrorCode(t, unauthorized.body); code != string(domain.CodeUnauthorized) {
		t.Fatalf("无凭据自助查询错误码 = %q，期望 %q", code, domain.CodeUnauthorized)
	}

	// 新密钥 + 新限额：使「已用随调用增长」的起点确定，且不干扰此前步骤的用量口径。
	key, err := j.svc.IssueKey(ctx, admin.IssueKeyInput{
		AccountID: j.accountID, MerchantID: &j.merchantID, Name: "e2e-me",
	})
	e2eMust(t, err)
	const meQuotaLimit = "1000"
	_, err = j.svc.CreateQuota(ctx, admin.QuotaInput{
		Scope:       billing.ScopeAPIKey,
		ScopeID:     key.ID,
		Metric:      billing.MetricInputToken,
		WindowKind:  billing.WindowKindCalendar,
		Period:      billing.PeriodDay,
		LimitAmount: meQuotaLimit,
		Action:      billing.ActionReject,
	})
	e2eMust(t, err)

	before := j.e2eFetchSummary(t, key.Plaintext, "")
	if before.Account.ID != j.accountID || before.Account.Code != "e2e-account" {
		t.Fatalf("账户摘要 = %+v，期望 id=%d code=e2e-account", before.Account, j.accountID)
	}
	if len(before.Quotas) != 1 {
		t.Fatalf("限额条数 = %d，期望 1", len(before.Quotas))
	}
	if !e2eDecimalEqual(before.Quotas[0].Used, "0") {
		t.Fatalf("调用前已用量 = %s，期望 0", before.Quotas[0].Used)
	}
	if before.Quotas[0].ResetsAt == nil {
		t.Fatal("限额重置时间不应为空")
	}

	// 一次真实转发，用量进入窗口与账本。
	call := e2ePost(t, j.gatewayURL+domain.ProtocolOpenAIChat.EndpointPath(), key.Plaintext,
		e2eRequest(domain.ProtocolOpenAIChat, e2eBasicModel, false))
	if call.status != http.StatusOK {
		t.Fatalf("限额内转发状态码 = %d，期望 200，响应体 %s", call.status, call.body)
	}
	j.settledRequests++

	after := j.e2eFetchSummary(t, key.Plaintext, "?recent=1")
	if len(after.Quotas) != 1 {
		t.Fatalf("调用后限额条数 = %d，期望 1", len(after.Quotas))
	}
	if !e2eDecimalEqual(after.Quotas[0].Used, fmt.Sprint(e2eInputTokens)) {
		t.Fatalf("调用后已用量 = %s，期望 %d", after.Quotas[0].Used, e2eInputTokens)
	}
	if after.Quotas[0].WindowKind != string(billing.WindowKindCalendar) || after.Quotas[0].Period != string(billing.PeriodDay) {
		t.Errorf("限额窗口 = %s/%s，期望 calendar/day", after.Quotas[0].WindowKind, after.Quotas[0].Period)
	}
	if after.Quotas[0].ResetsAt == nil {
		t.Error("调用后限额重置时间不应为空")
	}
	if len(after.Recent) != 1 {
		t.Fatalf("最近流水条数 = %d，期望 1（recent=1）", len(after.Recent))
	}
	if after.Recent[0].Model != e2eBasicUpstreamModel {
		t.Errorf("最近流水模型 = %q，期望 %q", after.Recent[0].Model, e2eBasicUpstreamModel)
	}
	if !e2eDecimalEqual(after.Recent[0].ChargedAmount, e2eTokensPerRequest) {
		t.Errorf("最近流水付费金额 = %s，期望 %s", after.Recent[0].ChargedAmount, e2eTokensPerRequest)
	}

	// buckets 数值与库内一致。
	token := e2eFindMeBucket(t, after, string(billing.UnitSettleToken))
	var dbRemaining string
	if err := j.st.DB().QueryRowContext(ctx,
		"SELECT remaining FROM account_bucket WHERE id = ?", j.tokenBucket).Scan(&dbRemaining); err != nil {
		t.Fatalf("查询 token 账本失败：%v", err)
	}
	if !e2eDecimalEqual(token.Remaining, dbRemaining) {
		t.Fatalf("自助查询 token 余量 = %s，与库内 %s 不一致", token.Remaining, dbRemaining)
	}
	currency := e2eFindMeBucket(t, after, string(billing.UnitSettleCurrency))
	if !e2eDecimalEqual(currency.Remaining, e2eRechargeAmount) {
		t.Fatalf("自助查询货币余量 = %s，期望 %s（本次扣费落在 token 单位）", currency.Remaining, e2eRechargeAmount)
	}

	// 响应不含敏感字段。
	raw := e2eGet(t, j.gatewayURL+me.AccountPath, key.Plaintext)
	for _, forbidden := range []string{"merchant", "secret", "api_key_id", "key_hash", "key_prefix", "channel"} {
		if strings.Contains(string(raw.body), forbidden) {
			t.Errorf("响应出现敏感字段 %q：%s", forbidden, raw.body)
		}
	}
}
