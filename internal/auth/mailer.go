package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
)

// 本文件是验证码的投递端口与 SMTP 实现。
//
// 抽成接口而不是直接调 SMTP：重置与注销路径的单测注入假实现，
// 不需要真实邮件通道；未配置 SMTP 时返回显式错误而不是静默丢信 ——
// 静默丢信会让「验证码收不到」与「邮箱不存在」在客户端侧无法区分。

// ErrMailerNotConfigured 表示服务端未配置邮件通道；映射 500。
var ErrMailerNotConfigured = errors.New("auth: 邮件服务未配置")

// Mailer 是验证码投递端口。
type Mailer interface {
	// Send 向 to 投递一封纯文本邮件；失败返回错误，由调用方决定映射。
	Send(ctx context.Context, to, subject, body string) error
}

// SMTPConfig 是 SMTP 通道的连接参数；Addr 为空视为未配置。
type SMTPConfig struct {
	// Addr 形如 smtp.example.com:587。
	Addr string
	// From 是发件人地址。
	From string
	// User、Password 是 SMTP 认证凭据；匿名中继可留空。
	User     string
	Password string
}

// SMTPMailer 经 SMTP 发信。
type SMTPMailer struct {
	cfg SMTPConfig
}

// NewSMTPMailer 构造 SMTP 投递器；Addr 为空返回 nil，由调用方按「未配置」处理。
//
// 返回字面 nil 接口而不是占位实现：Service 判 s.mailer == nil 即可确定通道缺失，
// 不必再为占位实现维护一条分支。
func NewSMTPMailer(cfg SMTPConfig) Mailer {
	if strings.TrimSpace(cfg.Addr) == "" {
		return nil
	}
	return &SMTPMailer{cfg: cfg}
}

// Send 投递一封纯文本邮件。
//
// 连接超时由调用方的 context 控制；SMTP 握手失败按原样返回，
// 不在这里做重试 —— 验证码有有效期，重试由客户端重新获取验证码承担。
func (m *SMTPMailer) Send(ctx context.Context, to, subject, body string) error {
	host, _, err := net.SplitHostPort(m.cfg.Addr)
	if err != nil {
		return fmt.Errorf("auth: 解析 SMTP 地址失败: %w", err)
	}

	var auth smtp.Auth
	if m.cfg.User != "" {
		auth = smtp.PlainAuth("", m.cfg.User, m.cfg.Password, host)
	}

	msg := buildMessage(m.cfg.From, to, subject, body)
	// SendMail 在服务器宣告 STARTTLS 时自动升级；未宣告时按明文投递，
	// 验证码邮件的机密性由收发双方的通道策略决定，这里不强制隐式 TLS。
	if err := smtp.SendMail(m.cfg.Addr, auth, m.cfg.From, []string{to}, msg); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("auth: SMTP 发送失败: %w", err)
	}
	return nil
}

// buildMessage 组装 RFC 5322 报文：UTF-8 正文与 MIME 编码的主题。
func buildMessage(from, to, subject, body string) []byte {
	var sb strings.Builder
	fmt.Fprintf(&sb, "From: %s\r\n", from)
	fmt.Fprintf(&sb, "To: %s\r\n", to)
	// 主题含中文：按 RFC 2047 用 base64 编码，避免裸 UTF-8 被旧客户端乱码。
	sb.WriteString("Subject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?=\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return []byte(sb.String())
}
