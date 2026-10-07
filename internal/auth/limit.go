package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// 本文件承载三件杂事：按来源的频率限制、令牌生成与哈希、来源地址解析。
// 它们都不碰数据库，因此不进 Service 的业务方法。

// rateLimiter 是进程内滑动窗口限频。
//
// 与 internal/ratelimit 的分工：那边按渠道做令牌桶与并发位，服务转发热路径；
// 这里只保护「每次调用都要做 RSA keygen 或 bcrypt 比对」的认证端点，
// 计数粒度是来源地址，窗口式计数足够，不值得引入桶与突发容量概念。
type rateLimiter struct {
	mu      sync.Mutex
	hits    map[string][]time.Time
	window  time.Duration
	maxHits int
	now     func() time.Time
}

// newRateLimiter 构造滑动窗口限频器；窗口内超过 maxHits 即拒绝。
func newRateLimiter(window time.Duration, maxHits int, now func() time.Time) *rateLimiter {
	return &rateLimiter{
		hits:    make(map[string][]time.Time),
		window:  window,
		maxHits: maxHits,
		now:     now,
	}
}

// allow 记一次调用并报告是否放行。
//
// 拒绝时同样记账：被拒的重试也占窗口额度，否则固定间隔的脚本永远踩不进拒绝分支。
func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := l.now().Add(-l.window)
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.hits[key] = append(kept, l.now())

	// 窗口内计数整体为空间增长兜底：无新调用的键在下一次访问时被上面的裁剪清空，
	// 长期不访问的键由本处在键数过多时统一丢弃，避免空键堆积。
	if len(l.hits) > 1<<16 {
		for k, v := range l.hits {
			if len(v) == 0 {
				delete(l.hits, k)
			}
		}
	}
	return len(l.hits[key]) <= l.maxHits
}

// tokenPair 是一对明文与哈希：明文只回给客户端一次，哈希才是后续查询的钥匙。
type tokenPair struct {
	plain string
	hash  string
}

// tokenBytes 是访问与刷新令牌的随机源长度：32 字节 = 256 位熵。
const tokenBytes = 32

// newTokenPair 生成随机令牌（base64url，无填充）与其 SHA-256 十六进制哈希。
//
// 密码学随机源失败属于进程级异常：直接返回错误由上层映射 500，不退化到弱随机。
func newTokenPair() (tokenPair, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return tokenPair{}, fmt.Errorf("auth: 生成令牌失败: %w", err)
	}
	plain := base64.RawURLEncoding.EncodeToString(buf)
	return tokenPair{plain: plain, hash: hashToken(plain)}, nil
}

// hashToken 计算令牌的存储哈希；库中只存该值。
func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// clientAddr 取用于限频的来源地址。
//
// 默认取直连地址：反代未配置时 X-Forwarded-For 可由客户端任意伪造，
// 按它限频等于把额度交给攻击者调度。仅在 Options.TrustProxy 打开时
// 才采信转发链的首段（部署在可信反代之后的形态）。
func clientAddr(remoteAddr, forwardedFor string, trustProxy bool) string {
	if trustProxy {
		if first := firstForwarded(forwardedFor); first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// firstForwarded 取 X-Forwarded-For 的第一个逗号段。
func firstForwarded(forwarded string) string {
	for i := 0; i < len(forwarded); i++ {
		if forwarded[i] == ',' {
			return strings.TrimSpace(forwarded[:i])
		}
	}
	return strings.TrimSpace(forwarded)
}
