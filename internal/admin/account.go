package admin

import (
	"context"
	"fmt"
	"time"

	"github.com/sumwai/tokenmp/internal/apikey"
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
// 生成、前缀与哈希规则见 internal/apikey：与用户自助签发共用一份实现，
// 两处签出的密钥因此都能直接被转发鉴权路径识别。
func (s *Service) IssueKey(ctx context.Context, in IssueKeyInput) (*IssuedKey, error) {
	if err := requireID("密钥 account", in.AccountID); err != nil {
		return nil, err
	}
	if in.MerchantID != nil && *in.MerchantID == 0 {
		return nil, fmt.Errorf("admin: 密钥 merchant 不能为 0")
	}
	plaintext, err := apikey.Generate()
	if err != nil {
		return nil, err
	}
	prefix := apikey.Prefix(plaintext)
	hash := apikey.Hash(plaintext)
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
