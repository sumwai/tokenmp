package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// 本文件是邮箱一次性验证码（OTP）与它守护的三个动作：重置密码、注销账号；
// 改密不走 OTP（已有会话 + 当前密码双重凭据），但同文件就近维护。
//
// 防枚举口径贯穿三个入口：验证码对不存在邮箱永远核销失败，失败响应与
// 「邮箱存在但验证码错误」完全一致；发送入口在 mailer 未配置时一律 500，
// 在已配置时一律 200（发信故障只进日志），两条路径都不因账号是否存在而分化。

// 验证码的用途与寿命。
const (
	otpPurposeReset = "reset"
	otpPurposeErase = "erase"
	// otpTTLSeconds 是验证码寿命：短到限制泄露窗口，长到覆盖一封邮件的到达时间。
	otpTTLSeconds = 5 * 60
	// otpTTLMinutes 是寿命的分钟表达，用于邮件正文，避免正文里出现裸除法。
	otpTTLMinutes = otpTTLSeconds / 60
	// otpDigits 是验证码位数；6 位在「每分钟限频 + 一次性核销」下足够抗爆破。
	otpDigits = 6
	// otpBase 是验证码的进制基数。
	otpBase = 10
)

// ErrInvalidOTP 表示验证码错误、过期或已使用；三种情形对客户端无差别。
var ErrInvalidOTP = errors.New("auth: 验证码无效")

// SendOtp 生成并投递一枚验证码。
//
// mailer 未配置时在任何查询之前返回 ErrMailerNotConfigured：
// 先查后判会让「邮箱存在与否」通过 500 与 200 的差异泄漏出去。
func (s *Service) SendOtp(ctx context.Context, addr, email, purpose string) error {
	if !s.limiter.allow("otp:" + addr) {
		return ErrRateLimited
	}
	if purpose != otpPurposeReset && purpose != otpPurposeErase {
		return ErrInvalidParams
	}
	email = strings.TrimSpace(email)
	if err := validateEmail(email); err != nil {
		return err
	}
	if s.mailer == nil {
		return ErrMailerNotConfigured
	}

	user, err := s.store.WebUserByEmail(ctx, email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// 查询故障照常上抛：把它折进「成功」会让发不出信的故障静默下去。
		return err
	}
	if !userActive(user) {
		// 账号不存在或不可用：对外与成功完全一致，内部不发信。
		return nil
	}

	code, err := randomOTP()
	if err != nil {
		return err
	}
	expiresAt := s.opts.Now().Add(time.Duration(otpTTLSeconds) * time.Second)
	if err := s.store.WebInsertOTP(ctx, email, purpose, hashOTP(email, purpose, code), expiresAt); err != nil {
		return err
	}
	subject := "TokenMP 验证码"
	if purpose == otpPurposeErase {
		subject = "TokenMP 注销账号验证码"
	}
	body := fmt.Sprintf("验证码：%s\n%d 分钟内有效，请勿告知他人。\n若非本人操作请忽略本邮件。",
		code, otpTTLMinutes)
	// 发信失败只记日志：把失败回给客户端会让「存在且发信故障」与「不存在」可区分。
	if err := s.mailer.Send(ctx, email, subject, body); err != nil && s.logger != nil {
		s.logger.Warn("验证码投递失败", "purpose", purpose, "err", err)
	}
	return nil
}

// Reset 用 OTP 校验后重置密码，并撤销该账号全部会话。
func (s *Service) Reset(ctx context.Context, addr, email, otp, passwordCipher, fingerprint string) error {
	if !s.limiter.allow("reset:" + addr) {
		return ErrRateLimited
	}
	email = strings.TrimSpace(email)
	if err := validateEmail(email); err != nil {
		return err
	}
	plain, err := s.decryptPassword(fingerprint, passwordCipher)
	if err != nil {
		return err
	}
	defer clear(plain)

	if otpErr := s.consumeOTP(ctx, email, otpPurposeReset, otp); otpErr != nil {
		return otpErr
	}
	user, err := s.store.WebUserByEmail(ctx, email)
	if err != nil || !userActive(user) {
		// OTP 已核销说明它曾发给存在的账号；这里兜底为同一错误，避免状态推断外泄。
		return ErrInvalidOTP
	}
	hash, err := bcrypt.GenerateFromPassword(plain, bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := s.store.WebUpdatePassword(ctx, user.ID, string(hash)); err != nil {
		return err
	}
	// 重置密码等价于「凭据可能已失窃」：全量吊销会话，包括发起重置的那台设备。
	return s.store.WebRevokeUserSessions(ctx, user.ID)
}

// ChangePassword 校验当前密码后设置新密码；只吊销其余会话，当前登录保留。
//
// 新旧密码各经一次 challenge 取公钥：两把私钥独立、各自一次性，
// 任一指纹的密钥过期都回 410，客户端重取对应的一把重试。
func (s *Service) ChangePassword(ctx context.Context, accessToken,
	currentCipher, currentFingerprint, newCipher, newFingerprint string) error {
	row, err := s.store.WebSessionByAccess(ctx, hashToken(accessToken))
	if err != nil || !sessionUsable(row, s.opts.Now()) {
		return ErrUnauthorized
	}
	if row.User.PasswordHash == "" {
		// 仅第三方登录的账号没有当前密码可校验；首次设密码走重置路径。
		return ErrInvalidCredentials
	}

	currentPlain, err := s.decryptPassword(currentFingerprint, currentCipher)
	if err != nil {
		return err
	}
	defer clear(currentPlain)
	if cmpErr := bcrypt.CompareHashAndPassword([]byte(row.User.PasswordHash), currentPlain); cmpErr != nil {
		return ErrInvalidCredentials
	}

	newPlain, err := s.decryptPassword(newFingerprint, newCipher)
	if err != nil {
		return err
	}
	defer clear(newPlain)
	hash, err := bcrypt.GenerateFromPassword(newPlain, bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := s.store.WebUpdatePassword(ctx, row.User.ID, string(hash)); err != nil {
		return err
	}
	return s.store.WebRevokeOtherSessions(ctx, row.User.ID, row.Session.ID)
}

// Erase 用 OTP 校验后注销账号：抹除密码与第三方绑定、撤销全部会话。
func (s *Service) Erase(ctx context.Context, addr, accessToken, otp string) error {
	if !s.limiter.allow("erase:" + addr) {
		return ErrRateLimited
	}
	row, err := s.store.WebSessionByAccess(ctx, hashToken(accessToken))
	if err != nil || !sessionUsable(row, s.opts.Now()) {
		return ErrUnauthorized
	}
	if err := s.consumeOTP(ctx, row.User.Email, otpPurposeErase, otp); err != nil {
		return err
	}
	if err := s.store.WebEraseUser(ctx, row.User.ID); err != nil {
		return err
	}
	return s.store.WebRevokeUserSessions(ctx, row.User.ID)
}

// consumeOTP 校验并核销验证码；任何不匹配统一回 ErrInvalidOTP。
func (s *Service) consumeOTP(ctx context.Context, email, purpose, otp string) error {
	otp = strings.TrimSpace(otp)
	if otp == "" {
		return ErrInvalidOTP
	}
	ok, err := s.store.WebConsumeOTP(ctx, email, purpose, hashOTP(email, purpose, otp), s.opts.Now())
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidOTP
	}
	return nil
}

// randomOTP 生成 otpDigits 位十进制验证码；密码学随机源失败直接报错，不退化到弱随机。
func randomOTP() (string, error) {
	max := new(big.Int).Exp(big.NewInt(otpBase), big.NewInt(int64(otpDigits)), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", fmt.Errorf("auth: 生成验证码失败: %w", err)
	}
	return fmt.Sprintf("%0*d", otpDigits, n.Int64()), nil
}

// hashOTP 计算验证码存储哈希：邮箱与用途参与摘要，防跨邮箱、跨用途的哈希复用。
//
// 不引入慢哈希：验证码是一次性短寿随机值，攻击面由限频与核销语义收敛，
// 而不是靠 KDF 拖慢 —— 那会让每次核销都占用请求处理时间。
func hashOTP(email, purpose, otp string) string {
	sum := sha256.Sum256([]byte(email + "\x00" + purpose + "\x00" + otp))
	return hex.EncodeToString(sum[:])
}

// validateEmail 校验邮箱形态。
//
// 用标准库解析器做基础校验，再要求原文与解析结果一致且域名带点号：
// 只挡明显笔误，不替下游服务商验证可达性 —— 那是发信环节的职责。
func validateEmail(email string) error {
	if len(email) == 0 || len(email) > 255 {
		return ErrInvalidParams
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || !strings.EqualFold(addr.Address, email) || !strings.Contains(email, ".") {
		return ErrInvalidParams
	}
	return nil
}
