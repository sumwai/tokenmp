package partner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sumwai/tokenmp/internal/credential"
	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件覆盖商家域端点的可观察行为：会话与商家身份的推导、越权按 404 处理、
// 凭据明文只在创建响应出现一次、聚合查询的作用域与参数校验。全链不经数据库。

const testToken = "session-token"

// fakeSessions 是会话替身：返回固定主体，或按 wantErr 报错。
type fakeSessions struct {
	userID uint64
	roles  []string
	err    error
}

func (f *fakeSessions) SessionSubject(context.Context, string) (uint64, []string, error) {
	if f.err != nil {
		return 0, nil, f.err
	}
	return f.userID, f.roles, nil
}

// fakeStore 是数据面替身：按商家作用域返回注入的数据，并记录启停调用。
type fakeStore struct {
	merchant   *store.Merchant
	merchantID uint64
	channels   []store.Channel
	creds      []store.CredentialRow
	stats      []store.UsageStatsItem

	insertChannelErr error
	inserted         []store.Channel
	insertedCreds    []store.CredentialRow

	// lastStats 保存最后一次聚合查询，供作用域断言。
	lastStats store.MerchantUsageStatsQuery
	// enabledCalls 记录启停调用，形如 "channel:7:false"。
	enabledCalls []string
	// owned 是「本商家的行 id 集合」：不在其中的行按不存在处理。
	ownedChannels map[uint64]bool
	ownedCreds    map[uint64]bool
}

func (f *fakeStore) MerchantByOwner(context.Context, uint64) (*store.Merchant, error) {
	if f.merchant == nil {
		return nil, sql.ErrNoRows
	}
	return f.merchant, nil
}

func (f *fakeStore) ChannelsByMerchant(_ context.Context, merchantID uint64, _ *bool, limit, offset int) ([]store.Channel, int, error) {
	f.merchantID = merchantID
	total := len(f.channels)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return f.channels[offset:end], total, nil
}

func (f *fakeStore) CredentialsByMerchant(_ context.Context, merchantID uint64, limit, offset int) ([]store.CredentialRow, int, error) {
	f.merchantID = merchantID
	total := len(f.creds)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return f.creds[offset:end], total, nil
}

func (f *fakeStore) InsertChannel(_ context.Context, c store.Channel) (uint64, error) {
	if f.insertChannelErr != nil {
		return 0, f.insertChannelErr
	}
	f.inserted = append(f.inserted, c)
	return uint64(len(f.inserted)), nil
}

func (f *fakeStore) InsertCredential(_ context.Context, c store.CredentialRow) (uint64, error) {
	f.insertedCreds = append(f.insertedCreds, c)
	return uint64(len(f.insertedCreds)), nil
}

func (f *fakeStore) SetChannelEnabledForMerchant(_ context.Context, id, _ uint64, enabled bool) (bool, error) {
	f.enabledCalls = append(f.enabledCalls, channelCall(id, enabled))
	return f.ownedChannels[id], nil
}

func (f *fakeStore) SetCredentialEnabledForMerchant(_ context.Context, id, _ uint64, enabled bool) (bool, error) {
	f.enabledCalls = append(f.enabledCalls, channelCall(id, enabled))
	return f.ownedCreds[id], nil
}

func (f *fakeStore) MerchantUsageStats(_ context.Context, q store.MerchantUsageStatsQuery) ([]store.UsageStatsItem, error) {
	f.lastStats = q
	return f.stats, nil
}

// channelCall 拼一条启停调用的标识，避免用例里各处拼字符串。
func channelCall(id uint64, enabled bool) string {
	action := "disable"
	if enabled {
		action = "enable"
	}
	return action + ":" + strconv.FormatUint(id, 10)
}

// partnerSubject 是能通过商家身份与归属校验的主体。
func partnerSubject() (*fakeSessions, *fakeStore) {
	return &fakeSessions{userID: 7, roles: []string{store.RoleMember, store.RolePartner}},
		&fakeStore{
			merchant:      &store.Merchant{ID: 2, Code: "partner-1", Kind: store.MerchantKindPartner},
			ownedChannels: map[uint64]bool{7: true},
			ownedCreds:    map[uint64]bool{9: true},
		}
}

// do 发起一次请求并解出信封；token 为空时不带 Authorization 头。
func do(t *testing.T, h *Handler, method, path, body, token string) (int, map[string]json.RawMessage) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("响应不是 JSON 信封：%v（body=%s）", err, rec.Body.String())
	}
	for _, field := range []string{"code", "data", "message", "page", "size", "total"} {
		if _, ok := envelope[field]; !ok {
			t.Fatalf("信封缺少字段 %q：%s", field, rec.Body.String())
		}
	}
	var code int
	if err := json.Unmarshal(envelope["code"], &code); err != nil {
		t.Fatalf("code 不是整数：%v", err)
	}
	return code, envelope
}

// TestUnauthorizedWithoutSession 断言缺少令牌与令牌无效都回 401。
func TestUnauthorizedWithoutSession(t *testing.T) {
	sessions, st := partnerSubject()
	sessions.err = errors.New("invalid")
	h := NewHandler(Options{Sessions: sessions, Store: st})

	for _, path := range []string{ChannelsPath, CredentialsPath, UsageStatsPath} {
		if code, _ := do(t, h, http.MethodGet, path, "", ""); code != http.StatusUnauthorized {
			t.Errorf("%s 无令牌应回 401，得到 %d", path, code)
		}
		if code, _ := do(t, h, http.MethodGet, path, "", testToken); code != http.StatusUnauthorized {
			t.Errorf("%s 无效令牌应回 401，得到 %d", path, code)
		}
	}
}

// TestForbiddenWithoutPartnerIdentity 断言没有商家身份回 403：会话有效但主体没被授予
// 商家身份，下一步动作是等平台开通，不能折叠成 401。
func TestForbiddenWithoutPartnerIdentity(t *testing.T) {
	_, st := partnerSubject()
	h := NewHandler(Options{
		Sessions: &fakeSessions{userID: 7, roles: []string{store.RoleMember}},
		Store:    st,
	})
	if code, _ := do(t, h, http.MethodGet, ChannelsPath, "", testToken); code != http.StatusForbidden {
		t.Errorf("无商家身份应回 403，得到 %d", code)
	}
}

// TestForbiddenWithoutBoundMerchant 断言有商家身份但未绑定商家回 403。
func TestForbiddenWithoutBoundMerchant(t *testing.T) {
	sessions, _ := partnerSubject()
	h := NewHandler(Options{Sessions: sessions, Store: &fakeStore{}})
	if code, _ := do(t, h, http.MethodGet, ChannelsPath, "", testToken); code != http.StatusForbidden {
		t.Errorf("未绑定商家应回 403，得到 %d", code)
	}
}

// TestListChannelsScopedToSession 断言列表按会话推导出的商家取数，且分页三字段来自信封。
func TestListChannelsScopedToSession(t *testing.T) {
	sessions, st := partnerSubject()
	st.channels = []store.Channel{
		{ID: 3, MerchantID: 2, Name: "up-chat", Type: "openai_chat", CredGroup: "grp", BaseURL: "https://up.example", Enabled: true},
		{ID: 4, MerchantID: 2, Name: "up-session", Type: "anthropic_messages", CredGroup: "grp2", BaseURL: "https://up2.example", Enabled: false},
	}
	h := NewHandler(Options{Sessions: sessions, Store: st})

	code, env := do(t, h, http.MethodGet, ChannelsPath+"?page=1&size=20", "", testToken)
	if code != http.StatusOK {
		t.Fatalf("列表应回 200，得到 %d", code)
	}
	if st.merchantID != 2 {
		t.Errorf("作用域商家 = %d，期望会话推导出的 2", st.merchantID)
	}
	var data struct {
		Items []channelView `json:"items"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("data 不是 {items}：%v", err)
	}
	if len(data.Items) != 2 || data.Items[0].Name != "up-chat" {
		t.Errorf("渠道列表不符：%+v", data.Items)
	}
	if string(env["total"]) != "2" || string(env["page"]) != "1" {
		t.Errorf("分页字段不符：page=%s total=%s", env["page"], env["total"])
	}
}

// TestListChannelsRejectsBadPaging 断言非法分页回 400。
func TestListChannelsRejectsBadPaging(t *testing.T) {
	sessions, st := partnerSubject()
	h := NewHandler(Options{Sessions: sessions, Store: st})
	if code, _ := do(t, h, http.MethodGet, ChannelsPath+"?size=0", "", testToken); code != http.StatusBadRequest {
		t.Errorf("size=0 应回 400，得到 %d", code)
	}
}

// TestCreateChannelValidatesAndDefaults 断言登记渠道的必填校验与默认值：
// 优先级与权重未给时取列默认值 100，落库与回显一致。
func TestCreateChannelValidatesAndDefaults(t *testing.T) {
	sessions, st := partnerSubject()
	h := NewHandler(Options{Sessions: sessions, Store: st})

	body := `{"name":"up-chat","type":"openai_chat","cred_group":"grp","base_url":"https://up.example","credential_style":"x-api-key"}`
	code, _ := do(t, h, http.MethodPost, ChannelsPath, body, testToken)
	if code != http.StatusOK {
		t.Fatalf("登记应回 200，得到 %d", code)
	}
	if len(st.inserted) != 1 {
		t.Fatalf("应写入一条渠道，得到 %d", len(st.inserted))
	}
	got := st.inserted[0]
	if got.MerchantID != 2 {
		t.Errorf("归属商家 = %d，期望会话推导出的 2", got.MerchantID)
	}
	if got.Priority != 100 || got.Weight != 100 {
		t.Errorf("未给优先级与权重应取默认值 100，得到 %d/%d", got.Priority, got.Weight)
	}
	if string(got.Config) != `{"credential_style":"x-api-key"}` {
		t.Errorf("凭据注入形态未落进 config：%s", got.Config)
	}

	// 必填缺失回 400，且不写库。
	for _, bad := range []string{
		`{"type":"openai_chat","cred_group":"g","base_url":"https://x"}`,
		`{"name":"n","type":"nope","cred_group":"g","base_url":"https://x"}`,
		`{"name":"n","type":"openai_chat","cred_group":"g"}`,
		`{"name":"n","type":"openai_chat","cred_group":"g","base_url":"https://x","credential_style":"nope"}`,
		`{"name":"n","type":"openai_chat","cred_group":"g","base_url":"https://x","unknown":1}`,
	} {
		if code, _ := do(t, h, http.MethodPost, ChannelsPath, bad, testToken); code != http.StatusBadRequest {
			t.Errorf("非法请求体 %s 应回 400，得到 %d", bad, code)
		}
	}
	if len(st.inserted) != 1 {
		t.Errorf("非法请求不应写库，得到 %d 条", len(st.inserted))
	}
}

// TestCreateChannelConflictIs409 断言同名渠道回 409：唯一键冲突是可判定的。
func TestCreateChannelConflictIs409(t *testing.T) {
	sessions, st := partnerSubject()
	st.insertChannelErr = store.ErrConflict
	h := NewHandler(Options{Sessions: sessions, Store: st})

	body := `{"name":"up-chat","type":"openai_chat","cred_group":"grp","base_url":"https://up.example"}`
	if code, _ := do(t, h, http.MethodPost, ChannelsPath, body, testToken); code != http.StatusConflict {
		t.Errorf("唯一键冲突应回 409，得到 %d", code)
	}
}

// TestChannelDisableIsScoped 断言停用只作用于本商家的行：非本商家的行按 404 处理，
// 不以 403 区分存在性。
func TestChannelDisableIsScoped(t *testing.T) {
	sessions, st := partnerSubject()
	h := NewHandler(Options{Sessions: sessions, Store: st})

	if code, _ := do(t, h, http.MethodPost, ChannelsPath+"/7/disable", "", testToken); code != http.StatusOK {
		t.Errorf("停用本商家的渠道应回 200，得到 %d", code)
	}
	if len(st.enabledCalls) != 1 || st.enabledCalls[0] != "disable:7" {
		t.Errorf("启停调用不符：%v", st.enabledCalls)
	}
	if code, _ := do(t, h, http.MethodPost, ChannelsPath+"/8/disable", "", testToken); code != http.StatusNotFound {
		t.Errorf("停用他人渠道应回 404，得到 %d", code)
	}
	if code, _ := do(t, h, http.MethodPost, ChannelsPath+"/7/enable", "", testToken); code != http.StatusOK {
		t.Errorf("启用应回 200，得到 %d", code)
	}
	if code, _ := do(t, h, http.MethodPost, ChannelsPath+"/7", "", testToken); code != http.StatusNotFound {
		t.Errorf("缺少动作尾段的路径应回 404，得到 %d", code)
	}
}

// TestCreateCredentialReturnsSecretOnce 断言凭据明文只在创建响应出现，列表只有前缀。
func TestCreateCredentialReturnsSecretOnce(t *testing.T) {
	sessions, st := partnerSubject()
	h := NewHandler(Options{Sessions: sessions, Store: st})

	body := `{"cred_group":"grp","name":"primary","api_key":"sk-upstream-secret-1234"}`
	code, env := do(t, h, http.MethodPost, CredentialsPath, body, testToken)
	if code != http.StatusOK {
		t.Fatalf("登记凭据应回 200，得到 %d", code)
	}
	var created createdCredentialView
	if err := json.Unmarshal(env["data"], &created); err != nil {
		t.Fatalf("data 不是创建视图：%v", err)
	}
	if created.Secret != "sk-upstream-secret-1234" {
		t.Errorf("创建响应应回一次明文，得到 %q", created.Secret)
	}
	if created.Prefix == "" || strings.Contains(created.Prefix, "secret-1234") {
		t.Errorf("前缀不应泄露明文：%q", created.Prefix)
	}
	if len(st.insertedCreds) != 1 || !json.Valid(st.insertedCreds[0].Secret) {
		t.Fatalf("落库的 secret 应是合法 JSON：%+v", st.insertedCreds)
	}
	// 落库的 secret 必须是可解析的静态密钥形态。
	parsed, err := credential.ParseSecret(st.insertedCreds[0].Secret)
	if err != nil {
		t.Fatalf("落库的 secret 无法解析：%v", err)
	}
	if parsed.APIKey != "sk-upstream-secret-1234" {
		t.Errorf("落库明文不符：%q", parsed.APIKey)
	}

	// 列表只给出前缀，任何字段都不含明文。
	st.creds = []store.CredentialRow{{ID: 9, MerchantID: 2, CredGroup: "grp", Name: "primary", Secret: st.insertedCreds[0].Secret, Enabled: true}}
	code, env = do(t, h, http.MethodGet, CredentialsPath, "", testToken)
	if code != http.StatusOK {
		t.Fatalf("凭据列表应回 200，得到 %d", code)
	}
	if strings.Contains(string(env["data"]), "secret-1234") {
		t.Errorf("列表不应出现明文：%s", env["data"])
	}
}

// TestCredentialDisableIsScoped 断言凭据停用同样按商家收敛。
func TestCredentialDisableIsScoped(t *testing.T) {
	sessions, st := partnerSubject()
	h := NewHandler(Options{Sessions: sessions, Store: st})
	if code, _ := do(t, h, http.MethodPost, CredentialsPath+"/9/disable", "", testToken); code != http.StatusOK {
		t.Errorf("停用本商家的凭据应回 200，得到 %d", code)
	}
	if code, _ := do(t, h, http.MethodPost, CredentialsPath+"/10/disable", "", testToken); code != http.StatusNotFound {
		t.Errorf("停用他人凭据应回 404，得到 %d", code)
	}
}

// TestUsageStatsScopedAndValidated 断言聚合查询带上会话推导出的商家，且维度与区间参数
// 与账户面同口径。
func TestUsageStatsScopedAndValidated(t *testing.T) {
	sessions, st := partnerSubject()
	st.stats = []store.UsageStatsItem{{
		Key: "2026-10-09", Calls: 3,
		Tokens:        map[string]int64{"input_token": 10, "output_token": 4},
		ChargedAmount: "1.50000000",
	}}
	h := NewHandler(Options{Sessions: sessions, Store: st})

	path := UsageStatsPath + "?group_by=model&since=2026-10-01T00:00:00Z&until=2026-10-10T00:00:00Z&model=up-glm-5"
	code, env := do(t, h, http.MethodGet, path, "", testToken)
	if code != http.StatusOK {
		t.Fatalf("聚合应回 200，得到 %d", code)
	}
	if st.lastStats.MerchantID != 2 {
		t.Errorf("聚合作用域商家 = %d，期望 2", st.lastStats.MerchantID)
	}
	if st.lastStats.GroupBy != "model" || st.lastStats.RequestedModel != "up-glm-5" {
		t.Errorf("过滤条件不符：%+v", st.lastStats)
	}
	if st.lastStats.Since.IsZero() || st.lastStats.Until.IsZero() {
		t.Errorf("区间未透传：%+v", st.lastStats)
	}
	var data struct {
		Items []usageStatsItemView `json:"items"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("data 不是 {items}：%v", err)
	}
	if len(data.Items) != 1 || data.Items[0].Usage.InputTokens != 10 || data.Items[0].ChargedAmount != "1.50000000" {
		t.Errorf("聚合视图不符：%+v", data.Items)
	}

	for _, bad := range []string{"?group_by=hour", "?since=yesterday", "?api_key_id=0"} {
		if code, _ := do(t, h, http.MethodGet, UsageStatsPath+bad, "", testToken); code != http.StatusBadRequest {
			t.Errorf("%s 应回 400，得到 %d", bad, code)
		}
	}
}

// TestUnknownPathReturnsEnvelope 断言子树内未声明的路径回页面信封 404。
func TestUnknownPathReturnsEnvelope(t *testing.T) {
	sessions, st := partnerSubject()
	h := NewHandler(Options{Sessions: sessions, Store: st})
	if code, _ := do(t, h, http.MethodGet, PathPrefix+"/nope", "", testToken); code != http.StatusNotFound {
		t.Errorf("未声明的路径应回 404，得到 %d", code)
	}
}
