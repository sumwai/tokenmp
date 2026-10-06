// Package oauth 实现订阅型上游所需的 OAuth 协议交互：刷新令牌、设备码授权与授权码交换。
//
// 职责边界：只按渠道配置里声明的端点地址与客户端标识收发报文，把厂商差异留在配置里；
// 本包不读数据库、不写凭据、不感知网关的凭据轮换与冷却，是一段可独立测试的协议客户端。
//
// 令牌只在本包内以结构体字段形式传递，任何错误文案只携带 OAuth 的 error 与
// error_description，不携带 access_token 或 refresh_token。
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/domain"
)

const (
	// defaultHTTPTimeout 是单次 OAuth 请求的兜底超时。
	defaultHTTPTimeout = 20 * time.Second
	// maxResponseBytes 是 OAuth 响应体的最大字节数，避免异常端点用超长响应撑爆网关。
	maxResponseBytes = 1 << 20
	// defaultDeviceInterval 是设备码轮询的兜底间隔。
	defaultDeviceInterval = 5 * time.Second
	// defaultDeviceExpiry 是设备码的兜底有效期。
	defaultDeviceExpiry = 10 * time.Minute
)

// 设备码轮询期间端点可能返回的两种「尚无结果」标记。
const (
	deviceCodePending  = "authorization_pending"
	deviceCodeSlowDown = "slow_down"
)

// ErrInvalidGrant 表示端点明确拒绝刷新令牌或授权码：凭据已失效。
//
// 调用方据此把该凭据标记为过期并进入冷却；这与网络抖动、端点 5xx 之类的
// 可重试失败必须分开，后者应保留旧 token 继续发请求。
var ErrInvalidGrant = errors.New("oauth: 授权已失效")

// ErrAuthorizationPending 表示设备码流程尚未获批。
var ErrAuthorizationPending = errors.New("oauth: 设备码尚未获批")

// Options 是构造 Client 的参数。
type Options struct {
	// HTTPClient 是底层 HTTP 客户端；nil 时使用带兜底超时的默认客户端。
	HTTPClient *http.Client
}

// Client 是 OAuth 协议客户端，可并发使用。
type Client struct {
	httpClient *http.Client
}

// New 构造 OAuth 协议客户端。
func New(opts Options) *Client {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Client{httpClient: httpClient}
}

// Token 是一次令牌端点的成功应答。
type Token struct {
	// Access 是访问令牌，注入上游请求头。
	Access string
	// Refresh 是刷新令牌；端点在本次应答里未回新的刷新令牌时沿用调用方传入的那份。
	Refresh string
	// Expires 是访问令牌的过期时刻；端点未给出有效期时为零值（视为不过期）。
	Expires time.Time
	// Account 是端点可选返回的账户标识，登录时写入凭据供人工辨识。
	Account string
}

// DeviceCode 是设备码流程的首个应答。
type DeviceCode struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	Interval                time.Duration
	ExpiresIn               time.Duration
}

// tokenResponse 是令牌端点与设备码端点应答里本包认识的字段。
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	Account          string `json:"account"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	DeviceCode       string `json:"device_code"`
	UserCode         string `json:"user_code"`
	VerificationURI  string `json:"verification_uri"`
	// 部分厂商用 verification_url 而非 verification_uri。
	VerificationURL         string `json:"verification_url"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                int64  `json:"interval"`
}

// Refresh 用刷新令牌换新的访问令牌。
//
// 成功时返回的 Refresh 优先取端点新发的刷新令牌：一次性刷新令牌必须在每次续期后
// 沿用端点给出的新值，否则下一次续期会用已被消耗的旧值。
func (c *Client) Refresh(ctx context.Context, profile domain.OAuthProfile, refreshToken string) (Token, error) {
	if strings.TrimSpace(profile.TokenURL) == "" {
		return Token{}, errors.New("oauth: 渠道未声明 token_url")
	}
	if strings.TrimSpace(refreshToken) == "" {
		return Token{}, errors.New("oauth: 缺少 refresh_token")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", profile.ClientID)
	if profile.Scope != "" {
		form.Set("scope", profile.Scope)
	}
	resp, err := c.postForm(ctx, profile.TokenURL, form)
	if err != nil {
		return Token{}, err
	}
	if resp.Error != "" {
		return Token{}, tokenError(resp)
	}
	if strings.TrimSpace(resp.AccessToken) == "" {
		return Token{}, errors.New("oauth: 刷新响应缺少 access_token")
	}
	return tokenFrom(resp, refreshToken, time.Now()), nil
}

// ExchangeCode 用授权码换取令牌。
func (c *Client) ExchangeCode(ctx context.Context, profile domain.OAuthProfile, code string) (Token, error) {
	if strings.TrimSpace(profile.TokenURL) == "" {
		return Token{}, errors.New("oauth: 渠道未声明 token_url")
	}
	if strings.TrimSpace(code) == "" {
		return Token{}, errors.New("oauth: 缺少授权码")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", profile.ClientID)
	if profile.Scope != "" {
		form.Set("scope", profile.Scope)
	}
	resp, err := c.postForm(ctx, profile.TokenURL, form)
	if err != nil {
		return Token{}, err
	}
	if resp.Error != "" {
		return Token{}, tokenError(resp)
	}
	if strings.TrimSpace(resp.AccessToken) == "" {
		return Token{}, errors.New("oauth: 令牌响应缺少 access_token")
	}
	return tokenFrom(resp, "", time.Now()), nil
}

// AuthorizeURL 拼装授权码流程的用户访问地址。
//
// state 由调用方提供用于防跨站请求伪造；为空时不带该参数。
func (c *Client) AuthorizeURL(profile domain.OAuthProfile, state string) (string, error) {
	if strings.TrimSpace(profile.AuthorizeURL) == "" {
		return "", errors.New("oauth: 渠道未声明 authorize_url")
	}
	parsed, err := url.Parse(profile.AuthorizeURL)
	if err != nil {
		return "", fmt.Errorf("oauth: authorize_url 非法: %w", err)
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", profile.ClientID)
	if profile.Scope != "" {
		query.Set("scope", profile.Scope)
	}
	if state != "" {
		query.Set("state", state)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// StartDeviceCode 发起设备码流程，返回用户码与验证地址。
func (c *Client) StartDeviceCode(ctx context.Context, profile domain.OAuthProfile) (DeviceCode, error) {
	if strings.TrimSpace(profile.DeviceURL) == "" {
		return DeviceCode{}, errors.New("oauth: 渠道未声明 device_url")
	}
	form := url.Values{}
	form.Set("client_id", profile.ClientID)
	if profile.Scope != "" {
		form.Set("scope", profile.Scope)
	}
	resp, err := c.postForm(ctx, profile.DeviceURL, form)
	if err != nil {
		return DeviceCode{}, err
	}
	if resp.Error != "" {
		return DeviceCode{}, tokenError(resp)
	}
	verification := firstNonEmpty(resp.VerificationURI, resp.VerificationURL)
	if strings.TrimSpace(resp.DeviceCode) == "" || strings.TrimSpace(resp.UserCode) == "" || verification == "" {
		return DeviceCode{}, errors.New("oauth: 设备码响应缺少 device_code / user_code / verification_uri")
	}
	interval := defaultDeviceInterval
	if resp.Interval > 0 {
		interval = time.Duration(resp.Interval) * time.Second
	}
	expires := defaultDeviceExpiry
	if resp.ExpiresIn > 0 {
		expires = time.Duration(resp.ExpiresIn) * time.Second
	}
	return DeviceCode{
		DeviceCode:              resp.DeviceCode,
		UserCode:                resp.UserCode,
		VerificationURI:         verification,
		VerificationURIComplete: resp.VerificationURIComplete,
		Interval:                interval,
		ExpiresIn:               expires,
	}, nil
}

// PollDeviceCode 按设备码轮询令牌，直到获批、超时或失败。
//
// 端点返回 authorization_pending 时继续等待，slow_down 时把轮询间隔加长一秒，
// 其余错误立即返回。ctx 取消或设备码过期时返回错误。
func (c *Client) PollDeviceCode(ctx context.Context, profile domain.OAuthProfile, device DeviceCode) (Token, error) {
	deadline := time.Now().Add(device.ExpiresIn)
	interval := device.Interval
	if interval <= 0 {
		interval = defaultDeviceInterval
	}
	for {
		if time.Now().After(deadline) {
			return Token{}, errors.New("oauth: 设备码已过期")
		}
		form := url.Values{}
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		form.Set("device_code", device.DeviceCode)
		form.Set("client_id", profile.ClientID)
		resp, err := c.postForm(ctx, profile.TokenURL, form)
		if err != nil {
			return Token{}, err
		}
		switch resp.Error {
		case "":
			if strings.TrimSpace(resp.AccessToken) == "" {
				return Token{}, errors.New("oauth: 设备码令牌响应缺少 access_token")
			}
			return tokenFrom(resp, "", time.Now()), nil
		case deviceCodePending:
		case deviceCodeSlowDown:
			interval += time.Second
		default:
			return Token{}, tokenError(resp)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Token{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// tokenFrom 把端点应答映射为 Token，并在端点未回新刷新令牌时沿用旧值。
func tokenFrom(resp tokenResponse, previousRefresh string, now time.Time) Token {
	refresh := strings.TrimSpace(resp.RefreshToken)
	if refresh == "" {
		refresh = previousRefresh
	}
	var expires time.Time
	if resp.ExpiresIn > 0 {
		expires = now.Add(time.Duration(resp.ExpiresIn) * time.Second)
	}
	return Token{
		Access:  resp.AccessToken,
		Refresh: refresh,
		Expires: expires,
		Account: strings.TrimSpace(resp.Account),
	}
}

// tokenError 把端点的 error 字段映射为错误；invalid_grant 单独成一个哨兵错误。
func tokenError(resp tokenResponse) error {
	description := strings.TrimSpace(resp.ErrorDescription)
	switch resp.Error {
	case "invalid_grant":
		if description != "" {
			return fmt.Errorf("%w: %s", ErrInvalidGrant, description)
		}
		return ErrInvalidGrant
	case deviceCodePending:
		return ErrAuthorizationPending
	default:
		if description != "" {
			return fmt.Errorf("oauth: 端点返回 %s: %s", resp.Error, description)
		}
		return fmt.Errorf("oauth: 端点返回 %s", resp.Error)
	}
}

// postForm 以表单编码发起一次 POST 并解析应答；非 2xx 也尝试解析 OAuth 错误体。
func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values) (tokenResponse, error) {
	body := form.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("oauth: 构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("oauth: 请求端点失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("oauth: 读取端点响应失败: %w", err)
	}
	var parsed tokenResponse
	if len(raw) > 0 {
		if unmarshalErr := json.Unmarshal(raw, &parsed); unmarshalErr != nil {
			// 非 JSON 应答只在状态码非 2xx 时值得转成 OAuth 错误；
			// 2xx 但解析失败说明端点形态不符，按协议错误返回。
			if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
				return tokenResponse{}, fmt.Errorf("oauth: 端点响应不是合法 JSON（状态码 %d）", resp.StatusCode)
			}
			return tokenResponse{}, fmt.Errorf("oauth: 端点返回状态码 %d", resp.StatusCode)
		}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if parsed.Error != "" {
			return parsed, nil
		}
		// 不把响应体原文放进错误：端点可能回显请求里的敏感值。
		return tokenResponse{}, fmt.Errorf("oauth: 端点返回状态码 %d", resp.StatusCode)
	}
	return parsed, nil
}

// firstNonEmpty 返回首个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
