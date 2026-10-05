package main

import (
	"context"

	"github.com/sumwai/tokenmp/internal/access"
)

// 本文件是测试侧的临时别名：鉴权与错误体常量下沉到 internal/access 后，
// 命令包内的既有测试仍以原名引用。测试随实现移到 internal/gateway 后本文件删除。

const (
	authorizationHeader = access.AuthorizationHeader
	authSchemePrefix    = access.AuthSchemePrefix
	jsonContentType     = access.JSONContentType
	retryAfterHeader    = access.RetryAfterHeader
	accountStatusActive = access.AccountStatusActive
)

// errorEnvelope 是共享错误体的测试别名。
type errorEnvelope = access.ErrorEnvelope

// identity 是鉴权归属的测试别名，字段保持小写以便既有用例原样构造。
type identity struct {
	accountID  uint64
	merchantID uint64
	apiKeyID   uint64
}

// hashAPIKey 转发到 access 的哈希实现。
func hashAPIKey(key string) string { return access.HashAPIKey(key) }

// withIdentity 把测试身份的字段映射到 access 的归属结构。
func withIdentity(ctx context.Context, id identity) context.Context {
	return access.WithIdentity(ctx, access.Identity{
		AccountID:  id.accountID,
		MerchantID: id.merchantID,
		APIKeyID:   id.apiKeyID,
	})
}
