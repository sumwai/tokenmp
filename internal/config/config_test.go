package config

import (
	"testing"
	"time"
)

// fakeEnv 用 map 构造查找函数，测试覆盖各种环境，不碰真实进程环境。
func fakeEnv(kv map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := kv[key]
		return v, ok
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name             string
		env              map[string]string
		wantErr          bool
		wantMaxOpen      int
		wantMaxIdle      int
		wantConnLifetime time.Duration
	}{
		{
			name:             "只给 DSN 时数值项留空用默认",
			env:              map[string]string{envDSN: "user:pass@tcp(localhost:3306)/tokenmp"},
			wantMaxOpen:      0,
			wantMaxIdle:      0,
			wantConnLifetime: 0,
		},
		{
			name: "全部显式给出",
			env: map[string]string{
				envDSN:             "dsn",
				envMaxOpenConns:    "30",
				envMaxIdleConns:    "8",
				envConnMaxLifetime: "90s",
			},
			wantMaxOpen:      30,
			wantMaxIdle:      8,
			wantConnLifetime: 90 * time.Second,
		},
		{
			name:    "缺少 DSN 报错",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name:    "DSN 为空白报错",
			env:     map[string]string{envDSN: "  "},
			wantErr: true,
		},
		{
			name:    "连接数不是整数报错",
			env:     map[string]string{envDSN: "dsn", envMaxOpenConns: "many"},
			wantErr: true,
		},
		{
			name:    "时长格式非法报错",
			env:     map[string]string{envDSN: "dsn", envConnMaxLifetime: "5 minutes"},
			wantErr: true,
		},
		{
			name:    "负数连接数报错",
			env:     map[string]string{envDSN: "dsn", envMaxOpenConns: "-3"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := load(fakeEnv(tt.env))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际通过，结果为 %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if got.MaxOpenConns != tt.wantMaxOpen {
				t.Errorf("MaxOpenConns = %d，期望 %d", got.MaxOpenConns, tt.wantMaxOpen)
			}
			if got.MaxIdleConns != tt.wantMaxIdle {
				t.Errorf("MaxIdleConns = %d，期望 %d", got.MaxIdleConns, tt.wantMaxIdle)
			}
			if got.ConnMaxLifetime != tt.wantConnLifetime {
				t.Errorf("ConnMaxLifetime = %s，期望 %s", got.ConnMaxLifetime, tt.wantConnLifetime)
			}
		})
	}
}
