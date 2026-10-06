// Package plan 实现上游套餐与配额的判定与采集：套餐模型、耗尽判定、声明式探针解析、
// JSON 路径映射与周期采集。
//
// 与本仓库既有限额能力的分工：
//
//   - internal/quota 判定面向下游的窗口限额（账户 / API key），数据面是 billing_usage；
//   - 本包判定面向上游的套餐额度，数据面是探针采集回来的快照，窗口计算直接复用
//     internal/quota 的纯函数，不写第二套。
//
// 耗尽判定与采集都刻意不阻断转发：套餐数据缺失、探针不可达、快照过期一律视为「未知」，
// 路由按未知放行，宁可尝试不可误杀。真正的拒绝仍由上游的 429 与下游限额承担。
package plan

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// Quota 是 upstream_plan_quota 的一行：套餐上的一条窗口型限额。
//
// LastUsed 是最近一次采集到的窗口已用量，LimitAmount 是上限；两者都是 DECIMAL，
// 比较不经过 float64。LastCheckedAt 为零值表示该行从未采集成功。
type Quota struct {
	ID            uint64
	PlanID        uint64
	Metric        billing.Metric
	WindowKind    billing.WindowKind
	Period        billing.Period
	LimitAmount   decimal.Decimal
	LastUsed      decimal.Decimal
	LastCheckedAt time.Time
}

// UpstreamPlan 是 upstream_plan 的一行及其限额行。
//
// LastSnapshot 是最近一次采集的原始响应；LastCheckedAt 为零值表示从未采集成功。
type UpstreamPlan struct {
	ID            uint64
	MerchantID    uint64
	CredGroup     string
	Name          string
	Multiplier    decimal.Decimal
	ValidFrom     *time.Time
	ValidTo       *time.Time
	LastSnapshot  []byte
	LastCheckedAt time.Time
	Quotas        []Quota
}

// QuotaUsed 是一次采集里某条限额行的结果。
type QuotaUsed struct {
	QuotaID uint64
	Used    decimal.Decimal
}

// ProbeTarget 是一条声明了探针的启用渠道：采集的去重键是 (MerchantID, CredGroup)。
type ProbeTarget struct {
	ChannelID  uint64
	MerchantID uint64
	CredGroup  string
	// Config 是 upstream_channel.config 原始 JSON；结构由本包解析，存储层不解释。
	Config []byte
}

// Reader 是消费侧的数据面：按商家读套餐与限额行。
type Reader interface {
	// Plans 读某商家下的全部套餐与限额行；merchantID 为 0 时读全部。
	Plans(ctx context.Context, merchantID uint64) ([]UpstreamPlan, error)
}

// Repo 是采集侧的数据面。
type Repo interface {
	// PlansByCredGroup 读某商家某凭据分组下的套餐与限额行；不存在时返回错误。
	PlansByCredGroup(ctx context.Context, merchantID uint64, credGroup string) (*UpstreamPlan, error)
	// ProbeTargets 读全部声明了探针的启用渠道。
	ProbeTargets(ctx context.Context) ([]ProbeTarget, error)
	// SaveProbeResult 保存一次采集结果：套餐快照与每行限额的已用量。
	// 只更新传入了 QuotaUsed 的行；未传入的行保持原值。
	SaveProbeResult(ctx context.Context, planID uint64, snapshot []byte, checkedAt time.Time, used []QuotaUsed) error
}

// Exhausted 报告该套餐是否「全部限额行已用满且快照仍然新鲜」。
//
// 三个条件缺一不可，任一不满足都按未知处理、放行：
//
//   - 快照新鲜：LastCheckedAt 非零且距 now 不超过 maxAge。过期快照可能来自上一个窗口，
//     用它判耗尽会把已经恢复的渠道误杀。
//   - 至少有一条限额行：没有限额行的套餐无从判定耗尽，按未知放行。
//   - 每一条限额行的 LastUsed 都不小于 LimitAmount。限额是上限，达到即算满（>= 而非 >），
//     与 internal/quota 的 Exceeded 同口径。
func Exhausted(p UpstreamPlan, now time.Time, maxAge time.Duration) bool {
	if p.LastCheckedAt.IsZero() {
		return false
	}
	if maxAge > 0 && now.Sub(p.LastCheckedAt) > maxAge {
		return false
	}
	if len(p.Quotas) == 0 {
		return false
	}
	for _, q := range p.Quotas {
		if q.LastUsed.LessThan(q.LimitAmount) {
			return false
		}
	}
	return true
}

// ExhaustedCredGroups 从套餐列表算出「配额耗尽且快照新鲜」的凭据分组集合。
//
// 路由候选按 CredGroup 命中该集合时跳过；集合为空表示没有可判定的耗尽渠道。
// maxAge 为 0 表示不做过期检查（只用于测试，生产一律传两个采集周期）。
func ExhaustedCredGroups(plans []UpstreamPlan, now time.Time, maxAge time.Duration) map[string]struct{} {
	exhausted := make(map[string]struct{})
	for _, p := range plans {
		if p.CredGroup == "" {
			continue
		}
		if Exhausted(p, now, maxAge) {
			exhausted[p.CredGroup] = struct{}{}
		}
	}
	return exhausted
}
