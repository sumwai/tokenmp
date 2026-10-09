package user

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sumwai/tokenmp/internal/store"
	"github.com/sumwai/tokenmp/internal/webapi"
)

// 本文件覆盖控制台清单端点：会话与账户两道判定、按角色的能力集合、条目过滤与
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

// TestConsolePerRole 覆盖三种角色的清单：角色差异只来自服务端的能力集合，
// 条目按能力过滤，因此前端不需要按角色分支。
func TestConsolePerRole(t *testing.T) {
	tests := []struct {
		name       string
		role       string
		wantCaps   []string
		wantNav    []string
		wantSectNo int
	}{
		{
			name:       "调用方",
			role:       store.RoleMember,
			wantCaps:   []string{CapConsole, CapKeys, CapRequests, CapUsage, CapAccount},
			wantNav:    []string{"/", "/keys", "/usage", "/account"},
			wantSectNo: 1,
		},
		{
			name:       "商户",
			role:       store.RolePartner,
			wantCaps:   []string{CapConsole, CapKeys, CapRequests, CapUsage, CapAccount},
			wantNav:    []string{"/", "/keys", "/usage", "/account"},
			wantSectNo: 1,
		},
		{
			// 平台管理员另有管理面能力；管理面页面尚未定义，因此当前只体现在
			// 能力集合上，导航与段落与其余角色一致。
			name:       "平台管理员",
			role:       store.RoleAdmin,
			wantCaps:   []string{CapConsole, CapKeys, CapRequests, CapUsage, CapAccount, CapOps},
			wantNav:    []string{"/", "/keys", "/usage", "/account"},
			wantSectNo: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv()
			e.session.role = tt.role
			status, env := e.do(t, http.MethodGet, ConsolePath, "token", "")
			if status != http.StatusOK || codeOf(t, env) != webapi.CodeOK {
				t.Fatalf("应 200: %d %s", status, env)
			}
			data := consoleDataOf(t, env)
			if data.Role != tt.role {
				t.Errorf("role = %q，期望 %q", data.Role, tt.role)
			}
			if len(data.Capabilities) != len(tt.wantCaps) {
				t.Fatalf("capabilities = %v，期望 %v", data.Capabilities, tt.wantCaps)
			}
			for i, cap := range tt.wantCaps {
				if data.Capabilities[i] != cap {
					t.Errorf("capabilities[%d] = %q，期望 %q", i, data.Capabilities[i], cap)
				}
			}
			if got := pathsOf(data.Navigation); len(got) != len(tt.wantNav) {
				t.Fatalf("navigation = %v，期望 %v", got, tt.wantNav)
			} else {
				for i, path := range tt.wantNav {
					if got[i] != path {
						t.Errorf("navigation[%d] = %q，期望 %q", i, got[i], path)
					}
				}
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

// TestConsoleFiltersByCapability 断言条目按能力过滤：条目所需的能力不在集合里时
// 该条目不出现，段落条目被过滤光时整段不返回。
func TestConsoleFiltersByCapability(t *testing.T) {
	// 缺少请求记录能力：唯一含该能力的段落整体消失，导航不含相关条目。
	caps := []string{CapConsole, CapKeys, CapUsage, CapAccount}
	if nav := filterEntries(consoleNavigation, caps); len(nav) != len(consoleNavigation) {
		t.Fatalf("导航项都不需要 %s，应全部保留，得到 %d 项", CapRequests, len(nav))
	}
	if sections := filterSections(consoleSections, caps); len(sections) != 0 {
		t.Fatalf("缺少 %s 时应没有段落，得到 %d 段", CapRequests, len(sections))
	}

	// 只剩首页能力：导航只剩首页项，段落同样为空。
	if nav := filterEntries(consoleNavigation, []string{CapConsole}); len(nav) != 1 || nav[0].Path != "/" {
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

// TestConsoleUnknownRoleIsEmpty 断言未登记的角色得到空清单：越权在配置出错时
// 不能因为回落到某个角色而静默生效。
func TestConsoleUnknownRoleIsEmpty(t *testing.T) {
	data := consoleFor("nobody")
	if len(data.Capabilities) != 0 || len(data.Navigation) != 0 || len(data.Sections) != 0 {
		t.Fatalf("未登记角色应得到空清单，得到 %s", data)
	}
}

// TestConsoleSlicesSerializeAsArrays 断言三个数组字段序列化成 [] 而不是 null：
// 契约把它们声明为数组，null 会让前端多一条「空清单」与「字段缺失」的分支。
func TestConsoleSlicesSerializeAsArrays(t *testing.T) {
	raw, err := json.Marshal(consoleFor("nobody"))
	if err != nil {
		t.Fatalf("序列化清单: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("解析清单: %v", err)
	}
	for _, key := range []string{"capabilities", "navigation", "sections"} {
		if string(fields[key]) != "[]" {
			t.Errorf("%s = %s，期望 []", key, fields[key])
		}
	}
}
