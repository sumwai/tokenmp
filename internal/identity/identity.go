// Package identity 是页面身份与能力的唯一出处。
//
// 身份是叠加而非互斥的：member 是所有主体的基线，partner（申请制的商家）在基线
// 之上，admin 同时持有前两者。web_user.role 里存的是已授予的最高身份，本包把它
// 展开成身份集合，再把各身份的能力取并集 —— 会话响应（SessionUser）与控制台
// 清单（ConsoleData）都由本包生成，前端只按能力渲染导航与权限，不按身份取值分支。
//
// 能力标识的取值与 docs/openapi-web.yaml 的 ConsoleData.capabilities 一致，改动
// 须同步契约；取值只增不改语义：清单随页面增加而增长，前端对未识别的标识与
// 未登记的图标名都不作错误处理。
package identity

import "github.com/sumwai/tokenmp/internal/store"

// 控制台能力标识。
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

// stacks 是身份的叠加关系：每个身份都含它下面的全部身份。
//
// 表按「已授予的最高身份 → 全部身份」写，展开后基线在前、顺序固定，使同一主体的
// 响应可比对；新增身份只在表里加一行，无需改动任何取值处。
var stacks = map[string][]string{
	store.RoleMember:  {store.RoleMember},
	store.RolePartner: {store.RoleMember, store.RolePartner},
	store.RoleAdmin:   {store.RoleMember, store.RolePartner, store.RoleAdmin},
}

// roleCapabilities 是「身份 → 能力」的唯一出处：改谁能看到什么只动本表。
//
// 三种身份都拥有自己的账户、都能签发密钥并调用模型，因此数据面能力对三者相同；
// 平台管理员另有管理面能力。商家面是否进控制台尚未定论（见 issue #143），
// 定论只落在本表。
var roleCapabilities = map[string][]string{
	store.RoleMember:  {CapConsole, CapKeys, CapRequests, CapUsage, CapAccount},
	store.RolePartner: {CapConsole, CapKeys, CapRequests, CapUsage, CapAccount},
	store.RoleAdmin:   {CapConsole, CapKeys, CapRequests, CapUsage, CapAccount, CapOps},
}

// Roles 把账号已授予的最高身份展开成叠加后的身份集合。
//
// 未登记的身份得到空集合：按无权处理，而不是回落到基线的身份 —— 回落会让越权在
// 配置出错时静默生效。返回空切片而不是 nil，序列化成 [] 而非 null。
func Roles(role string) []string {
	held := stacks[role]
	return append(make([]string, 0, len(held)), held...)
}

// Held 只保留已登记的身份并按出现顺序去重。
//
// 清单回显的是主体确实持有的身份：未登记的取值（写入侧有白名单，正常不会出现）
// 不作为身份出现，重复取值也不重复回显 —— 回显一份不可信的身份集合会让页面显示
// 出不存在的身份。
func Held(roles []string) []string {
	held := make([]string, 0, len(roles))
	for _, role := range roles {
		if _, ok := stacks[role]; ok && !contains(held, role) {
			held = append(held, role)
		}
	}
	return held
}

// Capabilities 取身份集合的能力并集，按身份顺序去重。
//
// 去重是叠加语义的要求：admin 含 partner 与 member，逐个身份拼接会把数据面能力
// 重复三次。返回空切片而不是 nil，理由同 Roles。
func Capabilities(roles []string) []string {
	caps := make([]string, 0, len(roleCapabilities[store.RoleAdmin]))
	for _, role := range roles {
		for _, cap := range roleCapabilities[role] {
			if !contains(caps, cap) {
				caps = append(caps, cap)
			}
		}
	}
	return caps
}

// contains 判断能力集合里是否已有目标能力。
func contains(caps []string, want string) bool {
	for _, cap := range caps {
		if cap == want {
			return true
		}
	}
	return false
}
