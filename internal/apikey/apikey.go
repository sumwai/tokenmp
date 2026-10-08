// Package apikey 实现客户端 API 密钥的生成、展示前缀与存储哈希。
//
// 管理面签发与用户自助签发共用同一份规则：两处生成的密钥必须能被同一条鉴权
// 路径识别，规则一旦分叉就会出现「签得出但用不了」的密钥。明文只在生成瞬间
// 存在，落库的是 SHA-256 十六进制哈希与前缀。
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const (
	// fixedPrefix 是明文密钥的固定前缀，用于肉眼区分网关密钥与上游密钥。
	fixedPrefix = "sk-"
	// randomBytes 是明文的随机字节数；48 个十六进制字符的熵远高于暴力破解门槛。
	randomBytes = 24
	// visiblePrefixLen 是落库前缀列的长度，供展示与 LIKE 前缀检索。
	visiblePrefixLen = 8
)

// Generate 生成一条高熵明文密钥。
func Generate() (string, error) {
	buf := make([]byte, randomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("apikey: 生成密钥失败: %w", err)
	}
	return fixedPrefix + hex.EncodeToString(buf), nil
}

// Hash 计算密钥的存储键：SHA-256 十六进制小写串。
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// Prefix 返回明文的前 visiblePrefixLen 个字符，作为 key_prefix 列的取值。
//
// 存原始前缀而不带省略号：该列供展示与检索，带省略号会让 LIKE 前缀查询失配。
func Prefix(plaintext string) string {
	if len(plaintext) <= visiblePrefixLen {
		return plaintext
	}
	return plaintext[:visiblePrefixLen]
}
