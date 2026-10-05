package store

import (
	"testing"
	"time"
)

func TestConfigResolve(t *testing.T) {
	tests := []struct {
		name         string
		cfg          Config
		wantErr      bool
		wantOpen     int
		wantIdle     int
		wantLifetime time.Duration
	}{
		{
			name:         "零值用默认连接池参数",
			cfg:          Config{DSN: "user:pass@tcp(localhost:3306)/tokenmp"},
			wantOpen:     DefaultMaxOpenConns,
			wantIdle:     DefaultMaxIdleConns,
			wantLifetime: DefaultConnMaxLifetime,
		},
		{
			name:         "显式参数原样生效",
			cfg:          Config{DSN: "dsn", MaxOpenConns: 10, MaxIdleConns: 3, ConnMaxLifetime: time.Minute},
			wantOpen:     10,
			wantIdle:     3,
			wantLifetime: time.Minute,
		},
		{
			name:    "DSN 为空报错",
			cfg:     Config{},
			wantErr: true,
		},
		{
			name:    "DSN 仅空白报错",
			cfg:     Config{DSN: "   "},
			wantErr: true,
		},
		{
			name:    "MaxOpenConns 为负报错",
			cfg:     Config{DSN: "dsn", MaxOpenConns: -1},
			wantErr: true,
		},
		{
			name:    "MaxIdleConns 为负报错",
			cfg:     Config{DSN: "dsn", MaxIdleConns: -1},
			wantErr: true,
		},
		{
			name:    "ConnMaxLifetime 为负报错",
			cfg:     Config{DSN: "dsn", ConnMaxLifetime: -time.Second},
			wantErr: true,
		},
		{
			name:         "空闲数超过总数时收敛到总数",
			cfg:          Config{DSN: "dsn", MaxOpenConns: 4, MaxIdleConns: 16},
			wantOpen:     4,
			wantIdle:     4,
			wantLifetime: DefaultConnMaxLifetime,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.cfg.resolve()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际通过，结果为 %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if got.MaxOpenConns != tt.wantOpen {
				t.Errorf("MaxOpenConns = %d，期望 %d", got.MaxOpenConns, tt.wantOpen)
			}
			if got.MaxIdleConns != tt.wantIdle {
				t.Errorf("MaxIdleConns = %d，期望 %d", got.MaxIdleConns, tt.wantIdle)
			}
			if got.ConnMaxLifetime != tt.wantLifetime {
				t.Errorf("ConnMaxLifetime = %s，期望 %s", got.ConnMaxLifetime, tt.wantLifetime)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	if err := (Config{DSN: "dsn"}).Validate(); err != nil {
		t.Errorf("合法配置不应报错，实际：%v", err)
	}
	if err := (Config{}).Validate(); err == nil {
		t.Error("空 DSN 应当报错")
	}
}
