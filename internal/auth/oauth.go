package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是第三方登录（Google、GitHub）的授权、换取与账号归并。
//
// 流程与 docs/openapi-web.yaml 对齐：providers 列出已配置的提供方，
// oauth/{provider} 返回带 state 的授权地址（整页跳转），
// 回调落前端路由后由 exchange 用授权码换取会话 —— 全程 JSON 信封，
// 不引入 302 响应绕开信封的例外。
//
// state 存内存、一次性：它是防 CSRF 的短期随机值，寿命以分钟计，
// 重启丢失的代价只是客户端重新走一次授权，不值得持久化。

// 新增的页面业务码语义（映射见 handler 的 writeServiceErr）。
var (
	// ErrOAuthProviderMissing 表示提供方不存在或未配置。
	ErrOAuthProviderMissing = errors.New("auth: 登录方式未启用")
	// ErrOAuthUpstream 表示与提供方的令牌或用户信息交换失败。
	ErrOAuthUpstream = errors.New("auth: 第三方登录服务不可用")
	// ErrOAuthEmailMissing 表示提供方未返回邮箱，无法归并账号。
	ErrOAuthEmailMissing = errors.New("auth: 第三方未提供邮箱")
)

// OAuthProvider 是一个已配置的第三方登录提供方。
//
// URL 字段不设默认值：由装配层按提供方常量填充，测试可整体替换成假端点。
type OAuthProvider struct {
	// ID 是提供方标识，用于路径与 web_identity.provider（google / github）。
	ID string
	// Name 是展示名，出现在 providers 列表里。
	Name         string
	ClientID     string
	ClientSecret string
	// RedirectURI 是授权回调地址，必须与提供方控制台登记的一致。
	RedirectURI  string
	AuthorizeURL string
	TokenURL     string
	UserInfoURL  string
	// EmailsURL 是补充邮箱端点（GitHub 的 user/emails）；空表示不用。
	EmailsURL string
	Scope     string
}

// providerData 是 providers 端点的响应项，与契约 AuthProvider 对齐。
type providerData struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// oauthAuthorizeData 是授权地址端点的响应数据，与契约对齐。
type oauthAuthorizeData struct {
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
}

// oauthStateEntry 是一条未消费的 state：它与签发时的提供方绑定，
// 兑换时两边必须一致，避免同一 state 被拿到另一个提供方路径上使用。
type oauthStateEntry struct {
	provider string
	expires  time.Time
}

// stateSet 是一次性 CSRF state 的内存表。
type stateSet struct {
	mu   sync.Mutex
	used map[string]oauthStateEntry
	ttl  time.Duration
	now  func() time.Time
}

func newStateSet(ttl time.Duration, now func() time.Time) *stateSet {
	return &stateSet{used: make(map[string]oauthStateEntry), ttl: ttl, now: now}
}

// put 生成并登记一个与 provider 绑定的 state。
func (s *stateSet) put(provider string) (string, error) {
	pair, err := newTokenPair()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 顺手清理过期项：表规模与限频同量级，逐次线性清理足够。
	for st, entry := range s.used {
		if s.now().After(entry.expires) {
			delete(s.used, st)
		}
	}
	s.used[pair.plain] = oauthStateEntry{provider: provider, expires: s.now().Add(s.ttl)}
	return pair.plain, nil
}

// take 校验并消费 state，返回它绑定的提供方；未登记、已消费或已过期返回空串。
func (s *stateSet) take(state string) string {
	if state == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.used[state]
	if !ok {
		return ""
	}
	delete(s.used, state)
	if !s.now().Before(entry.expires) {
		return ""
	}
	return entry.provider
}

// oauthStateTTL 是 state 寿命：覆盖「打开授权页 → 用户同意 → 回调」一段交互。
const oauthStateTTL = 10 * time.Minute

// oauthHTTPTimeout 是与提供方通信的上限：第三方卡住不应拖住页面请求。
const oauthHTTPTimeout = 10 * time.Second

// oauthMaxBody 是提供方响应体上限：token 与 userinfo 报文远小于此，
// 超限即截断并按解析失败处理，避免异常端点撑爆内存。
const oauthMaxBody = 1 << 20

// usernameMaxLen 是用户名长度上限，与 0006 迁移的列宽对齐留余。
const usernameMaxLen = 56

// Providers 返回已配置的登录方式列表。
func (s *Service) Providers() []providerData {
	out := make([]providerData, 0, len(s.oauthProviders))
	for _, p := range s.oauthProviders {
		out = append(out, providerData{ID: p.ID, Name: p.Name})
	}
	return out
}

// OAuthAuthorizeURL 生成带 state 的授权地址；提供方未配置回 ErrOAuthProviderMissing。
func (s *Service) OAuthAuthorizeURL(providerID string) (*oauthAuthorizeData, error) {
	p := s.oauthProvider(providerID)
	if p == nil {
		return nil, ErrOAuthProviderMissing
	}
	state, err := s.oauthStates.put(p.ID)
	if err != nil {
		return nil, err
	}
	authorize, err := url.Parse(p.AuthorizeURL)
	if err != nil {
		return nil, ErrOAuthUpstream
	}
	q := authorize.Query()
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", p.RedirectURI)
	q.Set("response_type", "code")
	q.Set("scope", p.Scope)
	q.Set("state", state)
	authorize.RawQuery = q.Encode()
	return &oauthAuthorizeData{AuthorizeURL: authorize.String(), State: state}, nil
}

// OAuthExchange 用授权码换取会话：校验 state、换 token、取用户信息、归并账号。
func (s *Service) OAuthExchange(ctx context.Context, addr, providerID, code, state string) (*sessionTokens, error) {
	if !s.limiter.allow("oauth:" + addr) {
		return nil, ErrRateLimited
	}
	p := s.oauthProvider(providerID)
	if p == nil {
		return nil, ErrOAuthProviderMissing
	}
	// state 先于一切网络调用校验，且必须绑定当前提供方：
	// 无效或跨提供方的请求不应消耗提供方配额。
	if s.oauthStates.take(strings.TrimSpace(state)) != providerID {
		return nil, ErrInvalidParams
	}
	if strings.TrimSpace(code) == "" {
		return nil, ErrInvalidParams
	}

	profile, err := s.fetchProfile(ctx, p, code)
	if err != nil {
		return nil, err
	}
	user, err := s.mergeIdentity(ctx, p.ID, profile)
	if err != nil {
		return nil, err
	}
	return s.issueSession(ctx, user)
}

// oauthProfile 是提供方归一后的身份：稳定主体标识与邮箱。
type oauthProfile struct {
	Subject string
	Email   string
}

// fetchProfile 完成令牌交换与用户信息拉取，并按提供方归一。
func (s *Service) fetchProfile(ctx context.Context, p *OAuthProvider, code string) (*oauthProfile, error) {
	accessToken, err := s.exchangeToken(ctx, p, code)
	if err != nil {
		return nil, err
	}
	body, err := s.oauthGet(ctx, p.UserInfoURL, accessToken)
	if err != nil {
		return nil, err
	}

	switch p.ID {
	case "google":
		var info struct {
			Sub   string `json:"sub"`
			Email string `json:"email"`
		}
		if err := json.Unmarshal(body, &info); err != nil || info.Sub == "" {
			return nil, ErrOAuthUpstream
		}
		return &oauthProfile{Subject: info.Sub, Email: info.Email}, nil

	case "github":
		var info struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
			Email string `json:"email"`
		}
		if err := json.Unmarshal(body, &info); err != nil || info.ID == 0 {
			return nil, ErrOAuthUpstream
		}
		// 主资料的 email 可为 null：改从邮箱列表取已验证的主邮箱。
		if info.Email == "" && p.EmailsURL != "" {
			email, err := s.githubEmail(ctx, p, accessToken)
			if err != nil {
				return nil, err
			}
			info.Email = email
		}
		// 主体用数字 id 而不是 login：login 可改，改了会导致账号失联。
		return &oauthProfile{Subject: strconv.FormatInt(info.ID, 10), Email: info.Email}, nil
	}
	return nil, ErrOAuthProviderMissing
}

// githubEmail 从邮箱列表挑一枚可用地址：优先已验证的主邮箱，其次任意已验证邮箱。
func (s *Service) githubEmail(ctx context.Context, p *OAuthProvider, accessToken string) (string, error) {
	body, err := s.oauthGet(ctx, p.EmailsURL, accessToken)
	if err != nil {
		return "", err
	}
	var list []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", ErrOAuthUpstream
	}
	fallback := ""
	for _, item := range list {
		if !item.Verified {
			continue
		}
		if item.Primary {
			return item.Email, nil
		}
		if fallback == "" {
			fallback = item.Email
		}
	}
	if fallback == "" {
		return "", ErrOAuthEmailMissing
	}
	return fallback, nil
}

// exchangeToken 用授权码向提供方换取访问令牌。
func (s *Service) exchangeToken(ctx context.Context, p *OAuthProvider, code string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {p.ClientID},
		"client_secret": {p.ClientSecret},
		"redirect_uri":  {p.RedirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", ErrOAuthUpstream
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.oauthClient.Do(req)
	if err != nil {
		return "", ErrOAuthUpstream
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", ErrOAuthUpstream
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, oauthMaxBody))
	if err != nil {
		return "", ErrOAuthUpstream
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" {
		return "", ErrOAuthUpstream
	}
	return token.AccessToken, nil
}

// oauthGet 带 Bearer 拉取提供方端点。
func (s *Service) oauthGet(ctx context.Context, endpoint, accessToken string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrOAuthUpstream
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := s.oauthClient.Do(req)
	if err != nil {
		return nil, ErrOAuthUpstream
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrOAuthUpstream
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, oauthMaxBody))
	if err != nil {
		return nil, ErrOAuthUpstream
	}
	return body, nil
}

// mergeIdentity 把提供方身份并到页面账号：绑定已存在走原账号，否则按邮箱并入或新建。
func (s *Service) mergeIdentity(ctx context.Context, providerID string, profile *oauthProfile) (*store.WebUser, error) {
	email := strings.TrimSpace(profile.Email)
	if email == "" {
		return nil, ErrOAuthEmailMissing
	}
	if err := validateEmail(email); err != nil {
		return nil, ErrOAuthEmailMissing
	}

	identity, err := s.store.WebIdentityByProvider(ctx, providerID, profile.Subject)
	if err == nil {
		user, userErr := s.store.WebUserByID(ctx, identity.UserID)
		if userErr != nil || !userActive(user) {
			return nil, ErrInvalidCredentials
		}
		return user, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// 未绑定：先按邮箱并入既有账号（同一人换了提供方或先注册过密码账号）。
	existing, err := s.store.WebUserByEmail(ctx, email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var userID uint64
	if existing != nil {
		if !userActive(existing) {
			return nil, ErrInvalidCredentials
		}
		userID = existing.ID
	} else {
		id, createErr := s.createOAuthUser(ctx, email)
		if createErr != nil {
			return nil, createErr
		}
		userID = id
	}
	if insertErr := s.store.WebInsertIdentity(ctx, store.WebIdentity{
		UserID: userID, Provider: providerID, Subject: profile.Subject, Email: email,
	}); insertErr != nil {
		return nil, insertErr
	}
	user, err := s.store.WebUserByID(ctx, userID)
	if err != nil || !userActive(user) {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}

// createOAuthUser 为第三方身份新建账号并在同一事务内开户，用户名取邮箱前缀并做唯一化。
func (s *Service) createOAuthUser(ctx context.Context, email string) (uint64, error) {
	base := usernameFromEmail(email)
	// 冲突时追加序号重试：唯一键是邮箱与用户名两者的并集，
	// 这里只处理用户名撞车；邮箱撞车在上一步已按既有账号并入。
	for attempt := 0; attempt < 6; attempt++ {
		candidate := base
		if attempt > 0 {
			candidate = fmt.Sprintf("%s_%d", base, attempt+1)
		}
		id, _, err := s.store.WebInsertUserWithAccount(ctx, store.WebUser{
			Email:    email,
			Username: candidate,
			Role:     store.RoleMember,
			Status:   statusActive,
		}, store.Account{
			Name:            candidate,
			PriceMultiplier: defaultAccountMultiplier,
			Status:          statusActive,
		})
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return 0, err
		}
	}
	return 0, store.ErrConflict
}

// usernameFromEmail 从邮箱前缀生成用户名：清洗非法字符并按上限截断。
func usernameFromEmail(email string) string {
	local := email
	if at := strings.IndexByte(email, '@'); at > 0 {
		local = email[:at]
	}
	var sb strings.Builder
	for _, r := range strings.ToLower(local) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	name := sb.String()
	// 用户名下限为 2：过短的前缀（如 a@x.com）补足长度，避免校验不通过。
	for len(name) < 2 {
		name += "u"
	}
	if len(name) > usernameMaxLen {
		name = name[:usernameMaxLen]
	}
	return name
}

// oauthProvider 按标识查已配置提供方。
func (s *Service) oauthProvider(id string) *OAuthProvider {
	for i := range s.oauthProviders {
		if s.oauthProviders[i].ID == id {
			return &s.oauthProviders[i]
		}
	}
	return nil
}
