package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

// 本文件实现按客户端指纹索引的一次性密码加密密钥。
//
// 密钥对每次现生成、只存内存、取出即销毁：私钥不落库、不进日志，
// 进程重启即全部失效 —— 客户端拿到 410 重取即可，无需恢复机制。
// 以指纹为索引而不是回传随机 id，是为了让客户端在重试路径上零状态：
// 同一指纹重新取密钥会覆盖旧值，服务端与客户端不需要协商「当前该用哪把」。

// publicKeyData 是 challenge 端点的响应数据，字段与契约 ChallengeData 对齐。
type publicKeyData struct {
	Algorithm string `json:"algorithm"`
	Hash      string `json:"hash"`
	PublicKey string `json:"public_key"`
	ExpiresIn int    `json:"expires_in"`
}

// challengeMaxEntries 是内存表的硬上限：防内存膨胀的兜底，不是容量规划。
//
// 正常流量先被按来源的频率限制挡在外面，走到上限说明限频参数配得过松，
// 宁可丢弃新密钥也不让内存无界增长。
const challengeMaxEntries = 4096

// rsaKeyBits 是一次性密钥的模长；2048 是当前通用安全基线。
const rsaKeyBits = 2048

// challengeEntry 是一条未消费的一次性密钥。
type challengeEntry struct {
	privateKey *rsa.PrivateKey
	expiresAt  time.Time
}

// challengeSet 是指纹 → 一次性密钥的内存表。
//
// 不做持久化的取舍：私钥寿命以分钟计，持久化换来的是「重启不失效」，
// 代价是密钥进库与清理任务；客户端一次 410 重试就覆盖了重启场景。
type challengeSet struct {
	mu         sync.Mutex
	entries    map[string]challengeEntry
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
}

// newChallengeSet 构造内存表；maxEntries 是防内存膨胀的硬上限。
func newChallengeSet(ttl time.Duration, now func() time.Time) *challengeSet {
	return &challengeSet{
		entries: make(map[string]challengeEntry),
		ttl:     ttl,
		// 上限已满且无过期可清时的处置见 challengeMaxEntries 注释。
		maxEntries: challengeMaxEntries,
		now:        now,
	}
}

// put 写入或覆盖同一指纹的密钥，返回公钥信息。
//
// 覆盖是契约行为：客户端重试时用同一指纹重取，旧私钥立刻作废。
func (c *challengeSet) put(fingerprint string) (*publicKeyData, error) {
	key, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
	if err != nil {
		return nil, errors.New("auth: 生成 RSA 密钥失败")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked()

	if len(c.entries) >= c.maxEntries {
		// 上限已满且无过期可清：丢弃任意一条最旧语义不值得引入序号结构，
		// 直接清空整表 —— 全部客户端至多多一次 410 重取。
		c.entries = make(map[string]challengeEntry)
	}
	expiresAt := c.now().Add(c.ttl)
	c.entries[fingerprint] = challengeEntry{privateKey: key, expiresAt: expiresAt}

	// 公钥取 SPKI DER 的 base64，与 WebCrypto importKey('spki', …) 的输入一致。
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("auth: 序列化公钥失败: %w", err)
	}
	return &publicKeyData{
		Algorithm: "RSA-OAEP",
		Hash:      "SHA-256",
		PublicKey: base64.StdEncoding.EncodeToString(der),
		ExpiresIn: int(c.ttl / time.Second),
	}, nil
}

// take 取出并销毁指纹对应的私钥；不存在或已过期返回 ErrChallengeExpired。
//
// 取出即删是「一次性」的全部实现：同一次登录的第二次解密请求拿不到同一把私钥。
func (c *challengeSet) take(fingerprint string) (*rsa.PrivateKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked()

	entry, ok := c.entries[fingerprint]
	if !ok {
		return nil, ErrChallengeExpired
	}
	delete(c.entries, fingerprint)
	if c.now().After(entry.expiresAt) {
		return nil, ErrChallengeExpired
	}
	return entry.privateKey, nil
}

// sweepLocked 清理过期项；调用方必须持有锁。
func (c *challengeSet) sweepLocked() {
	now := c.now()
	for fp, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, fp)
		}
	}
}

// decryptWith 解 OAEP 密文，哈希固定 SHA-256。
//
// base64 解码失败、填充或密钥不匹配的失败一律折叠成同一个错误返回：
// 客户端拿不到「错在哪一步」，避免把密文探测面暴露出去。
func decryptWith(privateKey *rsa.PrivateKey, cipherB64 string) ([]byte, error) {
	cipherText, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil || len(cipherText) == 0 {
		return nil, ErrInvalidParams
	}
	plain, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, cipherText, nil)
	if err != nil {
		return nil, ErrInvalidParams
	}
	return plain, nil
}
