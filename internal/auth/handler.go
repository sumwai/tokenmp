package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是页面认证的 HTTP 层：按路径分发、解码请求、把业务哨兵映射成信封。
//
// 与数据面 handler 的差异是刻意的：这里不产出 ErrorEnvelope，不带 402/429 限额语义，
// 也绝不复用 internal/access 的鉴权中间件 —— 页面会话与模型密钥是两套凭据。

// maxBodyBytes 是认证请求体的上限：本族端点只有几个短字段，
// 放大上限只会给「用大 body 撑爆解析」留空间。
const maxBodyBytes = 64 << 10

// NewHandler 构造页面认证的 HTTP 处理器，挂载于 PathPrefix。
func NewHandler(svc *Service) http.Handler {
	return &handler{svc: svc}
}

type handler struct {
	svc *Service
}

// signinRequest / signupRequest 与契约的 SigninRequest / SignupRequest 对齐。
type signinRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	Fingerprint string `json:"fingerprint"`
}

type signupRequest struct {
	Email       string `json:"email"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Fingerprint string `json:"fingerprint"`
}

// refreshRequest 与契约 RefreshRequest 对齐。
type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// otpRequest 与契约 SendOtpRequest 对齐；purpose 限定 reset / erase。
type otpRequest struct {
	Email   string `json:"email"`
	Purpose string `json:"purpose"`
}

// resetRequest 与契约 ResetRequest 对齐。
type resetRequest struct {
	Email       string `json:"email"`
	OTP         string `json:"otp"`
	Password    string `json:"password"`
	Fingerprint string `json:"fingerprint"`
}

// changePasswordRequest 与契约 ChangePasswordRequest 对齐：新旧密码各自带指纹。
type changePasswordRequest struct {
	CurrentPassword    string `json:"current_password"`
	CurrentFingerprint string `json:"current_fingerprint"`
	NewPassword        string `json:"new_password"`
	NewFingerprint     string `json:"new_fingerprint"`
}

// eraseRequest 与契约 EraseRequest 对齐。
type eraseRequest struct {
	OTP string `json:"otp"`
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case PathChallenge:
		h.challenge(w, r)
	case PathSignin:
		h.signin(w, r)
	case PathSignup:
		h.signup(w, r)
	case PathRefresh:
		h.refresh(w, r)
	case PathSession:
		h.session(w, r)
	case PathSignout:
		h.signout(w, r)
	case PathOTP:
		h.sendOTP(w, r)
	case PathReset:
		h.reset(w, r)
	case PathPassword:
		h.changePassword(w, r)
	case PathErase:
		h.erase(w, r)
	default:
		// 子树内未声明的路径回信封 404，不落到网关的 / 兜底：
		// 页面契约的错误形状在整条 /api/v1/auth/ 前缀上保持一致。
		writeErr(w, http.StatusNotFound, codeNotFound, "接口不存在")
	}
}

// challenge GET ?fingerprint=… → 公钥。
func (h *handler) challenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusBadRequest, codeBadRequest, "只支持 GET 方法")
		return
	}
	data, err := h.svc.Challenge(r.Context(), h.addr(r), r.URL.Query().Get("fingerprint"))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeOK(w, data)
}

// signin POST → 会话令牌。
func (h *handler) signin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusBadRequest, codeBadRequest, "只支持 POST 方法")
		return
	}
	var req signinRequest
	if !decodeBody(w, r, &req) {
		return
	}
	tokens, err := h.svc.Signin(r.Context(), h.addr(r), req.Username, req.Password, req.Fingerprint)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeOK(w, tokens)
}

// signup POST → 创建账号并签发会话。
func (h *handler) signup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusBadRequest, codeBadRequest, "只支持 POST 方法")
		return
	}
	var req signupRequest
	if !decodeBody(w, r, &req) {
		return
	}
	tokens, err := h.svc.Signup(r.Context(), h.addr(r), req.Email, req.Username, req.Password, req.Fingerprint)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeOK(w, tokens)
}

// refresh POST → 换发访问令牌。
func (h *handler) refresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusBadRequest, codeBadRequest, "只支持 POST 方法")
		return
	}
	var req refreshRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		writeErr(w, http.StatusBadRequest, codeBadRequest, "参数缺失或格式非法")
		return
	}
	data, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeOK(w, data)
}

// session GET → 当前身份。
func (h *handler) session(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusBadRequest, codeBadRequest, "只支持 GET 方法")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeErr(w, http.StatusUnauthorized, codeUnauthorized, "登录状态已失效")
		return
	}
	user, err := h.svc.SessionByAccess(r.Context(), token)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeOK(w, user)
}

// signout POST → 撤销当前会话。
func (h *handler) signout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusBadRequest, codeBadRequest, "只支持 POST 方法")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeErr(w, http.StatusUnauthorized, codeUnauthorized, "登录状态已失效")
		return
	}
	if err := h.svc.Signout(r.Context(), token); err != nil {
		writeServiceErr(w, err)
		return
	}
	writeOK(w, nil)
}

// sendOTP POST → 发送一次性验证码。
func (h *handler) sendOTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusBadRequest, codeBadRequest, "只支持 POST 方法")
		return
	}
	var req otpRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := h.svc.SendOtp(r.Context(), h.addr(r), req.Email, req.Purpose); err != nil {
		writeServiceErr(w, err)
		return
	}
	writeOK(w, nil)
}

// reset POST → 用 OTP 重置密码。
func (h *handler) reset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusBadRequest, codeBadRequest, "只支持 POST 方法")
		return
	}
	var req resetRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := h.svc.Reset(r.Context(), h.addr(r), req.Email, req.OTP, req.Password, req.Fingerprint); err != nil {
		writeServiceErr(w, err)
		return
	}
	writeOK(w, nil)
}

// changePassword PUT → 修改密码。
func (h *handler) changePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeErr(w, http.StatusBadRequest, codeBadRequest, "只支持 PUT 方法")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeErr(w, http.StatusUnauthorized, codeUnauthorized, "登录状态已失效")
		return
	}
	var req changePasswordRequest
	if !decodeBody(w, r, &req) {
		return
	}
	err := h.svc.ChangePassword(r.Context(), token,
		req.CurrentPassword, req.CurrentFingerprint, req.NewPassword, req.NewFingerprint)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeOK(w, nil)
}

// erase POST → 注销当前账号。
func (h *handler) erase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusBadRequest, codeBadRequest, "只支持 POST 方法")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeErr(w, http.StatusUnauthorized, codeUnauthorized, "登录状态已失效")
		return
	}
	var req eraseRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := h.svc.Erase(r.Context(), h.addr(r), token, req.OTP); err != nil {
		writeServiceErr(w, err)
		return
	}
	writeOK(w, nil)
}

// addr 取本次请求用于限频的来源地址。
func (h *handler) addr(r *http.Request) string {
	return clientAddr(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), h.svc.opts.TrustProxy)
}

// decodeBody 解码 JSON 请求体；语法错误、超限与非对象输入一律 400。
//
// 未知字段忽略而不是拒绝：与数据面「未知字段一律忽略」的口径一致，
// 前端先发、后端兼容期并存时不会因为多带一个字段整单失败。
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, codeBadRequest, "参数缺失或格式非法")
		return false
	}
	// 尾部再解一次：还有第二个 JSON 值说明 body 不是单个对象，同样按非法处理。
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, codeBadRequest, "参数缺失或格式非法")
		return false
	}
	return true
}

// bearerToken 从 Authorization 头取令牌；方案名不区分大小写，令牌非空。
//
// 与 internal/access 的同名函数刻意各写一份：两套凭据的演化方向不同，
// 共用一个解析器会让「哪边改了语义」变成跨包排查。
func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

// writeServiceErr 把业务哨兵映射为信封；未识别的错误按 500 处理并保留内部语义日志点。
func writeServiceErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidParams):
		writeErr(w, http.StatusBadRequest, codeBadRequest, "参数缺失或格式非法")
	case errors.Is(err, ErrInvalidCredentials):
		writeErr(w, http.StatusUnauthorized, codeUnauthorized, "账号或密码错误")
	case errors.Is(err, ErrUnauthorized):
		writeErr(w, http.StatusUnauthorized, codeUnauthorized, "登录状态已失效")
	case errors.Is(err, ErrSignupDisabled):
		writeErr(w, http.StatusForbidden, codeForbidden, "注册入口已关闭")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, codeConflict, "邮箱或用户名已被使用")
	case errors.Is(err, ErrChallengeExpired):
		writeErr(w, http.StatusGone, codeChallengeExpired, "加密公钥已失效，请重新获取")
	case errors.Is(err, ErrInvalidOTP):
		writeErr(w, http.StatusBadRequest, codeBadRequest, "验证码无效或已过期")
	case errors.Is(err, ErrRateLimited):
		writeErr(w, http.StatusTooManyRequests, codeTooManyRequests, "请求过于频繁，请稍后再试")
	case errors.Is(err, ErrMailerNotConfigured):
		writeErr(w, http.StatusInternalServerError, codeInternal, "邮件服务未配置")
	default:
		writeErr(w, http.StatusInternalServerError, codeInternal, "服务端错误")
	}
}
