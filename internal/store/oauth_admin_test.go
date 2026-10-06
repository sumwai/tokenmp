package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// 本文件覆盖订阅型凭据相关的两个存储入口的参数组装与可读错误，不连数据库：
// 续期写回按主键覆盖 secret（且只作用于启用行），登录流程按分组读渠道 config。

// TestUpdateCredentialSecret 断言写回把 secret 与 id 送到 UPDATE，并保留「只改启用行」的过滤。
func TestUpdateCredentialSecret(t *testing.T) {
	fake := &recordedExec{}
	secret := []byte(`{"type":"oauth","access":"a","refresh":"r","expires":1}`)
	if err := updateCredentialSecret(context.Background(), fake, 9, secret); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("执行 %d 次，期望 1", fake.calls)
	}
	if fake.query != updateCredentialSecretSQL {
		t.Errorf("SQL = %q，期望 %q", fake.query, updateCredentialSecretSQL)
	}
	wantArgs := []any{secret, uint64(9)}
	if !reflect.DeepEqual(fake.args, wantArgs) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, wantArgs)
	}
	// 过滤条件写在 SQL 里：并发停用应当胜出，续期不得把停用行改回可用态。
	if !strings.Contains(fake.query, "enabled = 1") {
		t.Errorf("SQL 缺少启用过滤：%s", fake.query)
	}
}

// TestUpdateCredentialSecretRejectsBadInput 断言 id 为 0 或 secret 非合法 JSON 时不触达驱动。
func TestUpdateCredentialSecretRejectsBadInput(t *testing.T) {
	tests := []struct {
		name   string
		id     uint64
		secret []byte
	}{
		{name: "id 为 0", id: 0, secret: []byte(`{"api_key":"k"}`)},
		{name: "secret 为空", id: 1, secret: nil},
		{name: "secret 非法 JSON", id: 1, secret: []byte(`{not json`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &recordedExec{}
			if err := updateCredentialSecret(context.Background(), fake, tt.id, tt.secret); err == nil {
				t.Fatal("非法输入应报错")
			}
			if fake.calls != 0 {
				t.Fatalf("非法输入不应触达驱动，实际执行 %d 次", fake.calls)
			}
		})
	}
}

// TestChannelConfigByCredGroup 断言按商家与分组读出 config 原文。
func TestChannelConfigByCredGroup(t *testing.T) {
	config := []byte(`{"oauth":{"token_url":"https://t","client_id":"c"}}`)
	fake := &recordedQuery{rows: [][]any{{config}}}

	got, err := channelConfigByCredGroup(context.Background(), fake, 7, "group-a")
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if !reflect.DeepEqual(got, config) {
		t.Errorf("config = %s，期望 %s", got, config)
	}
	if fake.query != channelConfigByCredGroupSQL {
		t.Errorf("SQL = %q，期望 %q", fake.query, channelConfigByCredGroupSQL)
	}
	wantArgs := []any{uint64(7), "group-a"}
	if !reflect.DeepEqual(fake.args, wantArgs) {
		t.Errorf("参数 = %#v，期望 %#v", fake.args, wantArgs)
	}
}

// TestChannelConfigByCredGroupErrors 断言非法入参在触达驱动前报错，无匹配行时返回 sql.ErrNoRows。
func TestChannelConfigByCredGroupErrors(t *testing.T) {
	t.Run("商家为 0 不触达驱动", func(t *testing.T) {
		fake := &recordedQuery{}
		if _, err := channelConfigByCredGroup(context.Background(), fake, 0, "group-a"); err == nil {
			t.Fatal("商家为 0 应报错")
		}
		if fake.calls != 0 {
			t.Fatalf("非法输入不应触达驱动，实际 %d 次", fake.calls)
		}
	})
	t.Run("分组为空白不触达驱动", func(t *testing.T) {
		fake := &recordedQuery{}
		if _, err := channelConfigByCredGroup(context.Background(), fake, 1, "  "); err == nil {
			t.Fatal("分组为空白应报错")
		}
		if fake.calls != 0 {
			t.Fatalf("非法输入不应触达驱动，实际 %d 次", fake.calls)
		}
	})
	t.Run("无匹配行返回 sql.ErrNoRows", func(t *testing.T) {
		fake := &recordedQuery{}
		_, err := channelConfigByCredGroup(context.Background(), fake, 1, "group-a")
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("应返回 sql.ErrNoRows，得到 %v", err)
		}
	})
	t.Run("驱动错误被透传", func(t *testing.T) {
		fake := &recordedQuery{err: errExecFailure}
		_, err := channelConfigByCredGroup(context.Background(), fake, 1, "group-a")
		if !errors.Is(err, errExecFailure) {
			t.Fatalf("应可追溯驱动错误，得到 %v", err)
		}
	})
}
