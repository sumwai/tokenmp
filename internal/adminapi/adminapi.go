// Package adminapi 实现 /api/v1/admin/* 管理面端点：只读清单与上游账号的配置动作。
//
// 与 internal/user 的分工：user 端点的作用域由会话推导，只回答「这个主体自己的
// 账户」；本包面向平台管理员，列的是跨主体、跨商家的运营对象，因此清单里的行带
// merchant_id / account_id 这类归属标识。两者共用页面信封（internal/webapi）与
// 会话令牌（internal/auth 的 SessionSubject），差别只在权限判定与作用域。
//
// 读路径不自己拼 SQL：清单来自 internal/admin 的业务读取，字段与
// `tokenmp admin <组> list --json` 的行一一对应，页面与 CLI 不会各算一套。
// 配置动作同理：新增商家、渠道、凭据与模型映射调用 internal/admin 的同名业务动作，
// 校验、默认值与派生计算只在那一处，本包只做请求解析、错误码翻译与响应写出。
//
// 动作类端点的规模与读路径不同：启动上游需要交互过程（OAuth 设备码轮询）或
// 属于别的运营域（定价、限额、调账、商品、账户）的动作仍只在 `tokenmp admin` 里，
// 未在本包开 HTTP 入口。
package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/admin"
	"github.com/sumwai/tokenmp/internal/billing"
	"github.com/sumwai/tokenmp/internal/identity"
	"github.com/sumwai/tokenmp/internal/settlement"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 管理面端点的固定路径。取值与 docs/openapi-web.yaml 一致，改动须同步契约。
const (
	// PathPrefix 是管理面子树前缀，装配层按它挂载。
	PathPrefix = "/api/v1/admin/"
	// ChannelsPath 是渠道清单与新建渠道的固定路径。
	ChannelsPath = "/api/v1/admin/channels"
	// CredentialsPath 是上游凭据清单与写入凭据的固定路径。
	CredentialsPath = "/api/v1/admin/credentials" //nolint:gosec // G101：这是 URL 路径，不是凭据
	// ModelMapsPath 是渠道模型映射清单与写入映射的固定路径。
	ModelMapsPath = "/api/v1/admin/modelmaps"
	// MerchantsPath 是商家清单与新建商家的固定路径。
	MerchantsPath = "/api/v1/admin/merchants"
	// AccountsPath 是账户清单的固定路径。
	AccountsPath = "/api/v1/admin/accounts"
	// PricingPath 是定价版本清单的固定路径。
	PricingPath = "/api/v1/admin/pricing"
	// QuotasPath 是窗口限额清单的固定路径。
	QuotasPath = "/api/v1/admin/quotas"
	// AdjustmentsPath 是调账清单的固定路径。
	AdjustmentsPath = "/api/v1/admin/adjustments"
	// UsagePath 是全平台用量流水的固定路径。
	UsagePath = "/api/v1/admin/usage"
	// RequestsPath 是全平台请求记录的固定路径。
	RequestsPath = "/api/v1/admin/requests"
	// SettlementsPath 是全平台结算对账单的固定路径。
	SettlementsPath = "/api/v1/admin/settlements"
)

// itemsKey 是列表数据的字段名，与契约里各 PageOf* 的形状一致。
const itemsKey = "items"

// 分页默认值与上限，与契约的 Page / Size 参数一致。
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// errInvalidPaging 是 page / size 非法时的文案。
var errInvalidPaging = errors.New("page 与 size 必须是正整数，size 上限 100")

// Sessions 解析页面会话令牌，由 internal/auth 的会话实现满足。
type Sessions interface {
	// SessionSubject 解析访问令牌并返回登录主体 id 与身份集合；令牌无效时返回错误。
	SessionSubject(ctx context.Context, accessToken string) (userID uint64, roles []string, err error)
}

// Lister 是管理面只读清单依赖的业务读取面，由 internal/admin 的 Service 满足。
//
// 只列清单用到的读取动作：本包不写库，写入一律经 Writer 的业务动作。
type Lister interface {
	ListMerchants(ctx context.Context) ([]store.Merchant, error)
	ListChannels(ctx context.Context) ([]store.Channel, error)
	ListCredentials(ctx context.Context) ([]admin.CredentialView, error)
	ListModelMaps(ctx context.Context) ([]store.ModelMap, error)
	ListAccounts(ctx context.Context) ([]store.Account, error)
	ListPricing(ctx context.Context, merchantID uint64, model string) ([]store.Pricing, error)
	ListQuotas(ctx context.Context, scope billing.Scope, scopeID uint64) ([]admin.QuotaView, error)
	ListAdjustments(ctx context.Context, accountID uint64) ([]store.Adjustment, error)
	ListUsage(ctx context.Context, accountID uint64, since time.Time) ([]store.UsageListRow, error)
	// ListRequests 分页列出全平台请求记录；账户为 0 表示跨账户。
	ListRequests(ctx context.Context, q admin.RequestListQuery) ([]admin.RequestView, int, error)
	// SettlementBills 按账期出账，返回每个商家的对账单；缺省账期按各商家口径取上一期。
	SettlementBills(ctx context.Context, q admin.SettlementQuery) ([]settlement.BillView, error)
}

// Writer 是管理面配置动作依赖的业务写入面，由 internal/admin 的 Service 满足。
//
// 与 Lister 分开：只读清单的测试替身不必把写入方法也实现一遍。生产装配里两者是
// 同一个实例，页面与 CLI 因此走同一条业务路径。
type Writer interface {
	CreateMerchant(ctx context.Context, code, name string, kind store.MerchantKind) (uint64, error)
	UpdateMerchant(ctx context.Context, id uint64, code, name string, kind store.MerchantKind) error
	EnableMerchant(ctx context.Context, id uint64) error
	DisableMerchant(ctx context.Context, id uint64) error
	SetMerchantOwner(ctx context.Context, id, userID uint64) error

	CreateChannel(ctx context.Context, in admin.ChannelInput) (uint64, error)
	UpdateChannel(ctx context.Context, id uint64, in admin.ChannelInput) error
	EnableChannel(ctx context.Context, id uint64) error
	DisableChannel(ctx context.Context, id uint64) error

	AddCredential(ctx context.Context, in admin.CredentialInput) (uint64, error)
	UpdateCredential(ctx context.Context, id uint64, in admin.CredentialInput) error
	EnableCredential(ctx context.Context, id uint64) error
	DisableCredential(ctx context.Context, id uint64) error

	SetModelMap(ctx context.Context, in admin.ModelMapInput) (uint64, error)
	UpdateModelMap(ctx context.Context, id uint64, in admin.ModelMapInput) error
	DisableModelMap(ctx context.Context, id uint64) error
}

// Options 是装配参数；Sessions 与 Lister 必填，Writer 缺失时配置动作端点不挂载。
type Options struct {
	Sessions Sessions
	Lister   Lister
	// Writer 是配置动作的业务写入面；为 nil 时写端点回 404，只读清单不受影响。
	Writer Writer
}

// Handler 是管理面端点的 HTTP 入口，按路径分发到各清单与配置动作。
type Handler struct {
	sessions Sessions
	lister   Lister
	writer   Writer
}

// NewHandler 构造管理面端点处理器。
func NewHandler(opts Options) *Handler {
	return &Handler{sessions: opts.Sessions, lister: opts.Lister, writer: opts.Writer}
}

// ServeHTTP 按路径分发到各动作；子树内未声明的路径回页面信封 404。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 权限判定先于路径分派：未持有管理面身份时不透露子树里有哪些端点。
	if _, ok := h.currentAdmin(w, r); !ok {
		return
	}
	switch {
	case r.URL.Path == MerchantsPath:
		h.handleMerchants(w, r)
	case strings.HasPrefix(r.URL.Path, MerchantsPath+"/"):
		h.handleMerchantAction(w, r)
	case r.URL.Path == ChannelsPath:
		h.handleChannels(w, r)
	case strings.HasPrefix(r.URL.Path, ChannelsPath+"/"):
		h.handleChannelAction(w, r)
	case r.URL.Path == CredentialsPath:
		h.handleCredentials(w, r)
	case strings.HasPrefix(r.URL.Path, CredentialsPath+"/"):
		h.handleCredentialAction(w, r)
	case r.URL.Path == ModelMapsPath:
		h.handleModelMaps(w, r)
	case strings.HasPrefix(r.URL.Path, ModelMapsPath+"/"):
		h.handleModelMapAction(w, r)
	case r.URL.Path == AccountsPath:
		h.handleAccounts(w, r)
	case r.URL.Path == PricingPath:
		h.handlePricing(w, r)
	case r.URL.Path == QuotasPath:
		h.handleQuotas(w, r)
	case r.URL.Path == AdjustmentsPath:
		h.handleAdjustments(w, r)
	case r.URL.Path == UsagePath:
		h.handleUsage(w, r)
	case r.URL.Path == RequestsPath:
		h.handleRequests(w, r)
	case r.URL.Path == SettlementsPath:
		h.handleSettlements(w, r)
	default:
		webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "端点不存在")
	}
}

// currentAdmin 解析会话并要求管理面身份；未登录回 401，已登录但无该身份回 403。
//
// 两个码分开：401 的下一步是重新登录，403 的下一步是换一个有权限的账号，
// 折叠成同一个码前端就无从区分。
func (h *Handler) currentAdmin(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	token, ok := webapi.BearerToken(r.Header.Get("Authorization"))
	if !ok {
		webapi.WriteError(w, http.StatusUnauthorized, webapi.CodeUnauthorized, "登录状态已失效")
		return 0, false
	}
	userID, roles, err := h.sessions.SessionSubject(r.Context(), token)
	if err != nil {
		webapi.WriteError(w, http.StatusUnauthorized, webapi.CodeUnauthorized, "登录状态已失效")
		return 0, false
	}
	if !holdsAdmin(roles) {
		webapi.WriteError(w, http.StatusForbidden, webapi.CodeForbidden, "无管理面权限")
		return 0, false
	}
	return userID, true
}

// holdsAdmin 判定主体是否持有管理面能力。
//
// 「谁能看到管理面」的唯一出处是 internal/identity 的「身份 → 能力」表：本处按
// ops 能力判定，与控制台清单里管理面条目的门槛是同一个标识，两者不会各写一套。
// 身份是叠加的，因此按各身份的并集判定，admin 之外的身份将来只要被授予该能力即生效。
func holdsAdmin(roles []string) bool {
	for _, capability := range identity.Capabilities(identity.Held(roles)) {
		if capability == identity.CapOps {
			return true
		}
	}
	return false
}

// pageQuery 是清单端点的分页参数。
type pageQuery struct {
	page int
	size int
}

// parsePage 解析分页参数：缺省第 1 页、每页 20 条，size 上限与契约一致。
func parsePage(query url.Values) (pageQuery, error) {
	page, err := parsePositive(query, "page", 1)
	if err != nil {
		return pageQuery{}, err
	}
	size, err := parsePositive(query, "size", defaultPageSize)
	if err != nil || size > maxPageSize {
		return pageQuery{}, errInvalidPaging
	}
	return pageQuery{page: page, size: size}, nil
}

// parsePositive 读一个正整数查询参数；缺省取 defaultValue。
func parsePositive(query url.Values, name string, defaultValue int) (int, error) {
	raw := strings.TrimSpace(query.Get(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, errInvalidPaging
	}
	return value, nil
}

// parseOptionalUint 读一个可选的正整数查询参数；缺省为 0，表示不过滤。
func parseOptionalUint(query url.Values, name string) (uint64, error) {
	raw := strings.TrimSpace(query.Get(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s 必须是正整数", name)
	}
	return value, nil
}

// pageSlice 切出当页；返回的切片非 nil，序列化成数组而不是 null。
func pageSlice[T any](items []T, page pageQuery) ([]T, int) {
	total := len(items)
	start := (page.page - 1) * page.size
	if start >= total {
		return []T{}, total
	}
	end := start + page.size
	if end > total {
		end = total
	}
	// 复制一份：结果与底层数组共享内存没有意义，跨请求持有更不该发生。
	kept := make([]T, end-start)
	copy(kept, items[start:end])
	return kept, total
}

// writeItems 写一个清单响应：data 是 {items: […] }，分页三字段取自信封。
func writeItems[T any](w http.ResponseWriter, items []T, page pageQuery, total int) {
	webapi.WritePage(w, map[string]any{itemsKey: items}, page.page, page.size, total)
}

// requireGet 只接受 GET：没有写入口的清单只有读方法。
func requireGet(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 方法")
		return false
	}
	return true
}

// requireWriter 确认配置动作的业务面已挂载；未挂载时按端点不存在处理。
//
// 生产装配里 Writer 恒非 nil（存储层满足管理面数据面才挂载整个子树）；
// 只读清单的测试替身不实现写入，此时写端点应当像没声明过一样回 404。
func (h *Handler) requireWriter(w http.ResponseWriter) bool {
	if h.writer == nil {
		webapi.WriteError(w, http.StatusNotFound, webapi.CodeNotFound, "端点不存在")
		return false
	}
	return true
}

// writeInternal 统一的服务端错误文案。
func writeInternal(w http.ResponseWriter) {
	webapi.WriteError(w, http.StatusInternalServerError, webapi.CodeInternal, "服务端错误")
}
