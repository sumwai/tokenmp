package user

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/settlement"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件覆盖商品目录、下单与订单列表：请求约束、金额与折算率的十进制字符串形态、
// 幂等命中返回首次订单，以及账户无可用额度时的 402。

// testProduct 是一个可购买档位：1 亿 token / 份，售价 10，30 天有效，限定两个模型。
func testProduct() store.Product {
	return store.Product{
		ID:           9,
		MerchantID:   1,
		Name:         "10 元 100M token",
		Unit:         billing.UnitSettleToken,
		Qty:          "100000000",
		Price:        "10",
		ModelScope:   []byte(`["gpt-4o","claude-3"]`),
		ValidityDays: 30,
	}
}

// postOrder 发一次下单请求，返回状态码与信封；请求体构造与断言口径集中在用例里。
func (e *testEnv) postOrder(t *testing.T, body any) (int, map[string]json.RawMessage) {
	t.Helper()
	return e.doJSON(t, http.MethodPost, OrdersPath, "token", body)
}

// TestAccountPaymentRequiredWhenUnfunded 断言账户没有可用额度时回 402，
// 且不再读取摘要：判定与数据面同一门闸，页面据此渲染充值引导。
func TestAccountPaymentRequiredWhenUnfunded(t *testing.T) {
	e := newTestEnv()
	e.store.buckets = nil

	status, env := e.do(t, http.MethodGet, AccountPath, "token", "")
	if status != http.StatusPaymentRequired || codeOf(t, env) != webapi.CodePaymentRequired {
		t.Fatalf("无可用额度应 402: %d %s", status, env)
	}
	if e.summary.recent != 0 {
		t.Errorf("402 分支不应读取摘要，得到 recent=%d", e.summary.recent)
	}
}

// TestAccountRejectsExpiredBucket 断言只剩过期包时同样回 402：过期包不能再作为「还有钱」的依据。
func TestAccountRejectsExpiredBucket(t *testing.T) {
	e := newTestEnv()
	past := testInstant.Add(-time.Hour)
	e.store.buckets = []settlement.Bucket{{
		ID: 1, Unit: billing.UnitSettleCurrency, Remaining: decimal.NewFromInt(5),
		Fallback: billing.FallbackReject, ExpiresAt: &past,
	}}

	status, env := e.do(t, http.MethodGet, AccountPath, "token", "")
	if status != http.StatusPaymentRequired || codeOf(t, env) != webapi.CodePaymentRequired {
		t.Fatalf("过期包应 402: %d %s", status, env)
	}
}

// TestListProducts 断言商品目录按档位返回，数量与售价是十进制字符串，
// 模型范围为空时序列化成 null 而不是空数组。
func TestListProducts(t *testing.T) {
	e := newTestEnv()
	// 第二档刻意用数据库读回来的定点文本：DECIMAL 列读回带列标度，
	// 视图要归一到 decimal.String() 的形态，否则页面会把尾零显示出来。
	unlimited := testProduct()
	unlimited.ID = 10
	unlimited.Name = "不限模型包"
	unlimited.ModelScope = nil
	unlimited.Qty = "100000000.00000000"
	unlimited.Price = "10.00000000"
	e.store.products = []store.Product{testProduct(), unlimited}

	status, env := e.do(t, http.MethodGet, ProductsPath, "token", "")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	var data struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	if len(data.Items) != 2 {
		t.Fatalf("目录条数 = %d，期望 2", len(data.Items))
	}
	var qty, price string
	if err := json.Unmarshal(data.Items[0]["qty"], &qty); err != nil || qty != "100000000" {
		t.Errorf("数量应为十进制字符串 100000000，得到 %s（err=%v）", data.Items[0]["qty"], err)
	}
	if err := json.Unmarshal(data.Items[0]["price"], &price); err != nil || price != "10" {
		t.Errorf("售价应为十进制字符串 10，得到 %s（err=%v）", data.Items[0]["price"], err)
	}
	if got := string(data.Items[1]["model_scope"]); got != "null" {
		t.Errorf("不限模型的档位 model_scope 应为 null，得到 %s", got)
	}
	if got := string(data.Items[0]["model_scope"]); got != `["gpt-4o","claude-3"]` {
		t.Errorf("限定模型的档位 model_scope = %s", got)
	}
	// 数据库读回的定点文本被归一到 decimal.String() 的形态：尾零不落到契约面上。
	if got := string(data.Items[1]["qty"]); got != `"100000000"` {
		t.Errorf("归一后的每份数量 = %s，期望 \"100000000\"", got)
	}
	if got := string(data.Items[1]["price"]); got != `"10"` {
		t.Errorf("归一后的售价 = %s，期望 \"10\"", got)
	}
}

// TestListProductsRejectsNonGet 断言非 GET 回 400。
func TestListProductsRejectsNonGet(t *testing.T) {
	e := newTestEnv()
	status, env := e.do(t, http.MethodPost, ProductsPath, "token", "")
	if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
		t.Fatalf("非 GET 应 400: %d %s", status, env)
	}
}

// TestCreateOrderSuccess 断言下单按档位口径派生：实付 = 售价 × 份数、
// 存量 = 每份数量 × 份数、折算率 = 售价 / 每份数量、有效期 = 购买时刻 + 有效天数，
// 且全部是十进制字符串。
func TestCreateOrderSuccess(t *testing.T) {
	e := newTestEnv()
	e.store.product = ptr(testProduct())
	e.store.createdOrder = store.OrderWrite{
		Purchase: store.Purchase{ID: 77, ProductID: 9, Qty: "2", PricePaid: "20", PurchasedAt: testInstant},
		BucketID: 5,
	}

	status, env := e.postOrder(t, map[string]any{
		"product_id":      9,
		"qty":             "2",
		"idempotency_key": "order-key-0001",
	})
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("下单应 200: %d %s", status, env)
	}
	if e.store.purchaseArg.PricePaid != "20" {
		t.Errorf("实付 = %q，期望 20", e.store.purchaseArg.PricePaid)
	}
	if e.store.purchaseArg.AccountID != 42 || e.store.purchaseArg.MerchantID != 1 {
		t.Errorf("归属应由会话与档位推导，得到 account=%d merchant=%d", e.store.purchaseArg.AccountID, e.store.purchaseArg.MerchantID)
	}
	if e.store.purchaseArg.IdempotencyKey != "order-key-0001" {
		t.Errorf("幂等键未透传到存储层：%q", e.store.purchaseArg.IdempotencyKey)
	}
	if !e.store.purchaseArg.PurchasedAt.Equal(testInstant) {
		t.Errorf("购买时刻 = %s，期望 %s", e.store.purchaseArg.PurchasedAt, testInstant)
	}
	if e.store.bucketArg.Total != "200000000" || e.store.bucketArg.Remaining != "200000000" {
		t.Errorf("派生存量 = %q / %q，期望 200000000", e.store.bucketArg.Total, e.store.bucketArg.Remaining)
	}
	if e.store.bucketArg.Unit != billing.UnitSettleToken || e.store.bucketArg.Source != billing.SourcePurchase {
		t.Errorf("派生账本单位 / 来路 = %q / %q", e.store.bucketArg.Unit, e.store.bucketArg.Source)
	}
	if e.store.bucketArg.Fallback != billing.FallbackChargeBalance {
		t.Errorf("派生的 fallback = %q，期望 charge_balance", e.store.bucketArg.Fallback)
	}
	if e.store.bucketArg.UnitRate == nil || *e.store.bucketArg.UnitRate != "0.0000001" {
		t.Errorf("锁定的折算率 = %v，期望 0.0000001", e.store.bucketArg.UnitRate)
	}
	if e.store.bucketArg.ExpiresAt == nil || !e.store.bucketArg.ExpiresAt.Equal(testInstant.AddDate(0, 0, 30)) {
		t.Errorf("到期时刻 = %v，期望 %s", e.store.bucketArg.ExpiresAt, testInstant.AddDate(0, 0, 30))
	}

	// 响应的金额、数量与折算率都是字符串，不经浮点。
	var view map[string]json.RawMessage
	if err := json.Unmarshal(env["data"], &view); err != nil {
		t.Fatalf("解析订单: %v", err)
	}
	for _, key := range []string{"qty", "price_paid", "total", "unit_rate"} {
		var got string
		if err := json.Unmarshal(view[key], &got); err != nil {
			t.Fatalf("订单字段 %s 不是字符串: %s", key, view[key])
		}
	}
	if got := string(view["product_name"]); got != `"10 元 100M token"` {
		t.Errorf("商品名 = %s", got)
	}
}

// TestCreateOrderIdempotentReplayReturnsFirst 断言同一幂等键再次提交时返回首次订单：
// 存储层报幂等命中，响应里的份数与金额取自首次那笔，而不是本次请求。
func TestCreateOrderIdempotentReplayReturnsFirst(t *testing.T) {
	e := newTestEnv()
	e.store.product = ptr(testProduct())
	e.store.createdOrder = store.OrderWrite{
		Purchase: store.Purchase{ID: 77, ProductID: 9, Qty: "3", PricePaid: "30", PurchasedAt: testInstant},
		Replayed: true,
	}

	status, env := e.postOrder(t, map[string]any{
		"product_id":      9,
		"qty":             "2",
		"idempotency_key": "order-key-0001",
	})
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("幂等命中应 200: %d %s", status, env)
	}
	var view struct {
		ID        uint64 `json:"id"`
		Qty       string `json:"qty"`
		PricePaid string `json:"price_paid"`
	}
	if err := json.Unmarshal(env["data"], &view); err != nil {
		t.Fatalf("解析订单: %v", err)
	}
	if view.ID != 77 || view.Qty != "3" || view.PricePaid != "30" {
		t.Errorf("幂等命中应返回首次订单，得到 %+v", view)
	}
}

// TestCreateOrderDecimalStringsSurviveFloatRange 断言超出双精度精确范围的份数
// 按原样十进制字符串传递与返回：这一层不做任何浮点转换。
func TestCreateOrderDecimalStringsSurviveFloatRange(t *testing.T) {
	e := newTestEnv()
	product := testProduct()
	product.Qty = "1"
	product.Price = "1"
	e.store.product = &product
	e.store.createdOrder = store.OrderWrite{
		Purchase: store.Purchase{ID: 1, ProductID: 9, Qty: "9007199254740993", PricePaid: "9007199254740993", PurchasedAt: testInstant},
	}

	status, env := e.postOrder(t, map[string]any{
		"product_id":      9,
		"qty":             "9007199254740993",
		"idempotency_key": "order-key-big1",
	})
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if e.store.purchaseArg.PricePaid != "9007199254740993" {
		t.Errorf("实付被改成 %q，期望原样十进制字符串", e.store.purchaseArg.PricePaid)
	}
	if !bytes.Contains(env["data"], []byte(`"9007199254740993"`)) {
		t.Errorf("响应里应保留原样十进制字符串：%s", env["data"])
	}
}

// TestCreateOrderValidation 断言请求约束逐条落到 400。
func TestCreateOrderValidation(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
	}{
		{name: "缺商品标识", body: map[string]any{"qty": "1", "idempotency_key": "order-key-0001"}},
		{name: "份数为零", body: map[string]any{"product_id": 9, "qty": "0", "idempotency_key": "order-key-0001"}},
		{name: "份数非数", body: map[string]any{"product_id": 9, "qty": "abc", "idempotency_key": "order-key-0001"}},
		{name: "幂等键过短", body: map[string]any{"product_id": 9, "qty": "1", "idempotency_key": "short"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv()
			e.store.product = ptr(testProduct())
			status, env := e.postOrder(t, tt.body)
			if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
				t.Fatalf("应 400: %d %s", status, env)
			}
		})
	}
}

// TestCreateOrderProductMissing 断言商品不存在回 404，且不落任何购买记录。
func TestCreateOrderProductMissing(t *testing.T) {
	e := newTestEnv()
	e.store.productErr = sql.ErrNoRows

	status, env := e.postOrder(t, map[string]any{
		"product_id": 9, "qty": "1", "idempotency_key": "order-key-0001",
	})
	if status != http.StatusNotFound || codeOf(t, env) != webapi.CodeNotFound {
		t.Fatalf("商品不存在应 404: %d %s", status, env)
	}
	if e.store.purchaseArg.Qty != "" {
		t.Errorf("商品不存在时不应落购买记录：%+v", e.store.purchaseArg)
	}
}

// TestListOrders 断言订单列表按偏移分页，并把订单口径透传为十进制字符串。
func TestListOrders(t *testing.T) {
	e := newTestEnv()
	e.store.orders = []store.OrderRow{{
		ID: 77, ProductID: 9, ProductName: "10 元 100M token", Unit: billing.UnitSettleToken,
		Qty: "2", PricePaid: "20", Total: "200000000", UnitRate: "0.0000001", PurchasedAt: testInstant,
	}}
	e.store.ordersTotal = 21

	status, env := e.do(t, http.MethodGet, OrdersPath, "token", "?page=2&size=10")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	if e.store.ordersLimit != 10 || e.store.ordersOffset != 10 {
		t.Errorf("分页入参 = limit %d offset %d，期望 10 / 10", e.store.ordersLimit, e.store.ordersOffset)
	}
	page, size, total := metaOf(t, env)
	if page != 2 || size != 10 || total != 21 {
		t.Errorf("分页字段 = %d / %d / %d，期望 2 / 10 / 21", page, size, total)
	}
	var data struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	if len(data.Items) != 1 {
		t.Fatalf("当页条数 = %d，期望 1", len(data.Items))
	}
	// 金额、数量与折算率必须原样是字符串。
	var got string
	if err := json.Unmarshal(data.Items[0]["unit_rate"], &got); err != nil || got != "0.0000001" {
		t.Errorf("unit_rate = %s（err=%v），期望字符串 0.0000001", data.Items[0]["unit_rate"], err)
	}
	if err := json.Unmarshal(data.Items[0]["total"], &got); err != nil || got != "200000000" {
		t.Errorf("total = %s（err=%v），期望字符串 200000000", data.Items[0]["total"], err)
	}
}

// metaOf 解析信封的分页三字段。
func metaOf(t *testing.T, env map[string]json.RawMessage) (int, int, int) {
	t.Helper()
	var page, size, total int
	for key, dst := range map[string]*int{"page": &page, "size": &size, "total": &total} {
		if err := json.Unmarshal(env[key], dst); err != nil {
			t.Fatalf("解析 %s: %v", key, err)
		}
	}
	return page, size, total
}

func ptr[T any](v T) *T { return &v }
