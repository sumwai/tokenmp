package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/sumwai/tokenmp/internal/apikey"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/plan"
	"github.com/sumwai/tokenmp/internal/quota"
	"github.com/sumwai/tokenmp/internal/store"
)

// fakeStore 是 Store 的记录型假实现：默认返回零值，测试按需覆盖单个方法。
//
// 每个方法都记下调用名，用于断言「非法输入没有触达存储层」与「合法输入调到了哪个动作」。
// 不用 testify 之类的替身库：本仓库的测试不使用断言库。
type fakeStore struct {
	calls []string

	insertMerchant       func(context.Context, store.Merchant) (uint64, error)
	listMerchants        func(context.Context) ([]store.Merchant, error)
	merchantByID         func(context.Context, uint64) (*store.Merchant, error)
	updateMerchant       func(context.Context, uint64, string, string, store.MerchantKind) error
	setMerchantStatus    func(context.Context, uint64, string) error
	merchantSettleInfo   func(context.Context, uint64) ([]byte, error)
	setMerchantSettle    func(context.Context, uint64, []byte) error
	settlementFacts      func(context.Context, uint64, time.Time, time.Time) (store.SettlementFacts, error)
	setMerchantOwner     func(context.Context, uint64, uint64) error
	insertChannel        func(context.Context, store.Channel) (uint64, error)
	listChannels         func(context.Context) ([]store.Channel, error)
	channelByID          func(context.Context, uint64) (*store.Channel, error)
	updateChannel        func(context.Context, store.Channel) error
	setChannelEnabled    func(context.Context, uint64, bool) error
	insertCredential     func(context.Context, store.CredentialRow) (uint64, error)
	listCredentials      func(context.Context) ([]store.CredentialRow, error)
	credentialByID       func(context.Context, uint64) (*store.CredentialRow, error)
	updateCredential     func(context.Context, store.CredentialRow) error
	setCredentialEnabled func(context.Context, uint64, bool) error
	channelConfigByGroup func(context.Context, uint64, string) ([]byte, error)
	upsertModelMap       func(context.Context, store.ModelMap) (uint64, error)
	updateModelMap       func(context.Context, store.ModelMap) error
	listModelMaps        func(context.Context) ([]store.ModelMap, error)
	modelMapByID         func(context.Context, uint64) (*store.ModelMap, error)
	setModelMapEnabled   func(context.Context, uint64, bool) error
	insertAccount        func(context.Context, store.Account) (uint64, error)
	listAccounts         func(context.Context) ([]store.Account, error)
	setAccountStatus     func(context.Context, uint64, string) error
	setAccountMultiplier func(context.Context, uint64, string) error
	setAccountMerchant   func(context.Context, uint64, uint64) error
	insertAPIKey         func(context.Context, store.APIKey) (uint64, error)
	listAPIKeys          func(context.Context) ([]store.APIKey, error)
	setAPIKeyEnabled     func(context.Context, uint64, bool) error
	webUserByID          func(context.Context, uint64) (*store.WebUser, error)
	insertBucket         func(context.Context, store.BucketRow) (uint64, error)
	listBuckets          func(context.Context, uint64) ([]store.BucketRow, error)
	insertProduct        func(context.Context, store.Product) (uint64, error)
	listProducts         func(context.Context) ([]store.Product, error)
	getProduct           func(context.Context, uint64) (*store.Product, error)
	createPurchase       func(context.Context, store.Purchase, store.BucketRow) (uint64, uint64, error)
	listPurchases        func(context.Context, uint64) ([]store.Purchase, error)
	publishPricing       func(context.Context, uint64, string, time.Time, []store.PriceComponent) (*store.Pricing, error)
	listPricing          func(context.Context, uint64, string) ([]store.Pricing, error)
	insertPriceRule      func(context.Context, store.PriceRule) (uint64, error)
	priceRulesByScope    func(context.Context, billing.Scope, uint64) ([]store.PriceRule, error)
	deletePriceRule      func(context.Context, uint64) error
	upsertCalendarDays   func(context.Context, string, []store.CalendarDay) error
	listCalendarDays     func(context.Context, string) ([]store.CalendarDay, error)
	listUsage            func(context.Context, uint64, time.Time) ([]store.UsageListRow, error)
	insertAdjustment     func(context.Context, store.Adjustment) (uint64, error)
	listAdjustments      func(context.Context, uint64) ([]store.Adjustment, error)
	insertQuota          func(context.Context, store.Quota) (uint64, error)
	insertQuotaEvent     func(context.Context, store.QuotaEvent) (uint64, error)
	deleteQuota          func(context.Context, uint64) error
	quotas               func(context.Context, billing.Scope, uint64) ([]quota.Limit, error)
	quotaUsage           func(context.Context, quota.UsageQuery) (decimal.Decimal, error)
	insertUpstreamPlan   func(context.Context, plan.UpstreamPlan, []plan.Quota) (uint64, error)
	plans                func(context.Context, uint64) ([]plan.UpstreamPlan, error)
}

// record 记下一次调用并返回调用名是否已记录。
func (f *fakeStore) record(name string) { f.calls = append(f.calls, name) }

func (f *fakeStore) InsertMerchant(ctx context.Context, m store.Merchant) (uint64, error) {
	f.record("InsertMerchant")
	if f.insertMerchant != nil {
		return f.insertMerchant(ctx, m)
	}
	return 1, nil
}

func (f *fakeStore) ListMerchants(ctx context.Context) ([]store.Merchant, error) {
	f.record("ListMerchants")
	if f.listMerchants != nil {
		return f.listMerchants(ctx)
	}
	return nil, nil
}

func (f *fakeStore) SetMerchantStatus(ctx context.Context, id uint64, status string) error {
	f.record("SetMerchantStatus")
	if f.setMerchantStatus != nil {
		return f.setMerchantStatus(ctx, id, status)
	}
	return nil
}

func (f *fakeStore) MerchantByID(ctx context.Context, id uint64) (*store.Merchant, error) {
	f.record("MerchantByID")
	if f.merchantByID != nil {
		return f.merchantByID(ctx, id)
	}
	// 缺省认为这一行存在：引用校验不是每个用例的关注点，需要不存在时由用例覆盖。
	return &store.Merchant{}, nil
}

func (f *fakeStore) UpdateMerchant(ctx context.Context, id uint64, code, name string, kind store.MerchantKind) error {
	f.record("UpdateMerchant")
	if f.updateMerchant != nil {
		return f.updateMerchant(ctx, id, code, name, kind)
	}
	return nil
}

func (f *fakeStore) MerchantSettleInfo(ctx context.Context, id uint64) ([]byte, error) {
	f.record("MerchantSettleInfo")
	if f.merchantSettleInfo != nil {
		return f.merchantSettleInfo(ctx, id)
	}
	return nil, nil
}

func (f *fakeStore) SetMerchantSettleInfo(ctx context.Context, id uint64, raw []byte) error {
	f.record("SetMerchantSettleInfo")
	if f.setMerchantSettle != nil {
		return f.setMerchantSettle(ctx, id, raw)
	}
	return nil
}

func (f *fakeStore) SetMerchantOwner(ctx context.Context, id, userID uint64) error {
	f.record("SetMerchantOwner")
	if f.setMerchantOwner != nil {
		return f.setMerchantOwner(ctx, id, userID)
	}
	return nil
}

func (f *fakeStore) MerchantSettlementFacts(ctx context.Context, merchantID uint64, from, to time.Time) (store.SettlementFacts, error) {
	f.record("MerchantSettlementFacts")
	if f.settlementFacts != nil {
		return f.settlementFacts(ctx, merchantID, from, to)
	}
	return store.SettlementFacts{MerchantID: merchantID}, nil
}

func (f *fakeStore) InsertChannel(ctx context.Context, c store.Channel) (uint64, error) {
	f.record("InsertChannel")
	if f.insertChannel != nil {
		return f.insertChannel(ctx, c)
	}
	return 1, nil
}

func (f *fakeStore) ListChannels(ctx context.Context) ([]store.Channel, error) {
	f.record("ListChannels")
	if f.listChannels != nil {
		return f.listChannels(ctx)
	}
	return nil, nil
}

func (f *fakeStore) SetChannelEnabled(ctx context.Context, id uint64, enabled bool) error {
	f.record("SetChannelEnabled")
	if f.setChannelEnabled != nil {
		return f.setChannelEnabled(ctx, id, enabled)
	}
	return nil
}

func (f *fakeStore) ChannelByID(ctx context.Context, id uint64) (*store.Channel, error) {
	f.record("ChannelByID")
	if f.channelByID != nil {
		return f.channelByID(ctx, id)
	}
	return &store.Channel{}, nil
}

func (f *fakeStore) UpdateChannel(ctx context.Context, c store.Channel) error {
	f.record("UpdateChannel")
	if f.updateChannel != nil {
		return f.updateChannel(ctx, c)
	}
	return nil
}

func (f *fakeStore) InsertCredential(ctx context.Context, c store.CredentialRow) (uint64, error) {
	f.record("InsertCredential")
	if f.insertCredential != nil {
		return f.insertCredential(ctx, c)
	}
	return 1, nil
}

func (f *fakeStore) ListCredentials(ctx context.Context) ([]store.CredentialRow, error) {
	f.record("ListCredentials")
	if f.listCredentials != nil {
		return f.listCredentials(ctx)
	}
	return nil, nil
}

func (f *fakeStore) SetCredentialEnabled(ctx context.Context, id uint64, enabled bool) error {
	f.record("SetCredentialEnabled")
	if f.setCredentialEnabled != nil {
		return f.setCredentialEnabled(ctx, id, enabled)
	}
	return nil
}

func (f *fakeStore) CredentialByID(ctx context.Context, id uint64) (*store.CredentialRow, error) {
	f.record("CredentialByID")
	if f.credentialByID != nil {
		return f.credentialByID(ctx, id)
	}
	return &store.CredentialRow{}, nil
}

func (f *fakeStore) UpdateCredential(ctx context.Context, c store.CredentialRow) error {
	f.record("UpdateCredential")
	if f.updateCredential != nil {
		return f.updateCredential(ctx, c)
	}
	return nil
}

func (f *fakeStore) ChannelConfigByCredGroup(ctx context.Context, merchantID uint64, credGroup string) ([]byte, error) {
	f.record("ChannelConfigByCredGroup")
	if f.channelConfigByGroup != nil {
		return f.channelConfigByGroup(ctx, merchantID, credGroup)
	}
	return nil, sql.ErrNoRows
}

func (f *fakeStore) UpsertModelMap(ctx context.Context, m store.ModelMap) (uint64, error) {
	f.record("UpsertModelMap")
	if f.upsertModelMap != nil {
		return f.upsertModelMap(ctx, m)
	}
	return 1, nil
}

func (f *fakeStore) ListModelMaps(ctx context.Context) ([]store.ModelMap, error) {
	f.record("ListModelMaps")
	if f.listModelMaps != nil {
		return f.listModelMaps(ctx)
	}
	return nil, nil
}

func (f *fakeStore) SetModelMapEnabled(ctx context.Context, id uint64, enabled bool) error {
	f.record("SetModelMapEnabled")
	if f.setModelMapEnabled != nil {
		return f.setModelMapEnabled(ctx, id, enabled)
	}
	return nil
}

func (f *fakeStore) ModelMapByID(ctx context.Context, id uint64) (*store.ModelMap, error) {
	f.record("ModelMapByID")
	if f.modelMapByID != nil {
		return f.modelMapByID(ctx, id)
	}
	return &store.ModelMap{}, nil
}

func (f *fakeStore) UpdateModelMap(ctx context.Context, m store.ModelMap) error {
	f.record("UpdateModelMap")
	if f.updateModelMap != nil {
		return f.updateModelMap(ctx, m)
	}
	return nil
}

func (f *fakeStore) InsertAccount(ctx context.Context, a store.Account) (uint64, error) {
	f.record("InsertAccount")
	if f.insertAccount != nil {
		return f.insertAccount(ctx, a)
	}
	return 1, nil
}

func (f *fakeStore) ListAccounts(ctx context.Context) ([]store.Account, error) {
	f.record("ListAccounts")
	if f.listAccounts != nil {
		return f.listAccounts(ctx)
	}
	return nil, nil
}

func (f *fakeStore) SetAccountStatus(ctx context.Context, id uint64, status string) error {
	f.record("SetAccountStatus")
	if f.setAccountStatus != nil {
		return f.setAccountStatus(ctx, id, status)
	}
	return nil
}

func (f *fakeStore) SetAccountMultiplier(ctx context.Context, id uint64, multiplier string) error {
	f.record("SetAccountMultiplier")
	if f.setAccountMultiplier != nil {
		return f.setAccountMultiplier(ctx, id, multiplier)
	}
	return nil
}

func (f *fakeStore) SetAccountMerchant(ctx context.Context, id, merchantID uint64) error {
	f.record("SetAccountMerchant")
	if f.setAccountMerchant != nil {
		return f.setAccountMerchant(ctx, id, merchantID)
	}
	return nil
}

func (f *fakeStore) InsertAPIKey(ctx context.Context, k store.APIKey) (uint64, error) {
	f.record("InsertAPIKey")
	if f.insertAPIKey != nil {
		return f.insertAPIKey(ctx, k)
	}
	return 1, nil
}

func (f *fakeStore) ListAPIKeys(ctx context.Context) ([]store.APIKey, error) {
	f.record("ListAPIKeys")
	if f.listAPIKeys != nil {
		return f.listAPIKeys(ctx)
	}
	return nil, nil
}

func (f *fakeStore) SetAPIKeyEnabled(ctx context.Context, id uint64, enabled bool) error {
	f.record("SetAPIKeyEnabled")
	if f.setAPIKeyEnabled != nil {
		return f.setAPIKeyEnabled(ctx, id, enabled)
	}
	return nil
}

func (f *fakeStore) WebUserByID(ctx context.Context, id uint64) (*store.WebUser, error) {
	f.record("WebUserByID")
	if f.webUserByID != nil {
		return f.webUserByID(ctx, id)
	}
	return &store.WebUser{}, nil
}

func (f *fakeStore) InsertBucket(ctx context.Context, b store.BucketRow) (uint64, error) {
	f.record("InsertBucket")
	if f.insertBucket != nil {
		return f.insertBucket(ctx, b)
	}
	return 1, nil
}

func (f *fakeStore) ListBuckets(ctx context.Context, accountID uint64) ([]store.BucketRow, error) {
	f.record("ListBuckets")
	if f.listBuckets != nil {
		return f.listBuckets(ctx, accountID)
	}
	return nil, nil
}

func (f *fakeStore) InsertProduct(ctx context.Context, p store.Product) (uint64, error) {
	f.record("InsertProduct")
	if f.insertProduct != nil {
		return f.insertProduct(ctx, p)
	}
	return 1, nil
}

func (f *fakeStore) ListProducts(ctx context.Context) ([]store.Product, error) {
	f.record("ListProducts")
	if f.listProducts != nil {
		return f.listProducts(ctx)
	}
	return nil, nil
}

func (f *fakeStore) Product(ctx context.Context, id uint64) (*store.Product, error) {
	f.record("Product")
	if f.getProduct != nil {
		return f.getProduct(ctx, id)
	}
	return nil, errors.New("未设置 Product 假实现")
}

func (f *fakeStore) CreatePurchaseAndBucket(ctx context.Context, p store.Purchase, b store.BucketRow) (uint64, uint64, error) {
	f.record("CreatePurchaseAndBucket")
	if f.createPurchase != nil {
		return f.createPurchase(ctx, p, b)
	}
	return 1, 2, nil
}

func (f *fakeStore) ListPurchases(ctx context.Context, accountID uint64) ([]store.Purchase, error) {
	f.record("ListPurchases")
	if f.listPurchases != nil {
		return f.listPurchases(ctx, accountID)
	}
	return nil, nil
}

func (f *fakeStore) PublishPricing(ctx context.Context, merchantID uint64, model string, effectiveAt time.Time, components []store.PriceComponent) (*store.Pricing, error) {
	f.record("PublishPricing")
	if f.publishPricing != nil {
		return f.publishPricing(ctx, merchantID, model, effectiveAt, components)
	}
	return &store.Pricing{ID: 1, MerchantID: merchantID, Model: model, Version: 1}, nil
}

func (f *fakeStore) ListPricing(ctx context.Context, merchantID uint64, model string) ([]store.Pricing, error) {
	f.record("ListPricing")
	if f.listPricing != nil {
		return f.listPricing(ctx, merchantID, model)
	}
	return nil, nil
}

func (f *fakeStore) InsertPriceRule(ctx context.Context, r store.PriceRule) (uint64, error) {
	f.record("InsertPriceRule")
	if f.insertPriceRule != nil {
		return f.insertPriceRule(ctx, r)
	}
	return 1, nil
}

func (f *fakeStore) PriceRulesByScope(ctx context.Context, scope billing.Scope, scopeID uint64) ([]store.PriceRule, error) {
	f.record("PriceRulesByScope")
	if f.priceRulesByScope != nil {
		return f.priceRulesByScope(ctx, scope, scopeID)
	}
	return nil, nil
}

func (f *fakeStore) DeletePriceRule(ctx context.Context, id uint64) error {
	f.record("DeletePriceRule")
	if f.deletePriceRule != nil {
		return f.deletePriceRule(ctx, id)
	}
	return nil
}

func (f *fakeStore) UpsertCalendarDays(ctx context.Context, calendar string, days []store.CalendarDay) error {
	f.record("UpsertCalendarDays")
	if f.upsertCalendarDays != nil {
		return f.upsertCalendarDays(ctx, calendar, days)
	}
	return nil
}

func (f *fakeStore) ListCalendarDays(ctx context.Context, calendar string) ([]store.CalendarDay, error) {
	f.record("ListCalendarDays")
	if f.listCalendarDays != nil {
		return f.listCalendarDays(ctx, calendar)
	}
	return nil, nil
}

func (f *fakeStore) ListUsage(ctx context.Context, accountID uint64, since time.Time) ([]store.UsageListRow, error) {
	f.record("ListUsage")
	if f.listUsage != nil {
		return f.listUsage(ctx, accountID, since)
	}
	return nil, nil
}

func (f *fakeStore) InsertAdjustment(ctx context.Context, a store.Adjustment) (uint64, error) {
	f.record("InsertAdjustment")
	if f.insertAdjustment != nil {
		return f.insertAdjustment(ctx, a)
	}
	return 1, nil
}

func (f *fakeStore) ListAdjustments(ctx context.Context, accountID uint64) ([]store.Adjustment, error) {
	f.record("ListAdjustments")
	if f.listAdjustments != nil {
		return f.listAdjustments(ctx, accountID)
	}
	return nil, nil
}

func (f *fakeStore) InsertQuota(ctx context.Context, q store.Quota) (uint64, error) {
	f.record("InsertQuota")
	if f.insertQuota != nil {
		return f.insertQuota(ctx, q)
	}
	return 1, nil
}

func (f *fakeStore) InsertQuotaEvent(ctx context.Context, e store.QuotaEvent) (uint64, error) {
	f.record("InsertQuotaEvent")
	if f.insertQuotaEvent != nil {
		return f.insertQuotaEvent(ctx, e)
	}
	return 1, nil
}

func (f *fakeStore) DeleteQuota(ctx context.Context, id uint64) error {
	f.record("DeleteQuota")
	if f.deleteQuota != nil {
		return f.deleteQuota(ctx, id)
	}
	return nil
}

func (f *fakeStore) Quotas(ctx context.Context, scope billing.Scope, scopeID uint64) ([]quota.Limit, error) {
	f.record("Quotas")
	if f.quotas != nil {
		return f.quotas(ctx, scope, scopeID)
	}
	return nil, nil
}

func (f *fakeStore) Usage(ctx context.Context, q quota.UsageQuery) (decimal.Decimal, error) {
	f.record("Usage")
	if f.quotaUsage != nil {
		return f.quotaUsage(ctx, q)
	}
	return decimal.Zero, nil
}

func (f *fakeStore) InsertUpstreamPlan(ctx context.Context, p plan.UpstreamPlan, quotas []plan.Quota) (uint64, error) {
	f.record("InsertUpstreamPlan")
	if f.insertUpstreamPlan != nil {
		return f.insertUpstreamPlan(ctx, p, quotas)
	}
	return 1, nil
}

func (f *fakeStore) Plans(ctx context.Context, merchantID uint64) ([]plan.UpstreamPlan, error) {
	f.record("Plans")
	if f.plans != nil {
		return f.plans(ctx, merchantID)
	}
	return nil, nil
}

// called 报告某动作是否被调用过。
func (f *fakeStore) called(name string) bool {
	for _, call := range f.calls {
		if call == name {
			return true
		}
	}
	return false
}

// fixedNow 是测试用的固定时刻。
var fixedNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// newService 构造带假存储与固定时钟的服务。
func newService(f *fakeStore) *Service {
	return New(f, WithClock(func() time.Time { return fixedNow }))
}

func TestMaskPrefix(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "长值保留前缀", value: "sk-1234567890abcdef", want: "sk-12345…"},
		{name: "八位不保留", value: "sk-12345", want: maskedFull},
		{name: "空值整段打码", value: "  ", want: maskedFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maskPrefix(tt.value); got != tt.want {
				t.Errorf("maskPrefix(%q) = %q，期望 %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestMaskSecret(t *testing.T) {
	got := maskSecret([]byte(`{"api_key":"sk-abcdefghijklmnop"}`))
	if got != "sk-abcde…" {
		t.Errorf("maskSecret = %q，期望保留前缀", got)
	}
	if got := maskSecret([]byte(`not-json`)); got != maskedFull {
		t.Errorf("无法解析时应整段打码，得到 %q", got)
	}
}

func TestCredentialViewHasNoSecret(t *testing.T) {
	encoded, err := json.Marshal(CredentialView{ID: 1, Prefix: "sk-abcde…"})
	if err != nil {
		t.Fatalf("编码失败：%v", err)
	}
	if strings.Contains(string(encoded), "api_key") || strings.Contains(string(encoded), "secret") {
		t.Errorf("凭据视图 JSON 不应含 secret 字段：%s", encoded)
	}
}

func TestParseComponentSpec(t *testing.T) {
	component, err := ParseComponentSpec("input_token:0.27:currency:1000000")
	if err != nil {
		t.Fatalf("合法规格不应报错：%v", err)
	}
	if component.Metric != billing.MetricInputToken || component.UnitSettle != billing.UnitSettleCurrency ||
		component.UnitPrice != "0.27" || component.BasisQty != "1000000" {
		t.Errorf("解析结果不符：%+v", component)
	}

	bad := []string{
		"input_token:0.27:currency",
		"not_a_metric:1:currency:1",
		"input_token:1:usd:1",
		"input_token:notanumber:currency:1",
		"input_token:1:currency:notanumber",
	}
	for _, spec := range bad {
		if _, err := ParseComponentSpec(spec); err == nil {
			t.Errorf("规格 %q 应当被拒绝", spec)
		}
	}
}

func TestParseCalendarImport(t *testing.T) {
	days, err := ParseCalendarImport("# 注释\n\n2026-10-01,holiday\n 2026-09-27 , makeup_workday \n")
	if err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if len(days) != 2 {
		t.Fatalf("解析行数 = %d，期望 2", len(days))
	}
	if days[0].Date != "2026-10-01" || days[0].DayKind != billing.DayKindHoliday {
		t.Errorf("首行不符：%+v", days[0])
	}
	if days[1].DayKind != billing.DayKindMakeupWorkday {
		t.Errorf("次行性质不符：%+v", days[1])
	}

	bad := []string{
		"2026-10-01",
		"2026-13-01,holiday",
		"2026-10-01,vacation",
	}
	for _, text := range bad {
		if _, err := ParseCalendarImport(text); err == nil {
			t.Errorf("输入 %q 应当被拒绝", text)
		}
	}
}

func TestCreateMerchantValidatesBeforeStore(t *testing.T) {
	f := &fakeStore{}
	s := newService(f)

	if _, err := s.CreateMerchant(context.Background(), "", "名", store.MerchantKindPartner); err == nil {
		t.Fatal("空 code 应当被拒绝")
	}
	if _, err := s.CreateMerchant(context.Background(), "c", "名", store.MerchantKind("reseller")); err == nil {
		t.Fatal("未知 kind 应当被拒绝")
	}
	if len(f.calls) != 0 {
		t.Fatalf("非法输入不应触达存储层，实际调用 %v", f.calls)
	}

	id, err := s.CreateMerchant(context.Background(), "partner-1", "入驻商家", store.MerchantKindPartner)
	if err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if id != 1 || !f.called("InsertMerchant") {
		t.Fatalf("应调用 InsertMerchant，得到 id=%d calls=%v", id, f.calls)
	}
}

func TestCreateChannelDefaults(t *testing.T) {
	f := &fakeStore{}
	var got store.Channel
	f.insertChannel = func(_ context.Context, c store.Channel) (uint64, error) {
		got = c
		return 9, nil
	}
	s := newService(f)
	id, err := s.CreateChannel(context.Background(), ChannelInput{
		MerchantID: 1, Name: "ch", Type: store.ChannelTypeOpenAIChat, CredGroup: "g", BaseURL: "https://up",
	})
	if err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if id != 9 || got.Priority != defaultChannelPriority || got.Weight != defaultChannelWeight {
		t.Fatalf("默认路由参数未生效：%+v", got)
	}

	if _, err := s.CreateChannel(context.Background(), ChannelInput{MerchantID: 1, Name: "ch", Type: store.ChannelType("gemini"), CredGroup: "g", BaseURL: "u"}); err == nil {
		t.Fatal("未知协议应当被拒绝")
	}
}

func TestAddCredentialWritesSecretJSON(t *testing.T) {
	f := &fakeStore{}
	var got store.CredentialRow
	f.insertCredential = func(_ context.Context, c store.CredentialRow) (uint64, error) {
		got = c
		return 3, nil
	}
	s := newService(f)
	if _, err := s.AddCredential(context.Background(), CredentialInput{MerchantID: 1, Group: "g", APIKey: "sk-up"}); err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	var parsed map[string]string
	if err := json.Unmarshal(got.Secret, &parsed); err != nil {
		t.Fatalf("secret 应是合法 JSON：%v", err)
	}
	if parsed["api_key"] != "sk-up" {
		t.Errorf("secret = %s，期望含 api_key=sk-up", got.Secret)
	}

	if _, err := s.AddCredential(context.Background(), CredentialInput{MerchantID: 1, Group: "g"}); err == nil {
		t.Fatal("缺 api-key 应当被拒绝")
	}
}

func TestListCredentialsMasks(t *testing.T) {
	f := &fakeStore{}
	f.listCredentials = func(context.Context) ([]store.CredentialRow, error) {
		return []store.CredentialRow{
			{ID: 1, MerchantID: 2, CredGroup: "g", Name: "primary", Secret: []byte(`{"api_key":"sk-upstream-abcdef"}`), Enabled: true},
		}, nil
	}
	s := newService(f)
	views, err := s.ListCredentials(context.Background())
	if err != nil {
		t.Fatalf("列出失败：%v", err)
	}
	if len(views) != 1 {
		t.Fatalf("行数 = %d，期望 1", len(views))
	}
	if strings.Contains(views[0].Prefix, "upstream-abcdef") {
		t.Errorf("前缀不应含明文后半段：%q", views[0].Prefix)
	}
	encoded, _ := json.Marshal(views)
	if strings.Contains(string(encoded), "upstream-abcdef") {
		t.Errorf("JSON 输出泄露明文：%s", encoded)
	}
}

func TestSetModelMapDefaultAndOverrides(t *testing.T) {
	f := &fakeStore{}
	var got store.ModelMap
	f.upsertModelMap = func(_ context.Context, m store.ModelMap) (uint64, error) {
		got = m
		return 5, nil
	}
	s := newService(f)
	if _, err := s.SetModelMap(context.Background(), ModelMapInput{
		ChannelID: 1, Model: "alias", UpstreamModel: "up", RequestOverrides: json.RawMessage(`{"temperature":0.2}`),
	}); err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if got.PriceMultiplier != defaultMultiplier {
		t.Errorf("倍率默认值 = %q，期望 %q", got.PriceMultiplier, defaultMultiplier)
	}
	if string(got.RequestOverrides) != `{"temperature":0.2}` {
		t.Errorf("overrides 未透传：%s", got.RequestOverrides)
	}

	if _, err := s.SetModelMap(context.Background(), ModelMapInput{
		ChannelID: 1, Model: "alias", UpstreamModel: "up", RequestOverrides: json.RawMessage(`"not-an-object"`),
	}); err == nil {
		t.Fatal("非对象 overrides 应当被拒绝")
	}
}

func TestIssueKeyHashesAndMasks(t *testing.T) {
	f := &fakeStore{}
	var got store.APIKey
	f.insertAPIKey = func(_ context.Context, k store.APIKey) (uint64, error) {
		got = k
		return 7, nil
	}
	s := newService(f)
	issued, err := s.IssueKey(context.Background(), IssueKeyInput{AccountID: 2})
	if err != nil {
		t.Fatalf("签发失败：%v", err)
	}
	if !strings.HasPrefix(issued.Plaintext, "sk-") {
		t.Errorf("明文前缀不符：%q", issued.Plaintext)
	}
	if got.KeyHash != apikey.Hash(issued.Plaintext) || len(got.KeyHash) != 64 {
		t.Errorf("存储的哈希与明文不对应：%q", got.KeyHash)
	}
	if got.KeyPrefix != apikey.Prefix(issued.Plaintext) {
		t.Errorf("前缀不符：%q", got.KeyPrefix)
	}
	if strings.Contains(got.KeyPrefix, strings.TrimPrefix(issued.Plaintext, "sk-")) {
		t.Errorf("前缀泄露了随机部分：%q", got.KeyPrefix)
	}

	// 两次签发不应重复。
	second, err := s.IssueKey(context.Background(), IssueKeyInput{AccountID: 2})
	if err != nil {
		t.Fatalf("第二次签发失败：%v", err)
	}
	if second.Plaintext == issued.Plaintext {
		t.Error("两次签发的明文不应相同")
	}
}

func TestCreditBucketValidates(t *testing.T) {
	f := &fakeStore{}
	s := newService(f)
	base := CreditBucketInput{
		AccountID: 1, MerchantID: 1, Unit: billing.UnitSettleToken,
		Amount: "100", Fallback: billing.FallbackReject, Source: billing.SourceGrant,
	}
	tests := []struct {
		name string
		give CreditBucketInput
	}{
		{name: "未知单位", give: func() CreditBucketInput { in := base; in.Unit = "usd"; return in }()},
		{name: "未知处置", give: func() CreditBucketInput { in := base; in.Fallback = "ignore"; return in }()},
		{name: "未知来路", give: func() CreditBucketInput { in := base; in.Source = "gift"; return in }()},
		{name: "数量为零", give: func() CreditBucketInput { in := base; in.Amount = "0"; return in }()},
		{name: "账户为零", give: func() CreditBucketInput { in := base; in.AccountID = 0; return in }()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.CreditBucket(context.Background(), tt.give); err == nil {
				t.Fatal("应当被拒绝")
			}
		})
	}
	if len(f.calls) != 0 {
		t.Fatalf("非法输入不应触达存储层，实际 %v", f.calls)
	}

	var got store.BucketRow
	f.insertBucket = func(_ context.Context, b store.BucketRow) (uint64, error) {
		got = b
		return 4, nil
	}
	if _, err := s.CreditBucket(context.Background(), base); err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if got.Total != "100" || got.Remaining != "100" || got.Priority != store.DefaultBucketPriority {
		t.Errorf("账本初值不符：%+v", got)
	}
}

func TestCreateProductValidatesModelScope(t *testing.T) {
	f := &fakeStore{}
	s := newService(f)
	_, err := s.CreateProduct(context.Background(), ProductInput{
		MerchantID: 1, Name: "p", Unit: billing.UnitSettleToken, Qty: "100", Price: "10",
		ModelScope: json.RawMessage(`{"not":"array"}`),
	})
	if err == nil {
		t.Fatal("非数组 model-scope 应当被拒绝")
	}
	if len(f.calls) != 0 {
		t.Fatalf("非法输入不应触达存储层：%v", f.calls)
	}
}

func TestBuyComputesAndDerivesBucket(t *testing.T) {
	f := &fakeStore{}
	f.getProduct = func(_ context.Context, id uint64) (*store.Product, error) {
		return &store.Product{ID: id, MerchantID: 8, Name: "包", Unit: billing.UnitSettleToken, Qty: "100000000", Price: "10", ValidityDays: 30}, nil
	}
	var gotPurchase store.Purchase
	var gotBucket store.BucketRow
	f.createPurchase = func(_ context.Context, p store.Purchase, b store.BucketRow) (uint64, uint64, error) {
		gotPurchase, gotBucket = p, b
		return 11, 12, nil
	}
	s := newService(f)
	result, err := s.Buy(context.Background(), BuyInput{AccountID: 3, ProductID: 5, Qty: "2"})
	if err != nil {
		t.Fatalf("购买失败：%v", err)
	}
	if gotPurchase.PricePaid != "20" || gotPurchase.MerchantID != 8 || gotPurchase.ProductID != 5 {
		t.Errorf("购买记录不符：%+v", gotPurchase)
	}
	if gotBucket.Total != "200000000" || gotBucket.Remaining != "200000000" {
		t.Errorf("派生账本数量不符：%+v", gotBucket)
	}
	if gotBucket.Source != billing.SourcePurchase || gotBucket.Unit != billing.UnitSettleToken {
		t.Errorf("派生账本来路 / 单位不符：%+v", gotBucket)
	}
	if gotBucket.Fallback != billing.FallbackChargeBalance {
		t.Errorf("默认 fallback = %q", gotBucket.Fallback)
	}
	if gotBucket.ExpiresAt == nil || !gotBucket.ExpiresAt.Equal(fixedNow.AddDate(0, 0, 30)) {
		t.Errorf("到期时间不符：%v", gotBucket.ExpiresAt)
	}
	// 折算率 = 商品单价 / 商品每份数量 = 10 / 100000000。
	if gotBucket.UnitRate == nil || *gotBucket.UnitRate != "0.0000001" {
		t.Errorf("派生账本折算率不符：%+v", gotBucket.UnitRate)
	}
	if result.PurchaseID != 11 || result.BucketID != 12 || result.Total != "200000000" {
		t.Errorf("返回结果不符：%+v", result)
	}
	if result.UnitRate != "0.0000001" {
		t.Errorf("返回折算率 = %q，期望 0.0000001", result.UnitRate)
	}
}

// TestBuyUnitRateRounds 覆盖折算率按 8 位四舍五入：10 / 3 = 3.33333333。
func TestBuyUnitRateRounds(t *testing.T) {
	f := &fakeStore{}
	f.getProduct = func(_ context.Context, id uint64) (*store.Product, error) {
		return &store.Product{ID: id, MerchantID: 8, Name: "散装", Unit: billing.UnitSettleToken, Qty: "3", Price: "10"}, nil
	}
	var gotBucket store.BucketRow
	f.createPurchase = func(_ context.Context, _ store.Purchase, b store.BucketRow) (uint64, uint64, error) {
		gotBucket = b
		return 1, 2, nil
	}
	s := newService(f)
	result, err := s.Buy(context.Background(), BuyInput{AccountID: 3, ProductID: 5, Qty: "1"})
	if err != nil {
		t.Fatalf("购买失败：%v", err)
	}
	if gotBucket.UnitRate == nil || *gotBucket.UnitRate != "3.33333333" {
		t.Errorf("折算率 = %+v，期望 3.33333333", gotBucket.UnitRate)
	}
	if result.UnitRate != "3.33333333" {
		t.Errorf("返回折算率 = %q，期望 3.33333333", result.UnitRate)
	}
}

func TestBuyProductNotFound(t *testing.T) {
	f := &fakeStore{}
	f.getProduct = func(context.Context, uint64) (*store.Product, error) {
		return nil, fmt.Errorf("store: 查询 merchant_product 失败: %w", sql.ErrNoRows)
	}
	s := newService(f)
	_, err := s.Buy(context.Background(), BuyInput{AccountID: 3, ProductID: 5, Qty: "1"})
	if err == nil {
		t.Fatal("商品不存在应当报错")
	}
	if !strings.Contains(err.Error(), "商品") {
		t.Errorf("错误信息应可读，得到 %v", err)
	}
	if f.called("CreatePurchaseAndBucket") {
		t.Error("商品不存在不应写购买记录")
	}
}

func TestPublishPricingRequiresComponents(t *testing.T) {
	f := &fakeStore{}
	s := newService(f)
	if _, err := s.PublishPricing(context.Background(), PublishPricingInput{MerchantID: 1, Model: "m"}); err == nil {
		t.Fatal("缺分量应当被拒绝")
	}
	if len(f.calls) != 0 {
		t.Fatalf("非法输入不应触达存储层：%v", f.calls)
	}

	var gotEffective time.Time
	f.publishPricing = func(_ context.Context, merchantID uint64, model string, effectiveAt time.Time, _ []store.PriceComponent) (*store.Pricing, error) {
		gotEffective = effectiveAt
		return &store.Pricing{ID: 2, MerchantID: merchantID, Model: model, Version: 1}, nil
	}
	if _, err := s.PublishPricing(context.Background(), PublishPricingInput{
		MerchantID: 1, Model: "m",
		Components: []store.PriceComponent{{Metric: billing.MetricRequest, UnitSettle: billing.UnitSettleCredit, UnitPrice: "1", BasisQty: "1"}},
	}); err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if !gotEffective.Equal(fixedNow) {
		t.Errorf("未指定生效时刻时应取当前时刻，得到 %v", gotEffective)
	}
}

func TestAddRuleNormalizesAndValidatesMasks(t *testing.T) {
	f := &fakeStore{}
	var got store.PriceRule
	f.insertPriceRule = func(_ context.Context, r store.PriceRule) (uint64, error) {
		got = r
		return 6, nil
	}
	s := newService(f)
	mask := uint8(0x7F)
	if _, err := s.AddRule(context.Background(), PriceRuleInput{
		Scope: billing.ScopePricing, ScopeID: 1, Multiplier: "0.5",
		TimeFrom: "22:00", WeekdayMask: &mask,
	}); err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if got.TimeFrom != "22:00:00" || got.Priority != defaultRulePriority {
		t.Errorf("归一或默认值未生效：%+v", got)
	}

	tooBig := uint8(0x80)
	if _, err := s.AddRule(context.Background(), PriceRuleInput{
		Scope: billing.ScopePricing, ScopeID: 1, Multiplier: "1", WeekdayMask: &tooBig,
	}); err == nil {
		t.Fatal("超位宽掩码应当被拒绝")
	}
	if _, err := s.AddRule(context.Background(), PriceRuleInput{
		Scope: billing.ScopePricing, ScopeID: 1, Multiplier: "1", TimeFrom: "25:00",
	}); err == nil {
		t.Fatal("非法时段应当被拒绝")
	}
	from := fixedNow
	to := fixedNow.Add(-time.Hour)
	if _, err := s.AddRule(context.Background(), PriceRuleInput{
		Scope: billing.ScopePricing, ScopeID: 1, Multiplier: "1", ValidFrom: &from, ValidTo: &to,
	}); err == nil {
		t.Fatal("valid-to 早于 valid-from 应当被拒绝")
	}
}

func TestImportCalendarRejectsEmpty(t *testing.T) {
	f := &fakeStore{}
	s := newService(f)
	if err := s.ImportCalendar(context.Background(), "cn", nil); err == nil {
		t.Fatal("空输入应当被拒绝")
	}
	if len(f.calls) != 0 {
		t.Fatalf("非法输入不应触达存储层：%v", f.calls)
	}
	if err := s.ImportCalendar(context.Background(), "cn", []store.CalendarDay{{Date: "2026-10-01", DayKind: billing.DayKindHoliday}}); err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
}

func TestAddAdjustmentRequiresAudit(t *testing.T) {
	f := &fakeStore{}
	s := newService(f)
	if _, err := s.AddAdjustment(context.Background(), AdjustmentInput{AccountID: 1, Amount: "-1", Reason: "r"}); err == nil {
		t.Fatal("缺 operator 应当被拒绝")
	}
	if len(f.calls) != 0 {
		t.Fatalf("非法输入不应触达存储层：%v", f.calls)
	}
	var got store.Adjustment
	f.insertAdjustment = func(_ context.Context, a store.Adjustment) (uint64, error) {
		got = a
		return 2, nil
	}
	if _, err := s.AddAdjustment(context.Background(), AdjustmentInput{AccountID: 1, Amount: "-1.5", Reason: "退费", Operator: "ops"}); err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if got.DeltaAmount != "-1.5" || got.Operator != "ops" {
		t.Errorf("调账记录不符：%+v", got)
	}
}

func TestCreateAccountDefaults(t *testing.T) {
	f := &fakeStore{}
	var got store.Account
	f.insertAccount = func(_ context.Context, a store.Account) (uint64, error) {
		got = a
		return 1, nil
	}
	s := newService(f)
	if _, err := s.CreateAccount(context.Background(), AccountInput{Code: "acct", Name: "账户"}); err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if got.PriceMultiplier != defaultMultiplier || got.Status != store.StatusActive || got.DefaultMerchantID != nil {
		t.Errorf("开户默认值不符：%+v", got)
	}

	zero := uint64(0)
	if _, err := s.CreateAccount(context.Background(), AccountInput{Code: "acct", Name: "账户", DefaultMerchantID: &zero}); err == nil {
		t.Fatal("default-merchant=0 应当被拒绝")
	}
	if err := s.SetAccountMultiplier(context.Background(), 0, "1"); err == nil {
		t.Error("账户 0 应当被拒绝")
	}
}

func TestDisableActionsRequireID(t *testing.T) {
	f := &fakeStore{}
	s := newService(f)
	calls := []func(context.Context, uint64) error{
		s.DisableMerchant,
		s.DisableChannel,
		s.DisableCredential,
		s.DisableModelMap,
		s.DisableAccount,
		s.RevokeKey,
		s.DeleteRule,
	}
	for i, call := range calls {
		if err := call(context.Background(), 0); err == nil {
			t.Errorf("第 %d 个动作在 id=0 时应报错", i)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("非法输入不应触达存储层：%v", f.calls)
	}
}

func TestChannelEnableDisableCallsStore(t *testing.T) {
	f := &fakeStore{}
	var gotEnabled bool
	var gotID uint64
	f.setChannelEnabled = func(_ context.Context, id uint64, enabled bool) error {
		gotID, gotEnabled = id, enabled
		return nil
	}
	s := newService(f)
	if err := s.EnableChannel(context.Background(), 3); err != nil {
		t.Fatalf("启用失败：%v", err)
	}
	if gotID != 3 || !gotEnabled {
		t.Errorf("启用未传参到位：id=%d enabled=%v", gotID, gotEnabled)
	}
	if err := s.DisableChannel(context.Background(), 4); err != nil {
		t.Fatalf("停用失败：%v", err)
	}
	if gotID != 4 || gotEnabled {
		t.Errorf("停用未传参到位：id=%d enabled=%v", gotID, gotEnabled)
	}
}

func TestListUsagePassesFilters(t *testing.T) {
	f := &fakeStore{}
	var gotAccount uint64
	var gotSince time.Time
	f.listUsage = func(_ context.Context, accountID uint64, since time.Time) ([]store.UsageListRow, error) {
		gotAccount, gotSince = accountID, since
		return nil, nil
	}
	s := newService(f)
	since := fixedNow.Add(-24 * time.Hour)
	if _, err := s.ListUsage(context.Background(), 9, since); err != nil {
		t.Fatalf("列出失败：%v", err)
	}
	if gotAccount != 9 || !gotSince.Equal(since) {
		t.Errorf("过滤条件未透传：account=%d since=%v", gotAccount, gotSince)
	}
}

func TestCreateQuotaValidates(t *testing.T) {
	f := &fakeStore{}
	s := newService(f)
	base := QuotaInput{
		Scope: billing.ScopeAccount, ScopeID: 2, Metric: billing.MetricRequest,
		WindowKind: billing.WindowKindCalendar, Period: billing.PeriodDay,
		LimitAmount: "100", Action: billing.ActionReject,
	}
	tests := []struct {
		name string
		give QuotaInput
	}{
		{name: "未知范围", give: func() QuotaInput { in := base; in.Scope = "tenant"; return in }()},
		{name: "范围 id 为零", give: func() QuotaInput { in := base; in.ScopeID = 0; return in }()},
		{name: "未知指标", give: func() QuotaInput { in := base; in.Metric = "watts"; return in }()},
		{name: "窗口组合非法", give: func() QuotaInput { in := base; in.WindowKind = billing.WindowKindRolling; return in }()},
		{name: "限额为零", give: func() QuotaInput { in := base; in.LimitAmount = "0"; return in }()},
		{name: "未知处置", give: func() QuotaInput { in := base; in.Action = "queue"; return in }()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.CreateQuota(context.Background(), tt.give); err == nil {
				t.Fatal("应当被拒绝")
			}
		})
	}
	if len(f.calls) != 0 {
		t.Fatalf("非法输入不应触达存储层，实际 %v", f.calls)
	}

	var got store.Quota
	f.insertQuota = func(_ context.Context, q store.Quota) (uint64, error) {
		got = q
		return 5, nil
	}
	id, err := s.CreateQuota(context.Background(), base)
	if err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if id != 5 || got.Metric != billing.MetricRequest || got.Period != billing.PeriodDay {
		t.Errorf("写入内容不符：%+v", got)
	}
}

func TestListQuotasComputesUsedAndRemaining(t *testing.T) {
	limit := quota.Limit{
		ID: 1, Scope: billing.ScopeAccount, ScopeID: 2, Metric: billing.MetricRequest,
		WindowKind: billing.WindowKindCalendar, Period: billing.PeriodDay,
		LimitAmount: decimal.NewFromInt(100), Action: billing.ActionReject,
	}
	f := &fakeStore{}
	f.quotas = func(context.Context, billing.Scope, uint64) ([]quota.Limit, error) {
		return []quota.Limit{limit}, nil
	}
	f.quotaUsage = func(context.Context, quota.UsageQuery) (decimal.Decimal, error) {
		return decimal.NewFromInt(40), nil
	}
	s := newService(f)
	views, err := s.ListQuotas(context.Background(), billing.ScopeAccount, 2)
	if err != nil {
		t.Fatalf("列出失败：%v", err)
	}
	if len(views) != 1 || views[0].Used == nil || *views[0].Used != "40" {
		t.Fatalf("已用量不符：%+v", views)
	}
	if views[0].Remaining == nil || *views[0].Remaining != "60" {
		t.Errorf("剩余额度不符：%+v", views[0])
	}

	// 已用量超过限额时剩余额度不显示负数。
	f.quotaUsage = func(context.Context, quota.UsageQuery) (decimal.Decimal, error) {
		return decimal.NewFromInt(150), nil
	}
	views, err = s.ListQuotas(context.Background(), billing.ScopeAccount, 2)
	if err != nil {
		t.Fatalf("列出失败：%v", err)
	}
	if views[0].Remaining == nil || *views[0].Remaining != "0" {
		t.Errorf("超限时剩余额度应为 0：%+v", views[0])
	}
}

func TestListQuotasToleratesUnresolvableRows(t *testing.T) {
	f := &fakeStore{}
	f.quotas = func(context.Context, billing.Scope, uint64) ([]quota.Limit, error) {
		return []quota.Limit{{
			ID: 1, Scope: billing.ScopeAccount, ScopeID: 2, Metric: billing.MetricRequest,
			WindowKind: billing.WindowKindRolling, Period: billing.PeriodDay,
			LimitAmount: decimal.NewFromInt(1), Action: billing.ActionReject,
		}}, nil
	}
	s := newService(f)
	views, err := s.ListQuotas(context.Background(), billing.ScopeAccount, 2)
	if err != nil {
		t.Fatalf("列出失败：%v", err)
	}
	if views[0].Used != nil || views[0].Remaining != nil {
		t.Errorf("不可判定的行应留空：%+v", views[0])
	}
	if f.called("Usage") {
		t.Error("不可判定的行不应发起聚合查询")
	}
}

func TestResetQuotaRequiresAudit(t *testing.T) {
	f := &fakeStore{}
	s := newService(f)
	if _, err := s.ResetQuota(context.Background(), ResetQuotaInput{QuotaID: 1, Reason: "r"}); err == nil {
		t.Fatal("缺 operator 应当被拒绝")
	}
	if _, err := s.ResetQuota(context.Background(), ResetQuotaInput{QuotaID: 1, Operator: "ops"}); err == nil {
		t.Fatal("缺 reason 应当被拒绝")
	}
	if len(f.calls) != 0 {
		t.Fatalf("非法输入不应触达存储层：%v", f.calls)
	}

	var got store.QuotaEvent
	f.insertQuotaEvent = func(_ context.Context, e store.QuotaEvent) (uint64, error) {
		got = e
		return 3, nil
	}
	if _, err := s.ResetQuota(context.Background(), ResetQuotaInput{QuotaID: 7, Reason: "误计重置", Operator: "ops"}); err != nil {
		t.Fatalf("合法输入不应报错：%v", err)
	}
	if got.QuotaID != 7 || got.Event != billing.QuotaEventReset || !got.BaselineAt.Equal(fixedNow) {
		t.Errorf("重置事件不符：%+v", got)
	}
}

func TestDeleteQuotaRequiresID(t *testing.T) {
	f := &fakeStore{}
	s := newService(f)
	if err := s.DeleteQuota(context.Background(), 0); err == nil {
		t.Fatal("id=0 应当被拒绝")
	}
	if len(f.calls) != 0 {
		t.Fatalf("非法输入不应触达存储层：%v", f.calls)
	}
	if err := s.DeleteQuota(context.Background(), 4); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if !f.called("DeleteQuota") {
		t.Error("应调用 DeleteQuota")
	}
}
