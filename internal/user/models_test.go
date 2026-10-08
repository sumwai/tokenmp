package user

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件覆盖模型目录：作用域推导、协议归拢与错误分支。
// 数据访问用替身，全链不经数据库。

// TestModelsList 断言模型与协议原样透出，且作用域来自会话。
func TestModelsList(t *testing.T) {
	e := newTestEnv()
	e.store.models = []store.AccountModel{
		{Name: "gpt-x", Protocols: []string{"openai_chat", "openai_responses"}},
		{Name: "claude-y", Protocols: []string{"anthropic_messages"}},
	}
	status, env := e.do(t, http.MethodGet, ModelsPath, "token", "")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if e.store.modelsAccountID != 42 {
		t.Errorf("账户 = %d，期望会话推导出的 42", e.store.modelsAccountID)
	}
	var data struct {
		Items []modelInfoView `json:"items"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	want := []modelInfoView{
		{Name: "gpt-x", Protocols: []string{"openai_chat", "openai_responses"}},
		{Name: "claude-y", Protocols: []string{"anthropic_messages"}},
	}
	if !reflect.DeepEqual(data.Items, want) {
		t.Errorf("items = %+v，期望 %+v", data.Items, want)
	}
	// 目录不分页，但分页三字段照填「一页含全部」，前端与其余列表共用一条渲染路径。
	var total int
	if err := json.Unmarshal(env["total"], &total); err != nil {
		t.Fatalf("解析 total: %v", err)
	}
	if total != len(want) {
		t.Errorf("total = %d，期望 %d", total, len(want))
	}
}

// TestModelsEmpty 断言没有可用模型时回空数组而不是 null。
func TestModelsEmpty(t *testing.T) {
	e := newTestEnv()
	status, env := e.do(t, http.MethodGet, ModelsPath, "token", "")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	var data struct {
		Items []modelInfoView `json:"items"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	if data.Items == nil {
		t.Errorf("items 应为空数组，得到 null：%s", env["data"])
	}
}

// TestModelsErrors 断言存储失败回 500、未登录回 401、非 GET 回 400。
func TestModelsErrors(t *testing.T) {
	e := newTestEnv()
	e.store.modelsErr = errors.New("数据库不可达")
	status, env := e.do(t, http.MethodGet, ModelsPath, "token", "")
	if status != http.StatusInternalServerError || codeOf(t, env) != webapi.CodeInternal {
		t.Fatalf("存储失败应 500: %d %s", status, env)
	}

	e = newTestEnv()
	status, env = e.do(t, http.MethodGet, ModelsPath, "", "")
	if status != http.StatusUnauthorized || codeOf(t, env) != webapi.CodeUnauthorized {
		t.Fatalf("缺少令牌应 401: %d %s", status, env)
	}

	e = newTestEnv()
	status, env = e.do(t, http.MethodPost, ModelsPath, "token", "")
	if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
		t.Fatalf("非 GET 应 400: %d %s", status, env)
	}
}
