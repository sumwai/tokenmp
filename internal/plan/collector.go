package plan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"
)

// 本文件是按 cred_group 去重的周期采集器。
//
// 采集是转发的旁路：任何失败都只记结构化日志并保留旧快照，不返回错误给上层、
// 不阻断任何转发路径。抓不到额度顶多是路由按未知放行，与「不采集」等价。

// CollectorOptions 是采集器的构造参数。
type CollectorOptions struct {
	// Prober 执行一次探针调用；零值即用默认 HTTP 客户端。
	Prober Prober
	// Interval 是两轮采集之间的间隔，必须为正；非法值在构造时报错，不推迟到运行时。
	Interval time.Duration
	// Logger 记采集结果；nil 时不记录。
	Logger *slog.Logger
	// Clock 取当前时刻；nil 时用 time.Now。
	Clock func() time.Time
}

// Collector 周期执行探针并把结果写回套餐。
type Collector struct {
	repo     Repo
	prober   Prober
	interval time.Duration
	logger   *slog.Logger
	clock    func() time.Time
}

// NewCollector 构造采集器。repo 为空或 interval 非正都在构造时报错：
// interval 为 0 会让 time.NewTicker 直接 panic，非法配置必须在启动期暴露。
func NewCollector(repo Repo, opts CollectorOptions) (*Collector, error) {
	if repo == nil {
		return nil, errors.New("plan: 采集器缺少数据面")
	}
	if opts.Interval <= 0 {
		return nil, fmt.Errorf("plan: 采集间隔必须为正，得到 %s", opts.Interval)
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Collector{
		repo:     repo,
		prober:   opts.Prober,
		interval: opts.Interval,
		logger:   opts.Logger,
		clock:    clock,
	}, nil
}

// Run 先立刻采集一轮，随后按间隔重复，直到 ctx 取消。
//
// 启动即采一轮：等一个完整周期才开始，重启后的前几分钟路由对配额完全无感。
func (c *Collector) Run(ctx context.Context) {
	c.CollectOnce(ctx)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.CollectOnce(ctx)
		}
	}
}

// CollectOnce 执行一轮采集：按 cred_group 去重后逐条探针。
func (c *Collector) CollectOnce(ctx context.Context) {
	targets, err := c.repo.ProbeTargets(ctx)
	if err != nil {
		c.warn("读取探针渠道失败，本轮采集跳过", "error", err)
		return
	}
	type targetKey struct {
		merchantID uint64
		credGroup  string
	}
	seen := make(map[targetKey]struct{}, len(targets))
	for _, target := range targets {
		key := targetKey{merchantID: target.MerchantID, credGroup: target.CredGroup}
		if _, duplicated := seen[key]; duplicated {
			continue
		}
		seen[key] = struct{}{}
		if ctx.Err() != nil {
			return
		}
		c.collectTarget(ctx, target)
	}
}

// collectTarget 采集一条去重后的目标。
func (c *Collector) collectTarget(ctx context.Context, target ProbeTarget) {
	cfg, err := ParseChannelConfig(target.Config)
	if err != nil {
		c.warn("渠道探针配置非法，已跳过",
			"channel_id", target.ChannelID, "cred_group", target.CredGroup, "error", err)
		return
	}
	if cfg == nil {
		return
	}
	plan, err := c.repo.PlansByCredGroup(ctx, target.MerchantID, target.CredGroup)
	if err != nil {
		c.warn("渠道声明了探针但没有对应套餐，已跳过",
			"channel_id", target.ChannelID, "cred_group", target.CredGroup, "error", err)
		return
	}
	snapshot, values, err := c.prober.Probe(ctx, *cfg)
	if err != nil {
		c.warn("探针采集失败，保留旧快照",
			"channel_id", target.ChannelID, "cred_group", target.CredGroup, "error", err)
		return
	}
	used, err := matchQuotas(plan.Quotas, values)
	if err != nil {
		c.warn("探针取值与套餐限额行不匹配，保留旧快照",
			"channel_id", target.ChannelID, "cred_group", target.CredGroup, "error", err)
		return
	}
	checkedAt := c.clock()
	if err := c.repo.SaveProbeResult(ctx, plan.ID, snapshot, checkedAt, used); err != nil {
		c.warn("保存采集结果失败，保留旧快照",
			"channel_id", target.ChannelID, "cred_group", target.CredGroup, "error", err)
		return
	}
	c.debug("上游套餐采集完成",
		"channel_id", target.ChannelID, "cred_group", target.CredGroup,
		"plan_id", plan.ID, "quotas", len(used))
}

// matchQuotas 把探针取值按 (metric, window_kind, period) 匹配到套餐限额行。
//
// 套餐里每一条限额行都必须被探针覆盖：只覆盖一部分会让未覆盖的行停留在旧值，
// 而套餐的 last_checked_at 已被刷新，路由会拿一份半新半旧的状态判耗尽。
// 探针多报的取值忽略——它可能是为将来新增限额预留的。
func matchQuotas(quotas []Quota, values []ProbeValue) ([]QuotaUsed, error) {
	type quotaKey struct {
		metric     string
		windowKind string
		period     string
	}
	byKey := make(map[quotaKey]decimal.Decimal, len(values))
	for _, v := range values {
		byKey[quotaKey{metric: string(v.Metric), windowKind: string(v.WindowKind), period: string(v.Period)}] = v.Used
	}
	used := make([]QuotaUsed, 0, len(quotas))
	for _, q := range quotas {
		value, ok := byKey[quotaKey{metric: string(q.Metric), windowKind: string(q.WindowKind), period: string(q.Period)}]
		if !ok {
			return nil, fmt.Errorf("plan: 套餐限额行 %d（%s/%s/%s）没有对应的探针取值",
				q.ID, string(q.Metric), string(q.WindowKind), string(q.Period))
		}
		used = append(used, QuotaUsed{QuotaID: q.ID, Used: value})
	}
	return used, nil
}

// warn 记一条告警日志；未配置 logger 时为空操作。
func (c *Collector) warn(msg string, args ...any) {
	if c.logger == nil {
		return
	}
	c.logger.Warn(msg, args...)
}

// debug 记一条调试日志；未配置 logger 时为空操作。
func (c *Collector) debug(msg string, args ...any) {
	if c.logger == nil {
		return
	}
	c.logger.Debug(msg, args...)
}
