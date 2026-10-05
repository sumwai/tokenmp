package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/billing"
)

// fakeRepo 是 Repo 的内存实现。
//
// InTx 期间持锁，模拟 account 行的 SELECT ... FOR UPDATE：同一账户的并发结算被串行化，
// 事务内读到的是上一次提交后的状态。失败时丢弃改动，与回滚同义。
type fakeRepo struct {
	mu                sync.Mutex
	accountMultiplier decimal.Decimal
	channelMultiplier decimal.Decimal
	pricing           *Pricing
	rules             map[scopeRef][]Rule
	dayKinds          map[string]billing.DayKind
	buckets           []Bucket
	insertErr         error

	inserted []Usage
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		accountMultiplier: decimal.NewFromInt(1),
		channelMultiplier: decimal.NewFromInt(1),
	}
}

func (r *fakeRepo) InTx(ctx context.Context, fn func(context.Context, Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx := &fakeTx{repo: r, buckets: append([]Bucket(nil), r.buckets...)}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	r.buckets = tx.buckets
	r.inserted = append(r.inserted, tx.inserted...)
	return nil
}

func (r *fakeRepo) AccountBuckets(_ context.Context, _ uint64) ([]Bucket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Bucket(nil), r.buckets...), nil
}

// snapshotInserted 返回已落库流水的一份拷贝。
func (r *fakeRepo) snapshotInserted() []Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Usage(nil), r.inserted...)
}

// fakeTx 在事务内读写 fakeRepo 的工作副本。
type fakeTx struct {
	repo     *fakeRepo
	buckets  []Bucket
	inserted []Usage
}

func (t *fakeTx) LockAccount(context.Context, uint64) (decimal.Decimal, error) {
	return t.repo.accountMultiplier, nil
}

func (t *fakeTx) ActivePricing(context.Context, uint64, string, time.Time) (*Pricing, error) {
	if t.repo.pricing == nil {
		return nil, ErrNoPricing
	}
	return t.repo.pricing, nil
}

func (t *fakeTx) PriceRulesByScope(_ context.Context, scope billing.Scope, scopeID uint64) ([]Rule, error) {
	return t.repo.rules[scopeRef{scope: scope, id: scopeID}], nil
}

func (t *fakeTx) ModelMapMultiplier(context.Context, uint64, string) (decimal.Decimal, error) {
	return t.repo.channelMultiplier, nil
}

func (t *fakeTx) CalendarDay(_ context.Context, calendar, _ string) (billing.DayKind, bool, error) {
	kind, ok := t.repo.dayKinds[calendar]
	return kind, ok, nil
}

func (t *fakeTx) LockBuckets(context.Context, uint64) ([]Bucket, error) {
	return append([]Bucket(nil), t.buckets...), nil
}

func (t *fakeTx) InsertUsage(_ context.Context, row Usage) (uint64, error) {
	if t.repo.insertErr != nil {
		return 0, t.repo.insertErr
	}
	t.inserted = append(t.inserted, row)
	return uint64(len(t.inserted)), nil
}

func (t *fakeTx) UpdateBucketRemaining(_ context.Context, bucketID uint64, remaining decimal.Decimal) error {
	for i := range t.buckets {
		if t.buckets[i].ID == bucketID {
			t.buckets[i].Remaining = remaining
			return nil
		}
	}
	return errors.New("账本不存在")
}

// input 构造一份最小的结算输入。
func input(usage map[billing.Metric]int) Input {
	return Input{
		RequestID:      "req-1",
		MerchantID:     1,
		AccountID:      2,
		ChannelID:      10,
		Model:          "up-model",
		RequestedModel: "alias",
		Usage:          usage,
		AsOf:           mondayMorning,
	}
}

func TestServiceSettleDeductsAndSnapshots(t *testing.T) {
	repo := newFakeRepo()
	repo.pricing = &Pricing{ID: 7, Version: 2, Components: []Component{{
		ID: 11, Metric: billing.MetricInputToken, UnitSettle: billing.UnitSettleCurrency,
		UnitPrice: dec(t, "0.27"), BasisQty: dec(t, "1000000"),
	}}}
	repo.buckets = []Bucket{{
		ID: 5, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "10"),
		Fallback: billing.FallbackChargeBalance,
	}}

	var logs []string
	service := New(repo, func(msg string, args ...any) {
		logs = append(logs, msg)
	})
	if err := service.Settle(context.Background(), input(map[billing.Metric]int{billing.MetricInputToken: 1_000_000})); err != nil {
		t.Fatalf("结算失败：%v", err)
	}

	rows := repo.snapshotInserted()
	if len(rows) != 1 {
		t.Fatalf("流水行数 = %d，期望 1", len(rows))
	}
	row := rows[0]
	if row.PricingID != 7 {
		t.Errorf("pricing_id = %d，期望 7", row.PricingID)
	}
	if row.GrossAmount.String() != "0.27" {
		t.Errorf("gross_amount = %s，期望 0.27", row.GrossAmount)
	}
	if row.Multiplier.String() != "1" {
		t.Errorf("multiplier = %s，期望 1", row.Multiplier)
	}
	if len(row.PricingSnapshot) == 0 || len(row.Settlement) == 0 {
		t.Fatalf("快照与明细不应为空：%s / %s", row.PricingSnapshot, row.Settlement)
	}

	var snapshot pricingSnapshotJSON
	if err := json.Unmarshal(row.PricingSnapshot, &snapshot); err != nil {
		t.Fatalf("解析快照失败：%v", err)
	}
	if snapshot.PricingID != 7 || len(snapshot.Components) != 1 || snapshot.Components[0].UnitPrice != "0.27" {
		t.Errorf("快照内容不符：%#v", snapshot)
	}

	var payload settlementJSON
	if err := json.Unmarshal(row.Settlement, &payload); err != nil {
		t.Fatalf("解析明细失败：%v", err)
	}
	if len(payload.Lines) != 1 || payload.Lines[0].Qty != "0.27" || payload.Lines[0].BucketID != 5 {
		t.Errorf("扣减明细不符：%#v", payload.Lines)
	}
	if len(repo.buckets) != 1 || repo.buckets[0].Remaining.String() != "9.73" {
		t.Errorf("账本余量 = %#v，期望 9.73", repo.buckets)
	}
	if len(logs) != 0 {
		t.Errorf("正常结算不应记日志：%v", logs)
	}
}

func TestServiceSettleNoPricing(t *testing.T) {
	repo := newFakeRepo()
	var logs []string
	service := New(repo, func(msg string, _ ...any) { logs = append(logs, msg) })

	if err := service.Settle(context.Background(), input(map[billing.Metric]int{billing.MetricInputToken: 10})); err != nil {
		t.Fatalf("无定价不应失败：%v", err)
	}
	rows := repo.snapshotInserted()
	if len(rows) != 1 {
		t.Fatalf("应落一行占位流水：%d", len(rows))
	}
	row := rows[0]
	if row.PricingID != 0 || row.GrossAmount.String() != "0" || row.Multiplier.String() != "1" {
		t.Errorf("占位口径不符：pricing=%d gross=%s multiplier=%s", row.PricingID, row.GrossAmount, row.Multiplier)
	}
	if len(row.PricingSnapshot) != 0 || len(row.Settlement) != 0 {
		t.Errorf("占位流水不应带快照与明细")
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "没有生效定价") {
		t.Errorf("应记一条无定价警告：%v", logs)
	}
}

func TestServiceSettleShortfall(t *testing.T) {
	repo := newFakeRepo()
	repo.pricing = &Pricing{ID: 7, Version: 1, Components: []Component{{
		ID: 11, Metric: billing.MetricInputToken, UnitSettle: billing.UnitSettleCurrency,
		UnitPrice: dec(t, "1"), BasisQty: dec(t, "1"),
	}}}
	repo.buckets = []Bucket{{
		ID: 5, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "3"),
		Fallback: billing.FallbackReject,
	}}
	var logs []string
	service := New(repo, func(msg string, _ ...any) { logs = append(logs, msg) })

	if err := service.Settle(context.Background(), input(map[billing.Metric]int{billing.MetricInputToken: 10})); err != nil {
		t.Fatalf("结算失败：%v", err)
	}
	row := repo.snapshotInserted()[0]
	var payload settlementJSON
	if err := json.Unmarshal(row.Settlement, &payload); err != nil {
		t.Fatalf("解析明细失败：%v", err)
	}
	if payload.Shortfall["currency"] != "7" {
		t.Errorf("欠额 = %#v，期望 currency=7", payload.Shortfall)
	}
	if repo.buckets[0].Remaining.String() != "0" {
		t.Errorf("余量 = %s，期望 0", repo.buckets[0].Remaining)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "结算欠额") {
		t.Errorf("应记一条欠额警告：%v", logs)
	}
}

func TestServiceSettleAppliesRuleChain(t *testing.T) {
	repo := newFakeRepo()
	repo.pricing = &Pricing{ID: 7, Version: 1, Components: []Component{{
		ID: 11, Metric: billing.MetricInputToken, UnitSettle: billing.UnitSettleCurrency,
		UnitPrice: dec(t, "1"), BasisQty: dec(t, "1"),
	}}}
	// 三个 scope 各一条命中规则：pricing 2、model_map 3、account 5，乘起来 30。
	repo.rules = map[scopeRef][]Rule{
		{scope: billing.ScopePricing, id: 7}: {{
			ID: 21, Scope: billing.ScopePricing, ScopeID: 7, Multiplier: dec(t, "2"),
			DayKindMask: ptr16(billing.DayKindBitWorkday), Calendar: "cn",
		}},
		{scope: billing.ScopeModelMap, id: 10}: {{
			ID: 22, Scope: billing.ScopeModelMap, ScopeID: 10, Multiplier: dec(t, "3"),
		}},
		{scope: billing.ScopeAccount, id: 2}: {{
			ID: 23, Scope: billing.ScopeAccount, ScopeID: 2, Multiplier: dec(t, "5"),
		}},
	}
	repo.dayKinds = map[string]billing.DayKind{"cn": billing.DayKindWorkday}
	repo.buckets = []Bucket{{
		ID: 5, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "100"),
		Fallback: billing.FallbackChargeBalance,
	}}

	service := New(repo, nil)
	if err := service.Settle(context.Background(), input(map[billing.Metric]int{billing.MetricInputToken: 2})); err != nil {
		t.Fatalf("结算失败：%v", err)
	}
	row := repo.snapshotInserted()[0]
	if row.Multiplier.String() != "30" {
		t.Errorf("倍率 = %s，期望 30", row.Multiplier)
	}
	if row.GrossAmount.String() != "2" {
		t.Errorf("基础金额 = %s，期望 2", row.GrossAmount)
	}
	var payload settlementJSON
	if err := json.Unmarshal(row.Settlement, &payload); err != nil {
		t.Fatalf("解析明细失败：%v", err)
	}
	if len(payload.Lines) != 1 || payload.Lines[0].Qty != "60" {
		t.Errorf("扣减明细 = %#v，期望一行 60", payload.Lines)
	}
}

func TestServiceSettleRejectsBadInput(t *testing.T) {
	repo := newFakeRepo()
	service := New(repo, nil)
	if err := service.Settle(context.Background(), Input{AccountID: 1, ChannelID: 1, Model: "m"}); err == nil {
		t.Fatal("缺少 merchant_id 应被拒绝")
	}
	if len(repo.snapshotInserted()) != 0 {
		t.Errorf("校验失败不应落流水")
	}
}

// TestServiceSettleSerializesSameAccount 覆盖同一账户的并发结算：
// Repo.InTx 串行化后，两个并发请求先到先扣，不会读到同一份余量各扣一次。
func TestServiceSettleSerializesSameAccount(t *testing.T) {
	repo := newFakeRepo()
	repo.pricing = &Pricing{ID: 7, Version: 1, Components: []Component{{
		ID: 11, Metric: billing.MetricInputToken, UnitSettle: billing.UnitSettleCurrency,
		UnitPrice: dec(t, "1"), BasisQty: dec(t, "1"),
	}}}
	repo.buckets = []Bucket{{
		ID: 5, Unit: billing.UnitSettleCurrency, Remaining: dec(t, "10"),
		Fallback: billing.FallbackReject,
	}}
	service := New(repo, nil)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = service.Settle(context.Background(), input(map[billing.Metric]int{billing.MetricInputToken: 4}))
		}()
	}
	wg.Wait()

	// 4 个请求各需 4，总额 16 超过 10：余额不得为负（reject），且总共只扣掉 10。
	if repo.buckets[0].Remaining.IsNegative() {
		t.Fatalf("reject 包不应被扣成负数：%s", repo.buckets[0].Remaining)
	}
	if repo.buckets[0].Remaining.String() != "0" {
		t.Errorf("余量 = %s，期望 0", repo.buckets[0].Remaining)
	}
	totalShortfall := decimal.Zero
	for _, row := range repo.snapshotInserted() {
		var payload settlementJSON
		if err := json.Unmarshal(row.Settlement, &payload); err != nil {
			t.Fatalf("解析明细失败：%v", err)
		}
		if qty, ok := payload.Shortfall["currency"]; ok {
			totalShortfall = totalShortfall.Add(dec(t, qty))
		}
	}
	// 应付 16，实扣 10，欠额 6。
	if totalShortfall.String() != "6" {
		t.Errorf("总欠额 = %s，期望 6", totalShortfall)
	}
}
