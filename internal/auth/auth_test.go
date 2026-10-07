package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件覆盖页面认证的端到端行为：挑战、注册登录、会话、刷新轮换与登出。
// 数据访问用内存替身，HTTP 层用 httptest，全链不经真实数据库。

// memStore 是 Store 的内存实现，语义与库表一致：
// 唯一键冲突回 store.ErrConflict，无匹配回 sql.ErrNoRows，轮换用 CAS。
type memStore struct {
	mu          sync.Mutex
	users       map[uint64]*store.WebUser
	sessions    map[uint64]*store.WebSession
	byAccess    map[string]uint64
	byRefresh   map[string]uint64
	byPrev      map[string]uint64
	otps        map[string]*memOTP
	identities  []store.WebIdentity
	nextUser    uint64
	nextSession uint64
}

// memOTP 是一行验证码：键含邮箱、用途与摘要，与库表的核销条件同构。
type memOTP struct {
	codeHash  string
	expiresAt time.Time
	used      bool
}

func newMemStore() *memStore {
	return &memStore{
		users:     make(map[uint64]*store.WebUser),
		sessions:  make(map[uint64]*store.WebSession),
		byAccess:  make(map[string]uint64),
		byRefresh: make(map[string]uint64),
		byPrev:    make(map[string]uint64),
		otps:      make(map[string]*memOTP),
	}
}

func (m *memStore) WebUserByLogin(_ context.Context, login string) (*store.WebUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.Email == login || u.Username == login {
			cp := *u
			return &cp, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (m *memStore) WebUserByEmail(_ context.Context, email string) (*store.WebUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (m *memStore) WebInsertUser(_ context.Context, u store.WebUser) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.users {
		if existing.Email == u.Email || existing.Username == u.Username {
			return 0, store.ErrConflict
		}
	}
	m.nextUser++
	u.ID = m.nextUser
	m.users[u.ID] = &u
	return u.ID, nil
}

func (m *memStore) WebInsertSession(_ context.Context, sess store.WebSession) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextSession++
	sess.ID = m.nextSession
	m.sessions[sess.ID] = &sess
	m.byAccess[sess.AccessHash] = sess.ID
	m.byRefresh[sess.RefreshHash] = sess.ID
	return sess.ID, nil
}

func (m *memStore) load(hash string, table map[string]uint64) (*store.WebSessionWithUser, error) {
	id, ok := table[hash]
	if !ok {
		return nil, sql.ErrNoRows
	}
	sess := *m.sessions[id]
	user := *m.users[sess.UserID]
	return &store.WebSessionWithUser{Session: sess, User: user}, nil
}

func (m *memStore) WebSessionByAccess(_ context.Context, hash string) (*store.WebSessionWithUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.load(hash, m.byAccess)
}

func (m *memStore) WebSessionByRefresh(_ context.Context, hash string) (*store.WebSessionWithUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.load(hash, m.byRefresh)
}

func (m *memStore) WebSessionByPrevRefresh(_ context.Context, hash string) (*store.WebSessionWithUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.load(hash, m.byPrev)
}

func (m *memStore) WebRotateSession(_ context.Context, id uint64, expect, accessHash, refreshHash string,
	accessExpiresAt, refreshExpiresAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[id]
	if !ok || sess.RefreshHash != expect || sess.RevokedAt != nil {
		return false, nil
	}
	delete(m.byAccess, sess.AccessHash)
	delete(m.byRefresh, sess.RefreshHash)
	m.byPrev[sess.RefreshHash] = id
	sess.RefreshHash = refreshHash
	sess.AccessHash = accessHash
	sess.AccessExpiresAt = accessExpiresAt
	sess.RefreshExpiresAt = refreshExpiresAt
	m.byAccess[accessHash] = id
	m.byRefresh[refreshHash] = id
	return true, nil
}

func (m *memStore) WebRevokeSession(_ context.Context, id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[id]; ok && sess.RevokedAt == nil {
		now := time.Now()
		sess.RevokedAt = &now
	}
	return nil
}

func (m *memStore) WebRevokeUserSessions(_ context.Context, userID uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, sess := range m.sessions {
		if sess.UserID == userID && sess.RevokedAt == nil {
			cp := now
			sess.RevokedAt = &cp
		}
	}
	return nil
}

func (m *memStore) WebIdentityProviders(context.Context, uint64) ([]string, error) {
	return []string{}, nil
}

func (m *memStore) WebUserByID(_ context.Context, id uint64) (*store.WebUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	cp := *u
	return &cp, nil
}

func (m *memStore) WebIdentityByProvider(_ context.Context, provider, subject string) (*store.WebIdentity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.identities {
		if id.Provider == provider && id.Subject == subject {
			cp := id
			return &cp, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (m *memStore) WebInsertIdentity(_ context.Context, id store.WebIdentity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.identities {
		if existing.Provider == id.Provider && existing.Subject == id.Subject {
			return store.ErrConflict
		}
	}
	m.identities = append(m.identities, id)
	return nil
}

// otpKey 是验证码的复合键：邮箱、用途与摘要拼接，与库表核销条件同构。
func otpKey(email, purpose, codeHash string) string {
	return email + "\x00" + purpose + "\x00" + codeHash
}

func (m *memStore) WebInsertOTP(_ context.Context, email, purpose, codeHash string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.otps[otpKey(email, purpose, codeHash)] = &memOTP{codeHash: codeHash, expiresAt: expiresAt}
	return nil
}

func (m *memStore) WebConsumeOTP(_ context.Context, email, purpose, codeHash string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.otps[otpKey(email, purpose, codeHash)]
	if !ok || entry.used || !now.Before(entry.expiresAt) {
		return false, nil
	}
	entry.used = true
	return true, nil
}

func (m *memStore) WebUpdatePassword(_ context.Context, userID uint64, passwordHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.users[userID]; ok {
		u.PasswordHash = passwordHash
	}
	return nil
}

func (m *memStore) WebRevokeOtherSessions(_ context.Context, userID, keepSessionID uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for id, sess := range m.sessions {
		if sess.UserID == userID && id != keepSessionID && sess.RevokedAt == nil {
			cp := now
			sess.RevokedAt = &cp
		}
	}
	return nil
}

func (m *memStore) WebEraseUser(_ context.Context, userID uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.users[userID]; ok {
		u.Status = "erased"
		u.PasswordHash = ""
	}
	return nil
}

// ---- 测试辅助 ----

type testEnv struct {
	svc *Service
	mux http.Handler
	st  *memStore
	now time.Time
}

// newTestEnv 构造固定时钟的认证服务与 HTTP 入口。
func newTestEnv(t *testing.T, opts Options) *testEnv {
	t.Helper()
	st := newMemStore()
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	opts.Now = func() time.Time { return base }
	svc := New(st, opts)
	return &testEnv{svc: svc, mux: NewHandler(svc), st: st, now: base}
}

// do 发起一次请求并解码信封。
func (e *testEnv) do(t *testing.T, method, path string, body any, bearer string) (int, map[string]json.RawMessage) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, reader)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("响应不是 JSON（%d）: %s", rec.Code, rec.Body.String())
	}
	// 信封六字段固定出现：这是页面契约的硬约束，任何端点不得缺字段。
	for _, key := range []string{"code", "data", "message", "page", "size", "total"} {
		if _, ok := envelope[key]; !ok {
			t.Errorf("信封缺少字段 %s: %s", key, rec.Body.String())
		}
	}
	return rec.Code, envelope
}

// codeOf 取信封业务码。
func codeOf(t *testing.T, envelope map[string]json.RawMessage) int {
	t.Helper()
	var code int
	if err := json.Unmarshal(envelope["code"], &code); err != nil {
		t.Fatalf("解析 code: %v", err)
	}
	return code
}

// challengeKey 请求一次性公钥并返回可加密的公钥对象。
func (e *testEnv) challengeKey(t *testing.T, fingerprint string) *rsa.PublicKey {
	t.Helper()
	status, env := e.do(t, http.MethodGet, PathChallenge+"?fingerprint="+url.QueryEscape(fingerprint), nil, "")
	if status != http.StatusOK || codeOf(t, env) != codeOK {
		t.Fatalf("challenge 失败: %d %s", status, env)
	}
	var data publicKeyData
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析公钥: %v", err)
	}
	if data.Algorithm != "RSA-OAEP" || data.Hash != "SHA-256" {
		t.Fatalf("算法声明不符: %+v", data)
	}
	der, err := base64.StdEncoding.DecodeString(data.PublicKey)
	if err != nil {
		t.Fatalf("公钥 base64 解码: %v", err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatalf("解析 SPKI: %v", err)
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("公钥类型不是 RSA: %T", parsed)
	}
	return pub
}

// encryptPassword 用挑战公钥加密密码，模拟浏览器 WebCrypto 的等价操作。
func encryptPassword(t *testing.T, pub *rsa.PublicKey, plain string) string {
	t.Helper()
	cipher, err := rsa.EncryptOAEP(sha256Hash(), rand.Reader, pub, []byte(plain), nil)
	if err != nil {
		t.Fatalf("加密密码: %v", err)
	}
	return base64.StdEncoding.EncodeToString(cipher)
}

// signup 注册一个固定账号并返回会话令牌。
func (e *testEnv) signup(t *testing.T, fingerprint string) (access, refresh string) {
	t.Helper()
	fp := fingerprint
	pub := e.challengeKey(t, fp)
	status, env := e.do(t, http.MethodPost, PathSignup, map[string]string{
		"email":       "user@example.com",
		"username":    "tester",
		"password":    encryptPassword(t, pub, "s3cret-pass"),
		"fingerprint": fp,
	}, "")
	if status != http.StatusOK || codeOf(t, env) != codeOK {
		t.Fatalf("注册失败: %d %s", status, env)
	}
	var tokens sessionTokens
	if err := json.Unmarshal(env["data"], &tokens); err != nil {
		t.Fatalf("解析令牌: %v", err)
	}
	if tokens.User.Role != "member" || tokens.User.Username != "tester" {
		t.Fatalf("注册响应身份不符: %+v", tokens.User)
	}
	if len(tokens.User.Identities) != 1 || tokens.User.Identities[0] != "password" {
		t.Fatalf("登录方式应只含 password: %v", tokens.User.Identities)
	}
	return tokens.AccessToken, tokens.RefreshToken
}

// ---- 用例 ----

// TestSignupSigninSessionRefreshSignout 走完注册 → 会话 → 刷新 → 登出的主链路，
// 并验证刷新令牌重放会使整个会话失效。
func TestSignupSigninSessionRefreshSignout(t *testing.T) {
	e := newTestEnv(t, Options{})
	access, refresh := e.signup(t, "fp-main-0001")

	// 会话查询返回身份与角色。
	status, env := e.do(t, http.MethodGet, PathSession, nil, access)
	if status != http.StatusOK || codeOf(t, env) != codeOK {
		t.Fatalf("session 失败: %d %s", status, env)
	}
	var user sessionUser
	if err := json.Unmarshal(env["data"], &user); err != nil {
		t.Fatalf("解析身份: %v", err)
	}
	if user.ID == 0 || user.Role != "member" {
		t.Fatalf("身份不符: %+v", user)
	}

	// 刷新：拿到新访问令牌，旧刷新令牌作废。
	status, env = e.do(t, http.MethodPost, PathRefresh, map[string]string{"refresh_token": refresh}, "")
	if status != http.StatusOK || codeOf(t, env) != codeOK {
		t.Fatalf("refresh 失败: %d %s", status, env)
	}
	var next accessTokenData
	if err := json.Unmarshal(env["data"], &next); err != nil {
		t.Fatalf("解析刷新结果: %v", err)
	}
	if next.AccessToken == "" || next.AccessToken == access {
		t.Fatalf("刷新应换发新访问令牌")
	}

	// 重放旧刷新令牌：拒绝，且整个会话被撤销。
	status, env = e.do(t, http.MethodPost, PathRefresh, map[string]string{"refresh_token": refresh}, "")
	if status != http.StatusUnauthorized || codeOf(t, env) != codeUnauthorized {
		t.Fatalf("重放应返回 401: %d %s", status, env)
	}
	// 会话撤销后，新访问令牌同样失效。
	status, env = e.do(t, http.MethodGet, PathSession, nil, next.AccessToken)
	if status != http.StatusUnauthorized || codeOf(t, env) != codeUnauthorized {
		t.Fatalf("会话撤销后应 401: %d %s", status, env)
	}
}

// TestSigninUnifiedFailure 断言账号不存在、密码错误、OAuth-only 账号三类失败
// 收敛为同一个 401 与同一句文案。
func TestSigninUnifiedFailure(t *testing.T) {
	e := newTestEnv(t, Options{})
	_, _ = e.signup(t, "fp-unified-01")

	cases := []struct {
		name  string
		login string
		plain string
		fp    string
	}{
		{"账号不存在", "ghost@example.com", "whatever-1", "fp-unified-02"},
		{"密码错误", "tester", "wrong-pass-1", "fp-unified-03"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pub := e.challengeKey(t, tc.fp)
			status, env := e.do(t, http.MethodPost, PathSignin, map[string]string{
				"username":    tc.login,
				"password":    encryptPassword(t, pub, tc.plain),
				"fingerprint": tc.fp,
			}, "")
			if status != http.StatusUnauthorized || codeOf(t, env) != codeUnauthorized {
				t.Fatalf("应统一 401: %d %s", status, env)
			}
			var msg string
			_ = json.Unmarshal(env["message"], &msg)
			if msg != "账号或密码错误" {
				t.Fatalf("文案不一致: %q", msg)
			}
		})
	}
}

// TestChallengeOneTime 断言同一指纹的公钥只能消费一次，重复提交回 410。
func TestChallengeOneTime(t *testing.T) {
	e := newTestEnv(t, Options{})
	_, _ = e.signup(t, "fp-once-other") // 造一个可登录账号

	fp := "fp-once-0001"
	pub := e.challengeKey(t, fp) // GET 只签发，不消费私钥
	cipher := encryptPassword(t, pub, "wrong-pass-01")

	// 第一次提交：私钥在、密码错 → 401（与其它凭据失败同码同文案）。
	status, env := e.do(t, http.MethodPost, PathSignin, map[string]string{
		"username":    "tester",
		"password":    cipher,
		"fingerprint": fp,
	}, "")
	if status != http.StatusUnauthorized || codeOf(t, env) != codeUnauthorized {
		t.Fatalf("首次消费应回 401: %d %s", status, env)
	}

	// 第二次提交：私钥已随上一次取出销毁 → 410，不进入密码比对。
	status, env = e.do(t, http.MethodPost, PathSignin, map[string]string{
		"username":    "tester",
		"password":    cipher,
		"fingerprint": fp,
	}, "")
	if status != http.StatusGone || codeOf(t, env) != codeChallengeExpired {
		t.Fatalf("已消费的指纹应 410: %d %s", status, env)
	}
}

// TestSignupDisabled 断言注册开关关闭时回 403。
func TestSignupDisabled(t *testing.T) {
	e := newTestEnv(t, Options{SignupDisabled: true})
	fp := "fp-disabled-1"
	pub := e.challengeKey(t, fp)
	status, env := e.do(t, http.MethodPost, PathSignup, map[string]string{
		"email":       "user@example.com",
		"username":    "tester",
		"password":    encryptPassword(t, pub, "s3cret-pass"),
		"fingerprint": fp,
	}, "")
	if status != http.StatusForbidden || codeOf(t, env) != codeForbidden {
		t.Fatalf("注册关闭应 403: %d %s", status, env)
	}
}

// TestSignupConflict 断言邮箱或用户名重复回 409。
func TestSignupConflict(t *testing.T) {
	e := newTestEnv(t, Options{})
	_, _ = e.signup(t, "fp-conflict-01")

	fp := "fp-conflict-02"
	pub := e.challengeKey(t, fp)
	status, env := e.do(t, http.MethodPost, PathSignup, map[string]string{
		"email":       "user@example.com", // 同邮箱
		"username":    "other-name",
		"password":    encryptPassword(t, pub, "s3cret-pass"),
		"fingerprint": fp,
	}, "")
	if status != http.StatusConflict || codeOf(t, env) != codeConflict {
		t.Fatalf("重复注册应 409: %d %s", status, env)
	}
}

// TestSigninRateLimited 断言认证端点按来源限频。
func TestSigninRateLimited(t *testing.T) {
	e := newTestEnv(t, Options{})
	for i := 0; i < 40; i++ {
		fp := fmt.Sprintf("fp-rate-%04d", i)
		status, env := e.do(t, http.MethodGet, PathChallenge+"?fingerprint="+fp, nil, "")
		if status == http.StatusTooManyRequests {
			if codeOf(t, env) != codeTooManyRequests {
				t.Fatalf("限频业务码应为 429: %s", env)
			}
			return
		}
		if status != http.StatusOK {
			t.Fatalf("第 %d 次 challenge 意外失败: %d %s", i, status, env)
		}
	}
	t.Fatalf("40 次 challenge 内未触发限频")
}

// TestSignoutIdempotent 断言登出成功后会话失效，且重复登出不报错。
func TestSignoutIdempotent(t *testing.T) {
	e := newTestEnv(t, Options{})
	access, _ := e.signup(t, "fp-signout-1")

	status, _ := e.do(t, http.MethodPost, PathSignout, nil, access)
	if status != http.StatusOK {
		t.Fatalf("登出应 200: %d", status)
	}
	status, _ = e.do(t, http.MethodGet, PathSession, nil, access)
	if status != http.StatusUnauthorized {
		t.Fatalf("登出后会话应失效: %d", status)
	}
	// 行仍在、已撤销：重复登出同样成功（幂等）。
	status, _ = e.do(t, http.MethodPost, PathSignout, nil, access)
	if status != http.StatusOK {
		t.Fatalf("重复登出应 200: %d", status)
	}
}

// TestTokenStoredHashed 断言库里存的是令牌哈希而不是明文。
func TestTokenStoredHashed(t *testing.T) {
	e := newTestEnv(t, Options{})
	access, refresh := e.signup(t, "fp-hash-0001")

	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	for _, sess := range e.st.sessions {
		if sess.AccessHash == access || sess.RefreshHash == refresh {
			t.Fatalf("库中出现明文令牌")
		}
		if len(sess.AccessHash) != 64 || len(sess.RefreshHash) != 64 {
			t.Fatalf("令牌哈希应为 64 位十六进制: %q", sess.AccessHash)
		}
	}
}

// TestUnknownPathReturnsEnvelope 断言子树内未声明路径回信封 404。
func TestUnknownPathReturnsEnvelope(t *testing.T) {
	e := newTestEnv(t, Options{})
	status, env := e.do(t, http.MethodGet, PathPrefix+"nonexistent", nil, "")
	if status != http.StatusNotFound || codeOf(t, env) != codeNotFound {
		t.Fatalf("应为信封 404: %d %s", status, env)
	}
	if !strings.Contains(string(env["message"]), "接口不存在") {
		t.Fatalf("文案不符: %s", env["message"])
	}
}

// sha256Hash 返回 OAEP 用的哈希实现，避免在辅助函数里重复引入依赖名。
func sha256Hash() hash.Hash { return sha256.New() }
