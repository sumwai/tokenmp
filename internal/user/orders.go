package user

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件实现商品目录与订单：列出可购买的档位、幂等下单、按账户列出订单。
//
// 页面不发起支付：下单即在同一事务内写入购买事实并派生账本，与 internal/admin 的
// 购买动作同一口径（数量、金额、有效期、折算率的算式只有一处出处）。金额与折算率
// 一律十进制字符串，页面不经 Number / parseFloat（web/AGENTS.md 的「数据展示」）。
//
// 归属由会话推导：下单不接受账户或商家参数，购买记录与派生的账本都挂在当前账户上。

// 请求约束，与契约 CreateOrderRequest 的声明一致。
const (
	// idempotencyKeyMinLen / idempotencyKeyMaxLen 是幂等键的字符数上下限。
	// 下限不是形式要求：太短的键（如 "1"）在客户端重复使用时会互相顶掉对方的下单。
	idempotencyKeyMinLen = 8
	idempotencyKeyMaxLen = 64
	// maxOrderQtyRunes 是购买份数字符串的字符数上限。份数是十进制字符串，
	// 超长的取值不是有效数字，直接拒绝而不是让解析器去扛。
	maxOrderQtyRunes = 32
)

var (
	// errInvalidQty 是购买份数非法时的错误，文案直接写回信封。
	errInvalidQty = errors.New("购买份数必须是正数的十进制字符串")
	// errInvalidIdempotencyKey 是幂等键非法时的错误。
	errInvalidIdempotencyKey = fmt.Errorf("幂等键必填，长度 %d 到 %d 个字符", idempotencyKeyMinLen, idempotencyKeyMaxLen)
)

// productView 是一个商品档位，字段与契约 Product 对齐。
//
// 不含商家标识与上游渠道：会员侧只暴露商品与店铺口径，商家内部标识属运营口径。
type productView struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
	Unit string `json:"unit"`
	// Qty 是每份包含的数量，十进制字符串。
	Qty string `json:"qty"`
	// Price 是售价，十进制字符串。
	Price string `json:"price"`
	// ModelScope 是可用模型范围；null 表示不限。
	ModelScope   []string `json:"model_scope"`
	ValidityDays int      `json:"validity_days"`
}

// orderView 是一笔订单，字段与契约 Order 对齐。
//
// 金额、数量与折算率都是十进制字符串：decimal 原样序列化，不借浮点。
type orderView struct {
	ID          uint64 `json:"id"`
	ProductID   uint64 `json:"product_id"`
	ProductName string `json:"product_name"`
	Unit        string `json:"unit"`
	// Qty 是购买份数，十进制字符串。
	Qty string `json:"qty"`
	// PricePaid 是实付金额，十进制字符串。
	PricePaid string `json:"price_paid"`
	// Total 是本次购买派生的存量，十进制字符串。
	Total string `json:"total"`
	// UnitRate 是购买时刻锁定的折算率，十进制字符串。
	UnitRate string `json:"unit_rate"`
	// PurchasedAt 是购买时刻；按 RFC3339 序列化，与其余页面端点的时间字段一致。
	PurchasedAt time.Time `json:"purchased_at"`
}

// createOrderRequest 是下单请求体，字段与契约 CreateOrderRequest 对齐。
type createOrderRequest struct {
	ProductID uint64 `json:"product_id"`
	Qty       string `json:"qty"`
	// IdempotencyKey 是客户端生成的幂等键：同一键重复提交只产生一笔订单，
	// 重复提交返回首次创建的那笔。
	IdempotencyKey string `json:"idempotency_key"`
}

// newProductView 把商品档位裁剪为对外视图。
//
// 数量与售价从 DECIMAL 列读回时带列标度（写入 "10" 读回 "10.00000000"），这里归一到
// decimal.String() 的形态：契约承诺的是十进制字符串，展示层只会按单位补零，不会截尾零。
func newProductView(p store.Product) (productView, error) {
	scope, err := decodeModelScope(p.ModelScope)
	if err != nil {
		return productView{}, err
	}
	qty, err := normalizeDecimal(p.Qty)
	if err != nil {
		return productView{}, fmt.Errorf("user: 商品 %d 的数量无法解析: %w", p.ID, err)
	}
	price, err := normalizeDecimal(p.Price)
	if err != nil {
		return productView{}, fmt.Errorf("user: 商品 %d 的价格无法解析: %w", p.ID, err)
	}
	return productView{
		ID:           p.ID,
		Name:         p.Name,
		Unit:         string(p.Unit),
		Qty:          qty,
		Price:        price,
		ModelScope:   scope,
		ValidityDays: p.ValidityDays,
	}, nil
}

// normalizeDecimal 把数据库读回的定点文本归一到 decimal.String() 的形态（去掉多余尾零）。
func normalizeDecimal(raw string) (string, error) {
	value, err := decimal.NewFromString(raw)
	if err != nil {
		return "", err
	}
	return value.String(), nil
}

// decodeModelScope 解析商品档位的可用模型范围；列值为 NULL 或空时返回 nil，
// 序列化成 null 表示不限。
func decodeModelScope(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var scope []string
	if err := json.Unmarshal(raw, &scope); err != nil {
		return nil, fmt.Errorf("user: 商品 model_scope 不是字符串数组: %w", err)
	}
	return scope, nil
}

// newOrderView 把订单行裁剪为对外视图；时间统一按 RFC3339 序列化。
func newOrderView(row store.OrderRow) orderView {
	return orderView{
		ID:          row.ID,
		ProductID:   row.ProductID,
		ProductName: row.ProductName,
		Unit:        string(row.Unit),
		Qty:         row.Qty,
		PricePaid:   row.PricePaid,
		Total:       row.Total,
		UnitRate:    row.UnitRate,
		PurchasedAt: row.PurchasedAt,
	}
}

// handleProducts 列出可购买的商品档位。
//
// 目录不分页，但分页三字段照填：契约把 data 声明为 PageOfProduct，与模型目录同一形状，
// 前端的列表渲染因此共用一条路径。作用域只要求有归属账户。
func (h *Handler) handleProducts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 方法")
		return
	}
	if _, ok := h.currentAccount(w, r); !ok {
		return
	}
	products, err := h.store.ListProducts(r.Context())
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "商品目录查询失败")
		return
	}
	items := make([]productView, 0, len(products))
	for _, p := range products {
		view, err := newProductView(p)
		if err != nil {
			webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "商品目录解析失败")
			return
		}
		items = append(items, view)
	}
	total := len(items)
	webapi.WritePage(w, map[string]any{itemsKey: items}, 1, total, total)
}

// handleOrders 处理订单集合上的列出与下单。
func (h *Handler) handleOrders(w http.ResponseWriter, r *http.Request) {
	account, ok := h.currentAccount(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listOrders(w, r, account)
	case http.MethodPost:
		h.createOrder(w, r, account)
	default:
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 与 POST 方法")
	}
}

// listOrders 按偏移分页返回当前账户的订单。
func (h *Handler) listOrders(w http.ResponseWriter, r *http.Request, account *store.Account) {
	page, size, err := parsePaging(r.URL.Query())
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	orders, total, err := h.store.ListOrdersByAccount(r.Context(), account.ID, size, (page-1)*size)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "订单查询失败")
		return
	}
	items := make([]orderView, 0, len(orders))
	for _, order := range orders {
		items = append(items, newOrderView(order))
	}
	webapi.WritePage(w, map[string]any{itemsKey: items}, page, size, total)
}

// createOrder 为当前账户下单：写购买事实并派生账本，按幂等键去重。
//
// 派生的 fallback 固定为「包扣尽转扣余额」，与 internal/admin 的购买动作同一默认：
// 页面不暴露这个运营口径，下单者选不了它。
func (h *Handler) createOrder(w http.ResponseWriter, r *http.Request, account *store.Account) {
	var req createOrderRequest
	if err := decodeJSON(r, &req); err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "请求体格式非法")
		return
	}
	if req.ProductID == 0 {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "商品标识必填")
		return
	}
	qty, err := validateOrderQty(req.Qty)
	if err != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, err.Error())
		return
	}
	if keyErr := validateIdempotencyKey(req.IdempotencyKey); keyErr != nil {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, keyErr.Error())
		return
	}
	product, ok := h.lookupProduct(w, r, req.ProductID)
	if !ok {
		return
	}
	productQty, productPrice, err := parseProductAmounts(product)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "商品档位数据非法")
		return
	}
	now := h.now()
	pricePaid := productPrice.Mul(qty)
	write, err := h.store.CreateOrder(r.Context(),
		store.Purchase{
			AccountID:      account.ID,
			MerchantID:     product.MerchantID,
			ProductID:      product.ID,
			Qty:            req.Qty,
			PricePaid:      pricePaid.String(),
			PurchasedAt:    now,
			IdempotencyKey: req.IdempotencyKey,
		},
		store.BucketRow{
			AccountID:  account.ID,
			MerchantID: product.MerchantID,
			Unit:       product.Unit,
			Total:      productQty.Mul(qty).String(),
			Remaining:  productQty.Mul(qty).String(),
			ExpiresAt:  validityExpiry(now, product.ValidityDays),
			Fallback:   billing.FallbackChargeBalance,
			Source:     billing.SourcePurchase,
			Priority:   store.DefaultBucketPriority,
			UnitRate:   unitRatePtr(productPrice, productQty),
		})
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "下单失败")
		return
	}
	// 幂等命中时生效订单可能是另一次请求创建的：档位按生效订单的 product_id 取，
	// 避免把本次请求的档位口径贴到既有订单上。
	effective := product
	if write.Purchase.ProductID != product.ID {
		effective, ok = h.lookupProduct(w, r, write.Purchase.ProductID)
		if !ok {
			return
		}
	}
	order, err := store.OrderRowFrom(write.Purchase, *effective)
	if err != nil {
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "订单折算失败")
		return
	}
	webapi.WriteOK(w, newOrderView(order))
}

// lookupProduct 读一个商品档位；不存在回 404。
func (h *Handler) lookupProduct(w http.ResponseWriter, r *http.Request, id uint64) (*store.Product, bool) {
	product, err := h.store.Product(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "商品不存在")
			return nil, false
		}
		webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "商品查询失败")
		return nil, false
	}
	return product, true
}

// validateOrderQty 校验购买份数并返回解析结果。
func validateOrderQty(raw string) (decimal.Decimal, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len([]rune(trimmed)) > maxOrderQtyRunes {
		return decimal.Decimal{}, errInvalidQty
	}
	qty, err := decimal.NewFromString(trimmed)
	if err != nil || !qty.IsPositive() {
		return decimal.Decimal{}, errInvalidQty
	}
	return qty, nil
}

// validateIdempotencyKey 校验幂等键：必填且长度在契约范围内。
func validateIdempotencyKey(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < idempotencyKeyMinLen || len(trimmed) > idempotencyKeyMaxLen {
		return errInvalidIdempotencyKey
	}
	return nil
}

// parseProductAmounts 解析商品档位的每份数量与售价。
//
// 解析失败回 500 而不是 400：档位数据由运营动作写入且写入口已校验，读不出来的档位是
// 服务端的数据问题，不是下单者的请求问题。
func parseProductAmounts(product *store.Product) (decimal.Decimal, decimal.Decimal, error) {
	qty, err := decimal.NewFromString(product.Qty)
	if err != nil {
		return decimal.Decimal{}, decimal.Decimal{}, fmt.Errorf("user: 商品 %d 的数量无法解析: %w", product.ID, err)
	}
	price, err := decimal.NewFromString(product.Price)
	if err != nil {
		return decimal.Decimal{}, decimal.Decimal{}, fmt.Errorf("user: 商品 %d 的价格无法解析: %w", product.ID, err)
	}
	return qty, price, nil
}

// unitRatePtr 算折算率并取地址；nil 与 0 在扣减语义下同义，这里始终给出取值。
func unitRatePtr(price, qty decimal.Decimal) *string {
	rate := store.DeriveUnitRate(price, qty)
	return &rate
}

// validityExpiry 按商品有效天数算派生账本的到期时刻；0 天表示不过期。
func validityExpiry(now time.Time, days int) *time.Time {
	if days <= 0 {
		return nil
	}
	expires := now.AddDate(0, 0, days)
	return &expires
}
