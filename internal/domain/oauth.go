package domain

import "strings"

// OAuthProfile 是渠道在 config 里声明的 OAuth 端点画像。
//
// 厂商差异全部配置化：本类型只承载端点地址与客户端标识，不含任何厂商分支；
// 新厂商接入 = 在渠道 config 里写一段 oauth 配置，不需要改代码。
//
// 它与 SigninHeader 同属「渠道 config 里本网关认识的键」：同样按结构体解析、
// 同样对写坏的取值宽容退化为未配置，缺键不影响选路。
type OAuthProfile struct {
	// AuthorizeURL 是授权码流程的授权地址，用于拼装用户访问的 URL。
	AuthorizeURL string
	// TokenURL 是换取与刷新令牌的地址；设备码轮询与授权码交换都打这里。
	TokenURL string
	// DeviceURL 是设备码流程的设备授权地址。为空表示该渠道不支持设备码，
	// 登录走授权码流程。
	DeviceURL string
	// ClientID 是 OAuth 客户端的公开标识。
	ClientID string
	// Scope 是请求的授权范围，可为空（部分厂商按客户端预置范围）。
	Scope string
}

// Configured 报告画像是否足以发起一次续期。
//
// 只认 TokenURL 与 ClientID：续期只需要这两个端点事实，授权地址与设备地址
// 只在交互式登录时用到，缺它们不影响数据面按 refresh_token 续期。
func (p OAuthProfile) Configured() bool {
	return strings.TrimSpace(p.TokenURL) != "" && strings.TrimSpace(p.ClientID) != ""
}

// SupportsDeviceCode 报告该画像是否声明了设备码端点。
func (p OAuthProfile) SupportsDeviceCode() bool {
	return p.Configured() && strings.TrimSpace(p.DeviceURL) != ""
}

// SupportsAuthorizationCode 报告该画像是否声明了授权码端点。
func (p OAuthProfile) SupportsAuthorizationCode() bool {
	return p.Configured() && strings.TrimSpace(p.AuthorizeURL) != ""
}
