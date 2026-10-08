package apikey

import (
	"strings"
	"testing"
)

// 本文件固定密钥生成、哈希与前缀的规则：这三者一旦变化，已签发的密钥会失效。

// TestGenerateShape 断言明文由固定前缀与随机十六进制组成，且两次生成不重复。
func TestGenerateShape(t *testing.T) {
	plaintext, err := Generate()
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if !strings.HasPrefix(plaintext, fixedPrefix) {
		t.Errorf("明文 = %q，期望以 %q 开头", plaintext, fixedPrefix)
	}
	if want := len(fixedPrefix) + randomBytes*2; len(plaintext) != want {
		t.Errorf("明文长度 = %d，期望 %d", len(plaintext), want)
	}

	second, err := Generate()
	if err != nil {
		t.Fatalf("第二次生成失败：%v", err)
	}
	if second == plaintext {
		t.Error("两次生成不应得到同一明文")
	}
}

// TestHashIsStableAndFullLength 断言哈希稳定、定长，且不同明文不碰撞。
func TestHashIsStableAndFullLength(t *testing.T) {
	const key = "sk-0123456789abcdef"
	if got, again := Hash(key), Hash(key); got != again {
		t.Error("同一明文两次哈希应相同")
	}
	if got := Hash(key); len(got) != 64 {
		t.Errorf("哈希长度 = %d，期望 64", len(got))
	}
	if Hash(key) == Hash(key+"x") {
		t.Error("不同明文不应得到同一哈希")
	}
}

// TestPrefixKeepsVisibleLen 断言前缀定长、不含完整随机部分，且短输入原样返回。
func TestPrefixKeepsVisibleLen(t *testing.T) {
	plaintext, err := Generate()
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	prefix := Prefix(plaintext)
	if len(prefix) != visiblePrefixLen {
		t.Errorf("前缀长度 = %d，期望 %d", len(prefix), visiblePrefixLen)
	}
	if strings.Contains(prefix, plaintext[len(fixedPrefix):]) {
		t.Errorf("前缀泄露了随机部分：%q", prefix)
	}
	if got := Prefix("sk-1"); got != "sk-1" {
		t.Errorf("短输入 = %q，期望原样返回", got)
	}
}
