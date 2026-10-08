package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sumwai/tokenmp/internal/user"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件覆盖用户级端点接到网关后的装配行为：子树已挂载、走页面信封、
// 未认证时落到页面信封而不是数据面错误体。

// TestGatewayUserAccountRequiresSession 断言缺少会话令牌回页面信封 401。
//
// 与自助查询端点形成对照：后者缺凭据回的是数据面 ErrorEnvelope，两者形状不同，
// 前端按各自的契约解析。
func TestGatewayUserAccountRequiresSession(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	result := doGet(t, server.URL+user.AccountPath, "")
	if result.status != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401，响应体 %s", result.status, result.body)
	}
	var envelope webapi.Envelope
	if err := json.Unmarshal(result.body, &envelope); err != nil {
		t.Fatalf("响应不是页面信封：%v，原文 %s", err, result.body)
	}
	if envelope.Code != webapi.CodeUnauthorized {
		t.Errorf("code = %d，期望 %d（原文 %s）", envelope.Code, webapi.CodeUnauthorized, result.body)
	}
}

// TestGatewayUserUnknownPathReturnsEnvelope 断言子树内未声明的路径回页面信封 404。
//
// 路径分发先于凭据校验：未声明的路径不带令牌也回 404 而不是 401，
// 与认证面子树的行为一致。
func TestGatewayUserUnknownPathReturnsEnvelope(t *testing.T) {
	server := httptest.NewServer(newTestGateway(t, &fakeGatewayStore{auth: activeAuth()}).handler)
	defer server.Close()

	result := doGet(t, server.URL+user.PathPrefix+"nope", "")
	if result.status != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404，响应体 %s", result.status, result.body)
	}
	var envelope webapi.Envelope
	if err := json.Unmarshal(result.body, &envelope); err != nil {
		t.Fatalf("响应不是页面信封：%v，原文 %s", err, result.body)
	}
	if envelope.Code != webapi.CodeNotFound {
		t.Errorf("code = %d，期望 %d", envelope.Code, webapi.CodeNotFound)
	}
}
