package user

import (
	"net/http"

	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件实现控制台清单端点：当前登录主体可用的导航项、首页段落与能力集合。
//
// 角色与权限的真相在服务端（web/AGENTS.md「多角色」）：前端按清单渲染，不硬编码
// 角色取值。清单因此由两张表生成 —— 「角色 → 能力」与「能力 → 条目」：改谁能看到
// 什么只动第一张表，改条目的位置与图标只动第二张表。

// 控制台能力标识。取值与 docs/openapi-web.yaml 的 ConsoleData.capabilities 一致，
// 改动须同步契约。
//
// 取值只增不改语义：清单随页面增加而增长，前端对未识别的标识与未登记的图标名都
// 不作错误处理。
const (
	// CapConsole 是控制台首页。
	CapConsole = "console"
	// CapKeys 是密钥自助管理。
	CapKeys = "keys"
	// CapRequests 是请求记录查看。
	CapRequests = "requests"
	// CapUsage 是用量与应扣量查看。
	CapUsage = "usage"
	// CapAccount 是账户概览查看。
	CapAccount = "account"
	// CapOps 是管理面能力；管理面页面尚未定义，当前只作为能力出现。
	CapOps = "ops"
)

// roleCapabilities 是「角色 → 能力」的唯一出处。
//
// 三种角色都拥有自己的账户、都能签发密钥并调用模型，因此数据面能力对三者相同；
// 平台管理员另有管理面能力。商家面是否进控制台尚未定论（见 #143）：定论只落在
// 本表，契约与前端都不必跟着改。
var roleCapabilities = map[string][]string{
	store.RoleMember:  {CapConsole, CapKeys, CapRequests, CapUsage, CapAccount},
	store.RolePartner: {CapConsole, CapKeys, CapRequests, CapUsage, CapAccount},
	store.RoleAdmin:   {CapConsole, CapKeys, CapRequests, CapUsage, CapAccount, CapOps},
}

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
	{Title: "首页", Icon: "house", Path: "/", Capability: CapConsole},
	{Title: "密钥", Description: "签发与吊销调用密钥", Icon: "key-round", Path: "/keys", Capability: CapKeys},
	{Title: "用量", Description: "token 与应扣量", Icon: "chart-line", Path: "/usage", Capability: CapUsage},
	{Title: "我的", Description: "包存量与窗口限额", Icon: "user-round", Path: "/account", Capability: CapAccount},
}

// consoleSections 是首页段落目录。
//
// 放不占导航的实体入口：日志类实体走全屏列表页，首页只提供预览入口
// （web/AGENTS.md 的移动端约定）。
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
				Capability:  CapRequests,
			},
		},
	},
}

// consoleView 是清单的响应体，字段与契约 ConsoleData 对齐。
type consoleView struct {
	Role         string           `json:"role"`
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
	webapi.WriteOK(w, consoleFor(subject.role))
}

// consoleFor 按角色生成控制台清单。
//
// 未知角色（写入侧有白名单，正常不会出现）得到空清单：按无权处理，而不是回落到
// 某个角色的清单 —— 回落会让越权在配置出错时静默生效。
func consoleFor(role string) consoleView {
	caps := roleCapabilities[role]
	return consoleView{
		Role:         role,
		Capabilities: append(make([]string, 0, len(caps)), caps...),
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
