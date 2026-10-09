package user

import (
	"net/http"

	"github.com/sumwai/tokenmp/internal/identity"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件实现控制台清单端点：当前登录主体可用的导航项、首页段落与能力集合。
//
// 身份与权限的真相在服务端（web/AGENTS.md「多角色」）：前端按清单渲染，不硬编码
// 身份取值。清单因此由三张表生成 —— internal/identity 的「身份 → 能力」与
// 「身份 → 全部身份」，以及本文件的「能力 → 条目」：改谁能看到什么只动第一张表，
// 改条目的位置与图标只动第二张表。

// 控制台条目所需的能力标识由 internal/identity 定义；清单的条目按这些标识声明
// 依赖，能力集合则由服务端按身份取并集下发。

// consoleEntry 是一条控制台条目，字段与契约 ConsoleEntry 对齐。
type consoleEntry struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Path        string `json:"path"`
	Capability  string `json:"capability"`
}

// consoleSection 是首页的一个段落，字段与契约 ConsoleSection 对齐。
type consoleSection struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Entries     []consoleEntry `json:"entries"`
}

// consoleNavigation 是底部导航项目录。
//
// 只放任务域，数量不超过 5（web/AGENTS.md 的移动端约定）；图标取 Lucide 名。
var consoleNavigation = []consoleEntry{
	{Title: "首页", Icon: "house", Path: "/", Capability: identity.CapConsole},
	{Title: "密钥", Description: "签发与吊销调用密钥", Icon: "key-round", Path: "/keys", Capability: identity.CapKeys},
	{Title: "用量", Description: "token 与应扣量", Icon: "chart-line", Path: "/usage", Capability: identity.CapUsage},
	{Title: "充值", Description: "购买存量与订单", Icon: "wallet", Path: "/purchase", Capability: identity.CapPurchase},
	{Title: "我的", Description: "包存量与窗口限额", Icon: "user-round", Path: "/account", Capability: identity.CapAccount},
}

// consoleSections 是首页段落目录。
//
// 放不占导航的实体入口：日志类实体走全屏列表页，首页只提供预览入口
// （web/AGENTS.md 的移动端约定）。底部导航只放任务域且不超过 5 项，商家域与请求记录
// 因此都落在段落里。管理面段落的条目都指向 /api/v1/admin/* 的只读清单，整体以 ops
// 能力为门槛：不具备该能力的主体看不到，也不该取到那些端点。
var consoleSections = []consoleSection{
	{
		Title:       "排障",
		Description: "按请求标识定位问题",
		Entries: []consoleEntry{
			{
				Title:       "请求记录",
				Description: "脱敏报文与尝试时间线",
				Icon:        "scroll-text",
				Path:        "/requests",
				Capability:  identity.CapRequests,
			},
		},
	},
	{
		Title:       "商家",
		Description: "上游账号与名下调用量",
		Entries: []consoleEntry{
			{
				Title:       "上游账号",
				Description: "渠道与凭据的登记、启停与名下调用量",
				Icon:        "store",
				Path:        "/partner",
				Capability:  identity.CapPartner,
			},
			{
				Title:       "结算",
				Description: "按账期的收益、平台抽成与上游成本",
				Icon:        "hand-coins",
				Path:        "/partner/settlement",
				Capability:  identity.CapPartner,
			},
		},
	},
	{
		Title:       "管理面",
		Description: "平台运营对象的只读清单",
		Entries: []consoleEntry{
			{
				Title:       "渠道",
				Description: "上游渠道与启用状态",
				Icon:        "route",
				Path:        "/admin/channels",
				Capability:  identity.CapOps,
			},
			{
				Title:       "凭据",
				Description: "上游凭据的形态与脱敏前缀",
				Icon:        "shield-check",
				Path:        "/admin/credentials",
				Capability:  identity.CapOps,
			},
			{
				Title:       "模型映射",
				Description: "渠道的模型替换与倍率",
				Icon:        "shuffle",
				Path:        "/admin/modelmaps",
				Capability:  identity.CapOps,
			},
			{
				Title:       "账户",
				Description: "账户归属与状态",
				Icon:        "users",
				Path:        "/admin/accounts",
				Capability:  identity.CapOps,
			},
			{
				Title:       "价格",
				Description: "定价版本与生效状态",
				Icon:        "tag",
				Path:        "/admin/pricing",
				Capability:  identity.CapOps,
			},
			{
				Title:       "限额",
				Description: "窗口限额与当前用量",
				Icon:        "gauge",
				Path:        "/admin/quotas",
				Capability:  identity.CapOps,
			},
			{
				Title:       "调账",
				Description: "人工补扣与退费记录",
				Icon:        "scale",
				Path:        "/admin/adjustments",
				Capability:  identity.CapOps,
			},
			{
				Title:       "流水",
				Description: "全平台用量流水",
				Icon:        "receipt",
				Path:        "/admin/usage",
				Capability:  identity.CapOps,
			},
			{
				Title:       "结算",
				Description: "各商家的分账对账单",
				Icon:        "landmark",
				Path:        "/admin/settlements",
				Capability:  identity.CapOps,
			},
		},
	},
}

// consoleView 是清单的响应体，字段与契约 ConsoleData 对齐。
type consoleView struct {
	Roles        []string         `json:"roles"`
	Capabilities []string         `json:"capabilities"`
	Navigation   []consoleEntry   `json:"navigation"`
	Sections     []consoleSection `json:"sections"`
}

// handleConsole 返回当前主体的控制台清单。
func (h *Handler) handleConsole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webapi.WriteError(w, http.StatusBadRequest, webapi.CodeBadRequest, "只支持 GET 方法")
		return
	}
	subject, ok := h.currentSubject(w, r)
	if !ok {
		return
	}
	// 清单只依赖主体，但仍要求有归属账户：清单里的条目都指向挂在账户上的数据，
	// 没有账户的主体在控制台里无事可做，与其余用户级端点回同一个 403。
	if _, ok := h.ownedAccount(w, r, subject.userID); !ok {
		return
	}
	webapi.WriteOK(w, consoleFor(subject.roles))
}

// consoleFor 按主体的身份集合生成控制台清单：能力取各身份的并集，条目再按能力过滤。
//
// 未登记身份（写入侧有白名单，正常不会出现）得到空清单：按无权处理，而不是回落到
// 基线身份的清单 —— 回落会让越权在配置出错时静默生效。
func consoleFor(roles []string) consoleView {
	held := identity.Held(roles)
	caps := identity.Capabilities(held)
	return consoleView{
		Roles:        held,
		Capabilities: caps,
		Navigation:   filterEntries(consoleNavigation, caps),
		Sections:     filterSections(consoleSections, caps),
	}
}

// filterEntries 保留能力集合内的条目。
//
// 返回空切片而不是 nil：契约把 navigation 与 entries 声明为数组，序列化成 null
// 会让前端多一条「空清单」与「字段缺失」的分支。
func filterEntries(entries []consoleEntry, caps []string) []consoleEntry {
	kept := make([]consoleEntry, 0, len(entries))
	for _, entry := range entries {
		if allows(caps, entry.Capability) {
			kept = append(kept, entry)
		}
	}
	return kept
}

// filterSections 过滤各段落的条目，并丢掉条目被过滤光的段落：
// 没有入口的段落只剩标题，对用户没有信息。
func filterSections(sections []consoleSection, caps []string) []consoleSection {
	kept := make([]consoleSection, 0, len(sections))
	for _, section := range sections {
		entries := filterEntries(section.Entries, caps)
		if len(entries) == 0 {
			continue
		}
		kept = append(kept, consoleSection{
			Title:       section.Title,
			Description: section.Description,
			Entries:     entries,
		})
	}
	return kept
}

// allows 判断能力集合里是否含目标能力。
func allows(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}
