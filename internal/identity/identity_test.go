package identity

import (
	"slices"
	"testing"

	"github.com/sumwai/tokenmp/internal/store"
)

// 本文件覆盖身份的叠加关系与能力并集：库里只存一个最高身份，页面要看到它包含的
// 全部身份，以及去重后的能力集合。

// TestRolesExpandStackedIdentities 断言最高身份被展开成它包含的全部身份，基线在前。
func TestRolesExpandStackedIdentities(t *testing.T) {
	tests := []struct {
		role string
		want []string
	}{
		{role: store.RoleMember, want: []string{store.RoleMember}},
		{role: store.RolePartner, want: []string{store.RoleMember, store.RolePartner}},
		{role: store.RoleAdmin, want: []string{store.RoleMember, store.RolePartner, store.RoleAdmin}},
	}
	for _, tt := range tests {
		if got := Roles(tt.role); !slices.Equal(got, tt.want) {
			t.Errorf("Roles(%q) = %v，期望 %v", tt.role, got, tt.want)
		}
	}
}

// TestRolesUnknownIsEmpty 断言未登记的身份得到空集合而不是基线身份：
// 越权不能因为回落到基线而静默生效。
func TestRolesUnknownIsEmpty(t *testing.T) {
	got := Roles("nobody")
	if got == nil || len(got) != 0 {
		t.Fatalf("未知身份应得到非 nil 的空切片，得到 %v", got)
	}
}

// TestCapabilitiesAreUnionAcrossIdentities 断言多身份的能力集合是各身份的并集且不重复：
// admin 同时是 partner 与 member，逐个身份拼接会把数据面能力重复三次。
func TestCapabilitiesAreUnionAcrossIdentities(t *testing.T) {
	roles := Roles(store.RoleAdmin)
	want := []string{CapConsole, CapKeys, CapRequests, CapUsage, CapAccount, CapPurchase, CapOps}
	if got := Capabilities(roles); !slices.Equal(got, want) {
		t.Fatalf("admin 的能力 = %v，期望 %v", got, want)
	}

	// 顺序不同的同一集合得到同一结果，且不随身份数量增长。
	if got := Capabilities([]string{store.RoleAdmin, store.RoleMember, store.RolePartner}); !slices.Equal(got, want) {
		t.Fatalf("重复身份的能力 = %v，期望 %v", got, want)
	}
}

// TestCapabilitiesBaseIdentity 断言基线的能力集合：数据面能力齐全，管理面能力缺席。
func TestCapabilitiesBaseIdentity(t *testing.T) {
	want := []string{CapConsole, CapKeys, CapRequests, CapUsage, CapAccount, CapPurchase}
	if got := Capabilities([]string{store.RoleMember}); !slices.Equal(got, want) {
		t.Fatalf("member 的能力 = %v，期望 %v", got, want)
	}
	if slices.Contains(Capabilities([]string{store.RoleMember}), CapOps) {
		t.Fatalf("基线身份不应有管理面能力")
	}
}

// TestCapabilitiesUnknownIsEmpty 断言未登记身份与空集合都得到非 nil 的空切片：
// 契约把能力声明为数组，null 会让前端多一条「空集合」与「字段缺失」的分支。
func TestCapabilitiesUnknownIsEmpty(t *testing.T) {
	for _, roles := range [][]string{nil, {"nobody"}} {
		got := Capabilities(roles)
		if got == nil || len(got) != 0 {
			t.Fatalf("Capabilities(%v) = %v，期望非 nil 的空切片", roles, got)
		}
	}
}

// TestHeldDropsUnknownAndDuplicates 断言清单回显的身份只含已登记的取值且不重复。
func TestHeldDropsUnknownAndDuplicates(t *testing.T) {
	held := Held([]string{store.RoleAdmin, "nobody", store.RoleAdmin, store.RoleMember})
	if !slices.Equal(held, []string{store.RoleAdmin, store.RoleMember}) {
		t.Fatalf("Held = %v，期望 {%s %s}", held, store.RoleAdmin, store.RoleMember)
	}
	if got := Held(nil); got == nil || len(got) != 0 {
		t.Fatalf("Held(nil) = %v，期望非 nil 的空切片", got)
	}
}
