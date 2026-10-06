package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/me"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件覆盖自助查询端点接到网关后的可观察行为：与数据面同一鉴权口径的 401、
// 成功响应是 JSON、方法与非法的 recent 参数。

// Account 返回自助查询要用的账户行；id 原样回填，使端到端断言的归属可核对。
func (f *fakeGatewayStore) Account(_ context.Context, id uint64) (*store.Account, error) {
	return &store.Account{ID: id, Code: "acct"}, nil
}

// RecentUsage 默认不回流水；需要断言最近流水的用例走 internal/me 的单测。
func (f *fakeGatewayStore) RecentUsage(_ context.Context, _ uint64, limit int) ([]store.UsageListRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	return nil, nil
}

// doGet 向网关发一次 GET，返回结果快照。
func doGet(t *testing.T, url, authorization string) httpResult {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	if authorization != "" {
		req.Header.Set(authorizationHeader, authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("发送请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败：%v", err)
	}
	return httpResult{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), headers: resp.Header.Clone(), body: body}
}

// TestGatewayMeAccountUnauthorizedMatchesDataPlane 覆盖自助查询与数据面同一种 401：
// 缺头与未知密钥都回共享 JSON 错误体，错误码 unauthorized。
func TestGatewayMeAccountUnauthorizedMatchesDataPlane(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	tests := []struct {
		name          string
		authorization string
	}{
		{name: "缺少 Authorization 头"},
		{name: "非法方案", authorization: "Basic abc"},
		{name: "未知密钥", authorization: authSchemePrefix + "wrong-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := doGet(t, server.URL+me.AccountPath, tt.authorization)
			assertJSONError(t, result, http.StatusUnauthorized)
			var envelope errorEnvelope
			if err := json.Unmarshal(result.body, &envelope); err != nil {
				t.Fatalf("解析错误体失败：%v", err)
			}
			if envelope.Error.Code != string(domain.CodeUnauthorized) {
				t.Errorf("错误码 = %q，期望 %q", envelope.Error.Code, domain.CodeUnauthorized)
			}
		})
	}
}

// TestGatewayMeAccountReturnsSummary 覆盖带凭据的 200：响应为 JSON，账户与可用包口径正确。
func TestGatewayMeAccountReturnsSummary(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	result := doGet(t, server.URL+me.AccountPath, authSchemePrefix+testAPIKey)
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体 %s", result.status, result.body)
	}
	if !strings.HasPrefix(result.contentType, jsonContentType) {
		t.Errorf("Content-Type = %q，期望 %q 前缀", result.contentType, jsonContentType)
	}
	var summary struct {
		Account struct {
			ID   uint64 `json:"id"`
			Code string `json:"code"`
		} `json:"account"`
		Buckets []struct {
			Unit      string `json:"unit"`
			Remaining string `json:"remaining"`
			Fallback  string `json:"fallback"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(result.body, &summary); err != nil {
		t.Fatalf("响应不是预期 JSON：%v，原文 %s", err, result.body)
	}
	if summary.Account.ID != activeAuth().AccountID || summary.Account.Code != "acct" {
		t.Errorf("account = %+v，期望 id=%d code=acct", summary.Account, activeAuth().AccountID)
	}
	if len(summary.Buckets) != 1 {
		t.Fatalf("buckets 条数 = %d，期望 1（假存储的默认可透支货币账本）", len(summary.Buckets))
	}
	if got := summary.Buckets[0]; got.Unit != "currency" || got.Remaining != "1000" || got.Fallback != "charge_balance" {
		t.Errorf("bucket = %+v，期望 currency/1000/charge_balance", got)
	}
}

// TestGatewayMeAccountMethodNotAllowed 覆盖非 GET 方法回 JSON 405。
func TestGatewayMeAccountMethodNotAllowed(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	result := doPost(t, server.URL+me.AccountPath, authSchemePrefix+testAPIKey, "{}")
	assertJSONError(t, result, http.StatusMethodNotAllowed)
}

// TestGatewayMeAccountRejectsInvalidRecent 覆盖非法的 recent 参数回 JSON 400。
func TestGatewayMeAccountRejectsInvalidRecent(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	result := doGet(t, server.URL+me.AccountPath+"?recent=abc", authSchemePrefix+testAPIKey)
	assertJSONError(t, result, http.StatusBadRequest)
}
