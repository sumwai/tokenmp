package admin

import (
	"context"
	"testing"
)

// TestSetCredentialEnabledBothWays 守护凭据启停的两个方向都能走通。
//
// 停用是运营动作，必须能原地恢复：只有 disable 没有 enable 时，误停的凭据只能靠改库
// 或重新写入一份，后者会让凭据 id 与历史流水脱钩。
func TestSetCredentialEnabledBothWays(t *testing.T) {
	cases := []struct {
		name string
		call func(*Service) error
		want bool
	}{
		{
			name: "停用",
			call: func(s *Service) error { return s.DisableCredential(context.Background(), 7) },
			want: false,
		},
		{
			name: "启用",
			call: func(s *Service) error { return s.EnableCredential(context.Background(), 7) },
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			var (
				gotID      uint64
				gotEnabled bool
				called     bool
			)
			f.setCredentialEnabled = func(_ context.Context, id uint64, enabled bool) error {
				called, gotID, gotEnabled = true, id, enabled
				return nil
			}
			if err := tc.call(newService(f)); err != nil {
				t.Fatalf("不应报错：%v", err)
			}
			if !called {
				t.Fatal("未触达存储层")
			}
			if gotID != 7 || gotEnabled != tc.want {
				t.Fatalf("落到位的是 (id=%d, enabled=%v)，期望 (7, %v)", gotID, gotEnabled, tc.want)
			}
		})
	}
}

// TestEnableCredentialRejectsZeroID 守护缺主键在服务层被拦下，不触达存储层。
func TestEnableCredentialRejectsZeroID(t *testing.T) {
	f := &fakeStore{}
	if err := newService(f).EnableCredential(context.Background(), 0); err == nil {
		t.Fatal("凭据 id 为 0 应当被拒绝")
	}
	if f.called("SetCredentialEnabled") {
		t.Error("校验失败不应触达存储层")
	}
}
