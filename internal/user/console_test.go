package user

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/sumwai/tokenmp/internal/identity"
	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件覆盖控制台清单端点：会话与账户两道判定、按身份并集的能力集合、条目过滤与
// 清单的空值形状。判定过程不经数据库。

// consoleDataOf 解出清单响应里的 data。
func consoleDataOf(t *testing.T, env map[string]json.RawMessage) consoleView {
	t.Helper()
	var data consoleView
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("解析 data: %v", err)
	}
	return data
}

// pathsOf 取一组条目的路径，用于断言导航与段落的构成。
func pathsOf(entries []consoleEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	return paths
}

// TestConsoleUnauthorized 断言缺少令牌与令牌无效都回 401。
func TestConsoleUnauthorized(t *testing.T) {
	e := newTestEnv()
	status, env := e.do(t, http.MethodGet, ConsolePath, "", "")
	if status != http.StatusUnauthorized || codeOf(t, env) != webapi.CodeUnauthorized {
		t.Fatalf("缺少令牌应 401: %d %s", status, env)
	}

	e.session.err = context.DeadlineExceeded
	status, env = e.do(t, http.MethodGet, ConsolePath, "stale", "")
	if status != http.StatusUnauthorized || codeOf(t, env) != webapi.CodeUnauthorized {
		t.Fatalf("令牌无效应 401: %d %s", status, env)
	}
}

// TestConsoleWithoutOwnedAccount 断言会话有效但没有归属账户时回 403：
// 清单里的条目都指向挂在账户上的数据，没有账户就没有可用的控制台。
func TestConsoleWithoutOwnedAccount(t *testing.T) {
	e := newTestEnv()
	e.store.err = sql.ErrNoRows
	status, env := e.do(t, http.MethodGet, ConsolePath, "token", "")
	if status != http.StatusForbidden || codeOf(t, env) != webapi.CodeForbidden {
		t.Fatalf("无归属账户应 403: %d %s", status, env)
	}
}

// TestConsoleRejectsNonGet 断言非 GET 方法回 400。
func TestConsoleRejectsNonGet(t *testing.T) {
	e := newTestEnv()
	status, env := e.do(t, http.MethodPost, ConsolePath, "token", "")
	if status != http.StatusBadRequest || codeOf(t, env) != webapi.CodeBadRequest {
		t.Fatalf("非 GET 应 400: %d %s", status, env)
	}
}

// TestConsolePerIdentity 覆盖三种身份的清单：身份是叠加的（库里只存最高身份，
// 响应下发它包含的全部身份），差异只来自服务端的能力集合，条目按能力过滤，
// 因此前端不需要按身份分支。
func TestConsolePerIdentity(t *testing.T) {
	dataCaps := []string{identity.CapConsole, identity.CapKeys, identity.CapRequests, identity.CapUsage, identity.CapAccount}
	tests := []struct {
		name       string
		role       string
		wantRoles  []string
		wantCaps   []string
		wantNav    []string
		wantSectNo int
	}{
		{
			name:       "调用方",
			role:       store.RoleMember,
			wantRoles:  []string{store.RoleMember},
			wantCaps:   dataCaps,
			wantNav:    []string{"/", "/keys", "/usage", "/account"},
			wantSectNo: 1,
		},
		{
			name:       "商户",
			role:       store.RolePartner,
			wantRoles:  []string{store.RoleMember, store.RolePartner},
			wantCaps:   dataCaps,
			wantNav:    []string{"/", "/keys", "/usage", "/account"},
			wantSectNo: 1,
		},
		{
			// 平台管理员另有管理面能力，因此多出管理面段落；导航与其余身份一致。
			name:       "平台管理员",
			role:       store.RoleAdmin,
			wantRoles:  []string{store.RoleMember, store.RolePartner, store.RoleAdmin},
			wantCaps:   append(slices.Clone(dataCaps), identity.CapOps),
			wantNav:    []string{"/", "/keys", "/usage", "/account"},
			wantSectNo: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv()
			e.session.roles = identity.Roles(tt.role)
			status, env := e.do(t, http.MethodGet, ConsolePath, "token", "")
			if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
				t.Fatalf("应 200: %d %s", status, env)
			}
			data := consoleDataOf(t, env)
			if !slices.Equal(data.Roles, tt.wantRoles) {
				t.Errorf("roles = %v，期望 %v", data.Roles, tt.wantRoles)
			}
			if !slices.Equal(data.Capabilities, tt.wantCaps) {
				t.Errorf("capabilities = %v，期望 %v", data.Capabilities, tt.wantCaps)
			}
			if got := pathsOf(data.Navigation); !slices.Equal(got, tt.wantNav) {
				t.Errorf("navigation = %v，期望 %v", got, tt.wantNav)
			}
			if len(data.Sections) != tt.wantSectNo {
				t.Fatalf("sections = %d 段，期望 %d 段", len(data.Sections), tt.wantSectNo)
			}
			for _, section := range data.Sections {
				if len(section.Entries) == 0 {
					t.Errorf("段落 %q 没有条目，条目被过滤光的段落不应返回", section.Title)
				}
			}
		})
	}
}

// TestConsoleAdminSectionPaths 断言管理面段落的条目路径与契约里的清单端点一一对应：
// 页面路由与 `docs/openapi-web.yaml` 的 /api/v1/admin/* 同段名，改名会在这里被发现。
func TestConsoleAdminSectionPaths(t *testing.T) {
	sections := filterSections(consoleSections, []string{identity.CapOps})
	if len(sections) != 1 {
		t.Fatalf("持有 %s 时应恰有一段管理面，得到 %d 段", identity.CapOps, len(sections))
	}
	want := []string{
		"/admin/channels",
		"/admin/credentials",
		"/admin/modelmaps",
		"/admin/accounts",
		"/admin/pricing",
		"/admin/quotas",
		"/admin/adjustments",
		"/admin/usage",
	}
	got := pathsOf(sections[0].Entries)
	if len(got) != len(want) {
		t.Fatalf("管理面条目 = %v，期望 %v", got, want)
	}
	for i, path := range want {
		if got[i] != path {
			t.Errorf("管理面条目[%d] = %q，期望 %q", i, got[i], path)
		}
	}
}

// TestConsoleCapabilitiesAreUnion 断言同一主体持有多个身份时能力集合是各身份的并集
// 且不重复：管理员的身份集合含 partner 与 member，逐个身份拼接会把数据面能力重复三次。
func TestConsoleCapabilitiesAreUnion(t *testing.T) {
	e := newTestEnv()
	// 顺序也刻意打乱：并集不随身份顺序漂移。
	e.session.roles = []string{store.RoleAdmin, store.RoleMember, store.RolePartner}
	status, env := e.do(t, http.MethodGet, ConsolePath, "token", "")
	if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
		t.Fatalf("应 200: %d %s", status, env)
	}
	data := consoleDataOf(t, env)
	want := []string{identity.CapConsole, identity.CapKeys, identity.CapRequests, identity.CapUsage, identity.CapAccount, identity.CapOps}
	if !slices.Equal(data.Capabilities, want) {
		t.Fatalf("capabilities = %v，期望并集 %v", data.Capabilities, want)
	}
	// 条目同样不重复：能力重复会让同一入口在导航里出现多次。
	if got := pathsOf(data.Navigation); !slices.Equal(got, []string{"/", "/keys", "/usage", "/account"}) {
		t.Fatalf("navigation = %v，期望每条目一次", got)
	}
}

// TestConsoleFiltersByCapability 断言条目按能力过滤：条目所需的能力不在集合里时
// 该条目不出现，段落条目被过滤光时整段不返回。
func TestConsoleFiltersByCapability(t *testing.T) {
	// 缺少请求记录能力：唯一含该能力的段落整体消失，导航不含相关条目。
	caps := []string{identity.CapConsole, identity.CapKeys, identity.CapUsage, identity.CapAccount}
	if nav := filterEntries(consoleNavigation, caps); len(nav) != len(consoleNavigation) {
		t.Fatalf("导航项都不需要 %s，应全部保留，得到 %d 项", identity.CapRequests, len(nav))
	}
	if sections := filterSections(consoleSections, caps); len(sections) != 0 {
		t.Fatalf("缺少 %s 时应没有段落，得到 %d 段", identity.CapRequests, len(sections))
	}

	// 只剩首页能力：导航只剩首页项，段落同样为空。
	if nav := filterEntries(consoleNavigation, []string{identity.CapConsole}); len(nav) != 1 || nav[0].Path != "/" {
		t.Fatalf("只剩首页能力时导航应只有首页项，得到 %v", pathsOf(nav))
	}

	// 空能力集合：导航与段落都为空切片而不是 nil。
	if nav := filterEntries(consoleNavigation, nil); nav == nil || len(nav) != 0 {
		t.Fatalf("空能力集合应得到空切片，得到 %v", nav)
	}
	if sections := filterSections(consoleSections, nil); sections == nil || len(sections) != 0 {
		t.Fatalf("空能力集合应得到空切片，得到 %v", sections)
	}
}

// TestConsoleUnknownIdentityIsEmpty 断言未登记的身份得到空清单：越权在配置出错时
// 不能因为回落到基线身份而静默生效。
func TestConsoleUnknownIdentityIsEmpty(t *testing.T) {
	for _, roles := range [][]string{nil, {"nobody"}} {
		data := consoleFor(roles)
		if len(data.Roles) != 0 || len(data.Capabilities) != 0 || len(data.Navigation) != 0 || len(data.Sections) != 0 {
			t.Fatalf("未登记身份 %v 应得到空清单，得到 %s", roles, data)
		}
	}
}

// TestConsoleSlicesSerializeAsArrays 断言四个数组字段序列化成 [] 而不是 null：
// 契约把它们声明为数组，null 会让前端多一条「空清单」与「字段缺失」的分支。
func TestConsoleSlicesSerializeAsArrays(t *testing.T) {
	raw, err := json.Marshal(consoleFor(nil))
	if err != nil {
		t.Fatalf("序列化清单: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("解析清单: %v", err)
	}
	for _, key := range []string{"roles", "capabilities", "navigation", "sections"} {
		if string(fields[key]) != "[]" {
			t.Errorf("%s = %s，期望 []", key, fields[key])
		}
	}
}
