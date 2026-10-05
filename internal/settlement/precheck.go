package settlement

import (
	"time"

	"github.com/sumwai/tokenmp/internal/billing"
)

// 本文件是 402 预检的判定口径：粗粒度，只防「彻底没钱」，不做用量估算。
//
// 精细化额度管控（预扣、冻结、按预估用量拦截）依赖常驻任务与解冻逻辑，另开 issue；
// 这里只回答「这个账户还能不能开出一次请求」。

// Fundable 报告账户是否还有可用额度。
//
// 判定为「还能服务」的条件是存在一个未过期账本，且满足其一：
//
//   - remaining > 0：还有存量可扣；
//   - unit 为 currency 且 fallback=charge_balance：货币账本可透支，余额为 0 也能后付。
//
// 从未充值（没有任何账本行）或只剩耗尽的 reject 包时返回 false。
// 已过期的账本不计入：过期包不再可扣，也就不能作为「还有钱」的依据。
func Fundable(buckets []Bucket, now time.Time) bool {
	for _, bucket := range buckets {
		if bucket.ExpiresAt != nil && !bucket.ExpiresAt.After(now) {
			continue
		}
		if bucket.Remaining.IsPositive() {
			return true
		}
		if bucket.Unit == billing.UnitSettleCurrency && bucket.Fallback == billing.FallbackChargeBalance {
			return true
		}
	}
	return false
}
