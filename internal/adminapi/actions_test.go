package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/domain"
	"github.com/sumwai/tokenmp/internal/identity"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件覆盖管理面配置动作：路由分派、字段级校验、业务入参透传、错误码翻译
// 与「响应不回显凭据明文」。业务校验本身在 internal/admin，不在这里重复。

// invalidInput 复刻 internal/admin 输入错误的形状：消息原样面向操作者，
// errors.Is 命中 ErrInvalidInput。处理器只依赖这两条性质。
type invalidInput struct{ message string }

// Error 返回原样文案。
func (e *invalidInput) Error() string { return e.message }

// Is 让 errors.Is(err, admin.ErrInvalidInput) 命中。
func (e *invalidInput) Is(target error) bool { return target == admin.ErrInvalidInput }

// missingRow 复刻 internal/admin 目标缺失错误的形状。
type missingRow struct{ message string }

// Error 返回原样文案。
func (e *missingRow) Error() string { return e.message }

// Is 让 errors.Is(err, admin.ErrNotFound) 命中。
func (e *missingRow) Is(target error) bool { return target == admin.ErrNotFound }

// fakeWriter 记录动作调用与入参，并回放固定错误。
type fakeWriter struct {
	calls []string
	err   error
	id    uint64

	lastChannelID    uint64
	lastCredentialID uint64
	lastModelMapID   uint64
	lastMerchantID   uint64
	lastUserID       uint64

	lastMerchantCode string
	lastMerchantName string
	lastMerchantKind store.MerchantKind

	lastChannel    admin.ChannelInput
	lastCredential admin.CredentialInput
	lastModelMap   admin.ModelMapInput
}

// record 记下一次动作调用。
func (f *fakeWriter) record(name string) { f.calls = append(f.calls, name) }

// called 报告某动作是否被调用过。
func (f *fakeWriter) called(name string) bool {
	for _, call := range f.calls {
		if call == name {
			return true
		}
	}
	return false
}

func (f *fakeWriter) CreateMerchant(_ context.Context, code, name string, kind store.MerchantKind) (uint64, error) {
	f.record("CreateMerchant")
	f.lastMerchantCode, f.lastMerchantName, f.lastMerchantKind = code, name, kind
	return f.id, f.err
}

func (f *fakeWriter) UpdateMerchant(_ context.Context, id uint64, code, name string, kind store.MerchantKind) error {
	f.record("UpdateMerchant")
	f.lastMerchantID = id
	f.lastMerchantCode, f.lastMerchantName, f.lastMerchantKind = code, name, kind
	return f.err
}

func (f *fakeWriter) EnableMerchant(_ context.Context, id uint64) error {
	f.record("EnableMerchant")
	f.lastMerchantID = id
	return f.err
}

func (f *fakeWriter) DisableMerchant(_ context.Context, id uint64) error {
	f.record("DisableMerchant")
	f.lastMerchantID = id
	return f.err
}

func (f *fakeWriter) SetMerchantOwner(_ context.Context, id, userID uint64) error {
	f.record("SetMerchantOwner")
	f.lastMerchantID, f.lastUserID = id, userID
	return f.err
}

func (f *fakeWriter) CreateChannel(_ context.Context, in admin.ChannelInput) (uint64, error) {
	f.record("CreateChannel")
	f.lastChannel = in
	return f.id, f.err
}

func (f *fakeWriter) UpdateChannel(_ context.Context, id uint64, in admin.ChannelInput) error {
	f.record("UpdateChannel")
	f.lastChannelID, f.lastChannel = id, in
	return f.err
}

func (f *fakeWriter) EnableChannel(_ context.Context, id uint64) error {
	f.record("EnableChannel")
	f.lastChannelID = id
	return f.err
}

func (f *fakeWriter) DisableChannel(_ context.Context, id uint64) error {
	f.record("DisableChannel")
	f.lastChannelID = id
	return f.err
}

func (f *fakeWriter) AddCredential(_ context.Context, in admin.CredentialInput) (uint64, error) {
	f.record("AddCredential")
	f.lastCredential = in
	return f.id, f.err
}

func (f *fakeWriter) UpdateCredential(_ context.Context, id uint64, in admin.CredentialInput) error {
	f.record("UpdateCredential")
	f.lastCredentialID, f.lastCredential = id, in
	return f.err
}

func (f *fakeWriter) EnableCredential(_ context.Context, id uint64) error {
	f.record("EnableCredential")
	f.lastCredentialID = id
	return f.err
}

func (f *fakeWriter) DisableCredential(_ context.Context, id uint64) error {
	f.record("DisableCredential")
	f.lastCredentialID = id
	return f.err
}

func (f *fakeWriter) SetModelMap(_ context.Context, in admin.ModelMapInput) (uint64, error) {
	f.record("SetModelMap")
	f.lastModelMap = in
	return f.id, f.err
}

func (f *fakeWriter) UpdateModelMap(_ context.Context, id uint64, in admin.ModelMapInput) error {
	f.record("UpdateModelMap")
	f.lastModelMapID, f.lastModelMap = id, in
	return f.err
}

func (f *fakeWriter) DisableModelMap(_ context.Context, id uint64) error {
	f.record("DisableModelMap")
	f.lastModelMapID = id
	return f.err
}

// newWriteEnv 构造「管理员 + 配置动作替身」的测试环境。
func newWriteEnv() (*Handler, *fakeSessions, *fakeLister, *fakeWriter) {
	session := &fakeSessions{userID: 7, roles: identity.Roles(store.RoleAdmin)}
	lister := &fakeLister{}
	writer := &fakeWriter{id: 42}
	return NewHandler(Options{Sessions: session, Lister: lister, Writer: writer}), session, lister, writer
}

// doJSON 发一次带请求体的请求，返回状态码与解出的信封字段。
func doJSON(t *testing.T, h *Handler, method, path, token, body string) (int, map[string]json.RawMessage) {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var env map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析信封 %q: %v", rec.Body.String(), err)
	}
	return rec.Code, env
}

// TestCreateMerchant 断言新建商家的入参透传与主键回显。
func TestCreateMerchant(t *testing.T) {
	h, _, _, writer := newWriteEnv()
	status, env := doJSON(t, h, http.MethodPost, MerchantsPath, "token",
		`{"code":"acme","name":"示例","kind":"partner"}`)
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if !writer.called("CreateMerchant") {
		t.Fatal("未触达业务动作")
	}
	if writer.lastMerchantCode != "acme" || writer.lastMerchantName != "示例" ||
		writer.lastMerchantKind != store.MerchantKindPartner {
		t.Fatalf("入参 = %q/%q/%q", writer.lastMerchantCode, writer.lastMerchantName, writer.lastMerchantKind)
	}
	if idOf(t, env) != 42 {
		t.Fatalf("data.id = %d，期望 42", idOf(t, env))
	}
}

// TestCreateMerchantRejectsBadShape 断言形状校验在触达业务层之前拦下请求。
func TestCreateMerchantRejectsBadShape(t *testing.T) {
	cases := map[string]string{
		"缺编码":     `{"name":"n","kind":"partner"}`,
		"类型非法":    `{"code":"c","name":"n","kind":"reseller"}`,
		"未知字段":    `{"code":"c","name":"n","kind":"partner","status":"active"}`,
		"不是对象":    `[]`,
		"不是 JSON": `{`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, _, writer := newWriteEnv()
			status, env := doJSON(t, h, http.MethodPost, MerchantsPath, "token", body)
			if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
				t.Fatalf("应 400: %d %s", status, env)
			}
			if writer.called("CreateMerchant") {
				t.Error("校验失败不应触达业务动作")
			}
		})
	}
}

// TestCreateChannelPassesBusinessInput 断言新建渠道的字段完整透传。
//
// priority / weight 为 0 时由业务层取默认值 100：本层不替业务层做默认值，
// 否则两处默认值会漂移。
func TestCreateChannelPassesBusinessInput(t *testing.T) {
	h, _, _, writer := newWriteEnv()
	body := `{"merchant_id":2,"name":"主渠道","vendor":"openai","type":"openai_chat",` +
		`"cred_group":"g","base_url":"https://api.openai.com","credential_style":"query",` +
		`"config":{"headers":{"X-Env":"prod"}},"priority":5}`
	status, env := doJSON(t, h, http.MethodPost, ChannelsPath, "token", body)
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	got := writer.lastChannel
	if got.MerchantID != 2 || got.Name != "主渠道" || got.Vendor != "openai" ||
		got.Type != store.ChannelType("openai_chat") || got.CredGroup != "g" ||
		got.BaseURL != "https://api.openai.com" || got.Priority != 5 || got.Weight != 0 {
		t.Fatalf("入参 = %+v", got)
	}
	if got.CredentialStyle != domain.CredentialHeaderQuery {
		t.Fatalf("凭据注入形态 = %q", got.CredentialStyle)
	}
	if got.Config != `{"headers":{"X-Env":"prod"}}` {
		t.Fatalf("config 原文 = %q", got.Config)
	}
}

// TestOptionalJSONFieldsAcceptNull 断言可选对象字段显式传 null 与省略等价。
//
// 客户端把没填的值序列化成 null 是常见形态，把它当成配错只会留下无从解释的 400。
func TestOptionalJSONFieldsAcceptNull(t *testing.T) {
	h, _, _, writer := newWriteEnv()
	status, _ := doJSON(t, h, http.MethodPost, ChannelsPath, "token",
		`{"merchant_id":2,"name":"c","type":"openai_chat","cred_group":"g","base_url":"https://u","config":null}`)
	if status != http.StatusOK {
		t.Fatalf("config 为 null 应 200: %d", status)
	}
	if writer.lastChannel.Config != "" {
		t.Fatalf("config = %q，期望空", writer.lastChannel.Config)
	}

	status, _ = doJSON(t, h, http.MethodPut, ModelMapsPath, "token",
		`{"channel_id":3,"model":"m","upstream_model":"u","request_overrides":null}`)
	if status != http.StatusOK {
		t.Fatalf("overrides 为 null 应 200: %d", status)
	}
	if len(writer.lastModelMap.RequestOverrides) != 0 {
		t.Fatalf("overrides = %s，期望空", writer.lastModelMap.RequestOverrides)
	}
}

// TestCreateChannelRejectsBadShape 断言渠道的形状与枚举校验。
func TestCreateChannelRejectsBadShape(t *testing.T) {
	const valid = `{"merchant_id":2,"name":"c","type":"openai_chat","cred_group":"g","base_url":"https://u"}`
	cases := map[string]string{
		"缺商家":    `{"name":"c","type":"openai_chat","cred_group":"g","base_url":"https://u"}`,
		"协议非法":   `{"merchant_id":2,"name":"c","type":"openai","cred_group":"g","base_url":"https://u"}`,
		"缺凭据分组":  `{"merchant_id":2,"name":"c","type":"openai_chat","base_url":"https://u"}`,
		"缺上游地址":  `{"merchant_id":2,"name":"c","type":"openai_chat","cred_group":"g"}`,
		"注入形态非法": `{"merchant_id":2,"name":"c","type":"openai_chat","cred_group":"g","base_url":"https://u","credential_style":"cookie"}`,
		"优先级为负":  `{"merchant_id":2,"name":"c","type":"openai_chat","cred_group":"g","base_url":"https://u","priority":-1}`,
		"未知字段":   `{"merchant_id":2,"name":"c","type":"openai_chat","cred_group":"g","base_url":"https://u","enabled":true}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, _, writer := newWriteEnv()
			status, _ := doJSON(t, h, http.MethodPost, ChannelsPath, "token", body)
			if status != http.StatusBadRequest {
				t.Fatalf("应 400: %d", status)
			}
			if writer.called("CreateChannel") {
				t.Error("校验失败不应触达业务动作")
			}
		})
	}

	h, _, _, writer := newWriteEnv()
	if status, _ := doJSON(t, h, http.MethodPost, ChannelsPath, "token", valid); status != http.StatusOK {
		t.Fatalf("合法请求应 200: %d", status)
	}
	if !writer.called("CreateChannel") {
		t.Error("合法请求未触达业务动作")
	}
}

// TestCreateCredentialDoesNotEchoSecret 断言凭据明文只进不出：入参进业务层，
// 响应数据与文案都不得回显。
func TestCreateCredentialDoesNotEchoSecret(t *testing.T) {
	//nolint:gosec // G101：用例里的假凭据明文，不是真实凭据。
	const secret = "sk-live-do-not-echo"
	h, _, _, writer := newWriteEnv()
	body := `{"merchant_id":2,"cred_group":"g","name":"n","api_key":"` + secret + `"}`
	status, env := doJSON(t, h, http.MethodPost, CredentialsPath, "token", body)
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if writer.lastCredential.APIKey != secret || writer.lastCredential.Group != "g" {
		t.Fatalf("入参 = %+v", writer.lastCredential)
	}
	if idOf(t, env) != 42 {
		t.Fatalf("data.id = %d，期望 42", idOf(t, env))
	}
	if strings.Contains(string(env["data"]), secret) {
		t.Fatalf("响应数据回显了凭据明文: %s", env["data"])
	}
	if strings.Contains(string(env["message"]), secret) {
		t.Fatalf("响应文案回显了凭据明文: %s", env["message"])
	}
}

// TestSetModelMapIsPut 断言模型映射只接受 PUT，入参含缺省倍率原样透传。
func TestSetModelMapIsPut(t *testing.T) {
	h, _, _, writer := newWriteEnv()
	body := `{"channel_id":3,"model":"gpt-4o","upstream_model":"gpt-4o-2024","request_overrides":{"temperature":0}}`
	status, env := doJSON(t, h, http.MethodPut, ModelMapsPath, "token", body)
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("PUT 应 200: %d %s", status, env)
	}
	got := writer.lastModelMap
	if got.ChannelID != 3 || got.Model != "gpt-4o" || got.UpstreamModel != "gpt-4o-2024" || got.PriceMultiplier != "" {
		t.Fatalf("入参 = %+v", got)
	}
	if string(got.RequestOverrides) != `{"temperature":0}` {
		t.Fatalf("request_overrides = %s", got.RequestOverrides)
	}
	// 写入幂等，不回行 id：data 为 null。
	if string(env["data"]) != "null" {
		t.Fatalf("data = %s，期望 null", env["data"])
	}

	if status, env := doJSON(t, h, http.MethodPost, ModelMapsPath, "token", body); status != http.StatusBadRequest {
		t.Fatalf("POST 模型映射应 400: %d %s", status, env)
	}
}

// TestActionRoutes 断言各启停动作路由到对应业务方法，且主键原样传入。
func TestActionRoutes(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		want  string
		gotID func(*fakeWriter) uint64
	}{
		{"启用渠道", ChannelsPath + "/7/enable", "EnableChannel", func(f *fakeWriter) uint64 { return f.lastChannelID }},
		{"停用渠道", ChannelsPath + "/7/disable", "DisableChannel", func(f *fakeWriter) uint64 { return f.lastChannelID }},
		{"启用凭据", CredentialsPath + "/7/enable", "EnableCredential", func(f *fakeWriter) uint64 { return f.lastCredentialID }},
		{"停用凭据", CredentialsPath + "/7/disable", "DisableCredential", func(f *fakeWriter) uint64 { return f.lastCredentialID }},
		{"停用模型映射", ModelMapsPath + "/7/disable", "DisableModelMap", func(f *fakeWriter) uint64 { return f.lastModelMapID }},
		{"启用商家", MerchantsPath + "/7/enable", "EnableMerchant", func(f *fakeWriter) uint64 { return f.lastMerchantID }},
		{"停用商家", MerchantsPath + "/7/disable", "DisableMerchant", func(f *fakeWriter) uint64 { return f.lastMerchantID }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, writer := newWriteEnv()
			status, env := doJSON(t, h, http.MethodPost, tc.path, "token", "")
			if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
				t.Fatalf("应 200: %d %s", status, env)
			}
			if !writer.called(tc.want) {
				t.Fatalf("未调用 %s，调用序列 %v", tc.want, writer.calls)
			}
			if got := tc.gotID(writer); got != 7 {
				t.Fatalf("主键 = %d，期望 7", got)
			}
		})
	}
}

// TestActionRouteShape 断言动作路径的形状要求：未知尾段与非法主键回 404，
// 非 POST 回 400，模型映射没有启用动作。
func TestActionRouteShape(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"未知尾段", http.MethodPost, ChannelsPath + "/7/remove", http.StatusNotFound},
		{"主键为零", http.MethodPost, ChannelsPath + "/0/enable", http.StatusNotFound},
		{"主键非数字", http.MethodPost, ChannelsPath + "/abc/enable", http.StatusNotFound},
		{"条目路径用错方法", http.MethodPost, ChannelsPath + "/7", http.StatusBadRequest},
		{"非 POST", http.MethodGet, ChannelsPath + "/7/enable", http.StatusBadRequest},
		{"模型映射无启用", http.MethodPost, ModelMapsPath + "/7/enable", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, writer := newWriteEnv()
			status, _ := doJSON(t, h, tc.method, tc.path, "token", "")
			if status != tc.want {
				t.Fatalf("状态码 = %d，期望 %d", status, tc.want)
			}
			if len(writer.calls) != 0 {
				t.Fatalf("未匹配的动作不应触达业务层：%v", writer.calls)
			}
		})
	}
}

// TestSetMerchantOwner 断言归属绑定透传商家与登录主体两个主键，且零值被拦下。
func TestSetMerchantOwner(t *testing.T) {
	h, _, _, writer := newWriteEnv()
	status, env := doJSON(t, h, http.MethodPost, MerchantsPath+"/7/owner", "token", `{"user_id":9}`)
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if !writer.called("SetMerchantOwner") || writer.lastMerchantID != 7 || writer.lastUserID != 9 {
		t.Fatalf("入参 = 商家 %d / 主体 %d", writer.lastMerchantID, writer.lastUserID)
	}

	h, _, _, writer = newWriteEnv()
	status, _ = doJSON(t, h, http.MethodPost, MerchantsPath+"/7/owner", "token", `{"user_id":0}`)
	if status != http.StatusBadRequest {
		t.Fatalf("user_id 为 0 应 400: %d", status)
	}
	if writer.called("SetMerchantOwner") {
		t.Error("校验失败不应触达业务动作")
	}
}

// TestActionErrorMapping 断言业务错误翻成页面信封：输入不合法回 400 且文案去掉
// 包名前缀，唯一键冲突回 409，其余回 500 且不外漏底层文案。
func TestActionErrorMapping(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    int
		wantMessage string
	}{
		{
			name:        "输入不合法",
			err:         &invalidInput{message: "admin: 渠道 config 必须是 JSON 对象"},
			wantStatus:  http.StatusBadRequest,
			wantCode:    webapi.CodeBadRequest,
			wantMessage: "渠道 config 必须是 JSON 对象",
		},
		{
			name:        "唯一键冲突",
			err:         store.ErrConflict,
			wantStatus:  http.StatusConflict,
			wantCode:    webapi.CodeConflict,
			wantMessage: "同名记录已存在",
		},
		{
			name:       "服务端错误",
			err:        errors.New("store: 插入 upstream_channel 失败: table doesn't exist"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   webapi.CodeInternal,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, writer := newWriteEnv()
			writer.err = tc.err
			status, env := doJSON(t, h, http.MethodPost, ChannelsPath, "token",
				`{"merchant_id":2,"name":"c","type":"openai_chat","cred_group":"g","base_url":"https://u"}`)
			if status != tc.wantStatus || codeOf(t, env) != tc.wantCode {
				t.Fatalf("状态 = %d/%d，期望 %d/%d", status, codeOf(t, env), tc.wantStatus, tc.wantCode)
			}
			var message string
			if err := json.Unmarshal(env["message"], &message); err != nil {
				t.Fatalf("解析 message: %v", err)
			}
			if tc.wantMessage != "" && message != tc.wantMessage {
				t.Fatalf("文案 = %q，期望 %q", message, tc.wantMessage)
			}
			if strings.Contains(message, "table doesn't exist") || strings.Contains(message, "admin:") {
				t.Fatalf("文案泄漏了内部细节: %q", message)
			}
		})
	}
}

// TestWriteRoutesAbsentWithoutWriter 断言未挂载写入面时写端点像没声明过一样回 404，
// 只读清单不受影响。
func TestWriteRoutesAbsentWithoutWriter(t *testing.T) {
	h, _, _ := newEnv()
	writes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, MerchantsPath, `{"code":"c","name":"n","kind":"partner"}`},
		{http.MethodPost, ChannelsPath, `{}`},
		{http.MethodPost, ChannelsPath + "/7/enable", ""},
		{http.MethodPut, ChannelsPath + "/7", `{}`},
		{http.MethodPost, CredentialsPath, `{}`},
		{http.MethodPut, CredentialsPath + "/7", `{}`},
		{http.MethodPut, MerchantsPath + "/7", `{}`},
		{http.MethodPut, ModelMapsPath, `{}`},
	}
	for _, tc := range writes {
		status, env := doJSON(t, h, tc.method, tc.path, "token", tc.body)
		if status != http.StatusNotFound || codeOf(t, env) != webapi.CodeNotFound {
			t.Errorf("%s %s 应 404: %d %s", tc.method, tc.path, status, env)
		}
	}
	if status, _ := do(t, h, http.MethodGet, MerchantsPath, "token"); status != http.StatusOK {
		t.Errorf("清单读取不应受写入面缺失影响: %d", status)
	}
}

// TestWriteRequiresAdmin 断言配置动作同样要求管理面身份。
func TestWriteRequiresAdmin(t *testing.T) {
	h, session, _, writer := newWriteEnv()
	session.roles = identity.Roles(store.RoleMember)
	status, env := doJSON(t, h, http.MethodPost, ChannelsPath, "token",
		`{"merchant_id":2,"name":"c","type":"openai_chat","cred_group":"g","base_url":"https://u"}`)
	if status != http.StatusForbidden || codeOf(t, env) != webapi.CodeForbidden {
		t.Fatalf("应 403: %d %s", status, env)
	}
	if len(writer.calls) != 0 {
		t.Fatalf("越权请求不应触达业务层：%v", writer.calls)
	}
}

// TestUpdateChannelIsPutOnItem 断言渠道的修改是条目路径上的 PUT，入参完整透传。
func TestUpdateChannelIsPutOnItem(t *testing.T) {
	h, _, _, writer := newWriteEnv()
	body := `{"merchant_id":2,"name":"改过的名字","vendor":"openai","type":"openai_responses",` +
		`"cred_group":"g","base_url":"https://u2","priority":3,"weight":4}`
	status, env := doJSON(t, h, http.MethodPut, ChannelsPath+"/7", "token", body)
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if !writer.called("UpdateChannel") || writer.lastChannelID != 7 {
		t.Fatalf("动作 = %v，主键 = %d", writer.calls, writer.lastChannelID)
	}
	got := writer.lastChannel
	if got.Name != "改过的名字" || got.Type != store.ChannelType("openai_responses") ||
		got.Priority != 3 || got.Weight != 4 || got.BaseURL != "https://u2" {
		t.Fatalf("入参 = %+v", got)
	}
	// 修改不回行 id：页面重新取清单即见最新取值。
	if string(env["data"]) != "null" {
		t.Fatalf("data = %s，期望 null", env["data"])
	}
	// 条目路径只接受 PUT。
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if status, _ := doJSON(t, h, method, ChannelsPath+"/7", "token", body); status != http.StatusBadRequest {
			t.Fatalf("%s 条目路径应 400，得到 %d", method, status)
		}
	}
}

// TestUpdateMerchant 断言商家修改透传三个字段。
func TestUpdateMerchant(t *testing.T) {
	h, _, _, writer := newWriteEnv()
	status, env := doJSON(t, h, http.MethodPut, MerchantsPath+"/7", "token",
		`{"code":"acme-2","name":"改名","kind":"platform"}`)
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if !writer.called("UpdateMerchant") || writer.lastMerchantID != 7 {
		t.Fatalf("动作 = %v，主键 = %d", writer.calls, writer.lastMerchantID)
	}
	if writer.lastMerchantCode != "acme-2" || writer.lastMerchantKind != store.MerchantKindPlatform {
		t.Fatalf("入参 = %s/%s", writer.lastMerchantCode, writer.lastMerchantKind)
	}
}

// TestUpdateCredentialWithoutKeyKeepsSecret 断言修改凭据时省略 api-key 表示不轮换。
func TestUpdateCredentialWithoutKeyKeepsSecret(t *testing.T) {
	h, _, _, writer := newWriteEnv()
	status, env := doJSON(t, h, http.MethodPut, CredentialsPath+"/7", "token",
		`{"merchant_id":2,"cred_group":"g2","name":"改名"}`)
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if !writer.called("UpdateCredential") || writer.lastCredentialID != 7 {
		t.Fatalf("动作 = %v，主键 = %d", writer.calls, writer.lastCredentialID)
	}
	if writer.lastCredential.APIKey != "" || writer.lastCredential.Group != "g2" {
		t.Fatalf("入参 = %+v", writer.lastCredential)
	}
}

// TestUpdateModelMapIsPutOnItem 断言模型映射的条目路径 PUT 能改模型名与归属渠道。
func TestUpdateModelMapIsPutOnItem(t *testing.T) {
	h, _, _, writer := newWriteEnv()
	status, env := doJSON(t, h, http.MethodPut, ModelMapsPath+"/7", "token",
		`{"channel_id":3,"model":"改过的别名","upstream_model":"u2","price_multiplier":"1.5"}`)
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if !writer.called("UpdateModelMap") || writer.lastModelMapID != 7 {
		t.Fatalf("动作 = %v，主键 = %d", writer.calls, writer.lastModelMapID)
	}
	got := writer.lastModelMap
	if got.Model != "改过的别名" || got.UpstreamModel != "u2" || got.PriceMultiplier != "1.5" {
		t.Fatalf("入参 = %+v", got)
	}
	if string(env["data"]) != "null" {
		t.Fatalf("data = %s，期望 null", env["data"])
	}
	if status, _ := doJSON(t, h, http.MethodPost, ModelMapsPath+"/7", "token", `{}`); status != http.StatusBadRequest {
		t.Fatalf("条目路径用 POST 应 400，得到 %d", status)
	}
}

// TestCreateCredentialRequiresKey 断言新增路径仍然要求 api-key：
// 省略只对修改有意义，新增一行没有 secret 的凭据是无法使用的。
func TestCreateCredentialRequiresKey(t *testing.T) {
	h, _, _, writer := newWriteEnv()
	status, _ := doJSON(t, h, http.MethodPost, CredentialsPath, "token",
		`{"merchant_id":2,"cred_group":"g","name":"n"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("应 400: %d", status)
	}
	if writer.called("AddCredential") {
		t.Error("校验失败不应触达业务动作")
	}
}

// TestMissingTargetMapsTo404 断言目标缺失回 404 且文案去掉包名前缀。
func TestMissingTargetMapsTo404(t *testing.T) {
	h, _, _, writer := newWriteEnv()
	writer.err = &missingRow{message: "admin: 渠道 7 不存在"}
	status, env := doJSON(t, h, http.MethodPut, ChannelsPath+"/7", "token",
		`{"merchant_id":2,"name":"c","type":"openai_chat","cred_group":"g","base_url":"https://u"}`)
	if status != http.StatusNotFound || codeOf(t, env) != webapi.CodeNotFound {
		t.Fatalf("应 404: %d %s", status, env)
	}
	var message string
	if err := json.Unmarshal(env["message"], &message); err != nil {
		t.Fatalf("解析 message: %v", err)
	}
	if message != "渠道 7 不存在" {
		t.Fatalf("文案 = %q", message)
	}
}

// idOf 读信封里 data.id。
func idOf(t *testing.T, env map[string]json.RawMessage) uint64 {
	t.Helper()
	var data struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	return data.ID
}
