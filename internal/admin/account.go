package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件是账户与客户端密钥的管理动作。

// AccountInput 是开户输入。
type AccountInput struct {
	Code              string
	Name              string
	DefaultMerchantID *uint64
	PriceMultiplier   string
}

// CreateAccount 开一个账户。
//
// default_merchant_id 留空表示走平台自营（NULL），与鉴权链的兜底口径一致。
func (s *Service) CreateAccount(ctx context.Context, in AccountInput) (uint64, error) {
	if err := requireString("账户 code", in.Code); err != nil {
		return 0, err
	}
	if err := requireString("账户 name", in.Name); err != nil {
		return 0, err
	}
	multiplier := in.PriceMultiplier
	if multiplier == "" {
		multiplier = defaultMultiplier
	}
	if err := requireDecimal("账户 multiplier", multiplier); err != nil {
		return 0, err
	}
	if in.DefaultMerchantID != nil && *in.DefaultMerchantID == 0 {
		return 0, fmt.Errorf("admin: 账户 default-merchant 不能为 0")
	}
	return s.store.InsertAccount(ctx, store.Account{
		Code:              in.Code,
		Name:              in.Name,
		DefaultMerchantID: in.DefaultMerchantID,
		PriceMultiplier:   multiplier,
		Status:            store.StatusActive,
	})
}

// ListAccounts 列出全部账户。
func (s *Service) ListAccounts(ctx context.Context) ([]store.Account, error) {
	return s.store.ListAccounts(ctx)
}

// DisableAccount 停用一个账户。
func (s *Service) DisableAccount(ctx context.Context, id uint64) error {
	if err := requireID("账户 id", id); err != nil {
		return err
	}
	return s.store.SetAccountStatus(ctx, id, store.StatusDisabled)
}

// SetAccountMultiplier 置位账户倍率。
func (s *Service) SetAccountMultiplier(ctx context.Context, id uint64, multiplier string) error {
	if err := requireID("账户 id", id); err != nil {
		return err
	}
	if err := requireDecimal("账户 multiplier", multiplier); err != nil {
		return err
	}
	return s.store.SetAccountMultiplier(ctx, id, multiplier)
}

// SetAccountMerchant 置位账户默认商家。
func (s *Service) SetAccountMerchant(ctx context.Context, id, merchantID uint64) error {
	if err := requireID("账户 id", id); err != nil {
		return err
	}
	if err := requireID("账户 merchant", merchantID); err != nil {
		return err
	}
	return s.store.SetAccountMerchant(ctx, id, merchantID)
}

// 客户端密钥的生成参数。
const (
	// apiKeyPrefix 是明文密钥的固定前缀，用于肉眼区分网关密钥与上游密钥。
	apiKeyPrefix = "sk-"
	// apiKeyRandomBytes 是明文密钥的随机字节数；48 个十六进制字符的熵远高于暴力破解门槛。
	apiKeyRandomBytes = 24
)

// IssueKeyInput 是签发客户端密钥的输入。
type IssueKeyInput struct {
	AccountID  uint64
	MerchantID *uint64
	Name       string
	ExpiresAt  *time.Time
}

// IssuedKey 是一次签发的产物。
//
// Plaintext 只在本结构体里出现一次；调用方输出后即丢弃，库中只留哈希与前缀。
type IssuedKey struct {
	ID        uint64 `json:"id"`
	AccountID uint64 `json:"account_id"`
	Prefix    string `json:"prefix"`
	Plaintext string `json:"plaintext"`
}

// IssueKey 生成并写入一条客户端密钥，返回只在本次可见的明文。
//
// 明文 = "sk-" + 24 字节 crypto/rand 的十六进制；库中存 SHA-256 十六进制哈希
// 与前缀。哈希格式与 cmd/tokenmp 的鉴权路径一致（SHA-256 hex 小写），
// 签发的密钥因此能直接用于转发鉴权。
func (s *Service) IssueKey(ctx context.Context, in IssueKeyInput) (*IssuedKey, error) {
	if err := requireID("密钥 account", in.AccountID); err != nil {
		return nil, err
	}
	if in.MerchantID != nil && *in.MerchantID == 0 {
		return nil, fmt.Errorf("admin: 密钥 merchant 不能为 0")
	}
	plaintext, err := generateAPIKey()
	if err != nil {
		return nil, err
	}
	prefix := plaintextPrefix(plaintext)
	hash := hashAPIKey(plaintext)
	id, err := s.store.InsertAPIKey(ctx, store.APIKey{
		AccountID:  in.AccountID,
		MerchantID: in.MerchantID,
		Name:       in.Name,
		KeyHash:    hash,
		KeyPrefix:  prefix,
		ExpiresAt:  in.ExpiresAt,
	})
	if err != nil {
		return nil, err
	}
	return &IssuedKey{
		ID:        id,
		AccountID: in.AccountID,
		Prefix:    prefix,
		Plaintext: plaintext,
	}, nil
}

// ListKeys 列出全部客户端密钥，只含前缀不含哈希与明文。
func (s *Service) ListKeys(ctx context.Context) ([]store.APIKey, error) {
	return s.store.ListAPIKeys(ctx)
}

// RevokeKey 吊销一条客户端密钥（enabled = 0）。
func (s *Service) RevokeKey(ctx context.Context, id uint64) error {
	if err := requireID("密钥 id", id); err != nil {
		return err
	}
	return s.store.SetAPIKeyEnabled(ctx, id, false)
}

// generateAPIKey 生成一条高熵明文密钥。
func generateAPIKey() (string, error) {
	buf := make([]byte, apiKeyRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("admin: 生成密钥失败: %w", err)
	}
	return apiKeyPrefix + hex.EncodeToString(buf), nil
}

// hashAPIKey 计算客户端密钥的存储键：SHA-256 十六进制小写串。
func hashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
