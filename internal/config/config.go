// Package config 从环境变量读取各子系统的运行配置。
//
// 刻意不引入配置文件框架：当前需要的字段很少，配置文件会带来格式、加载顺序
// 与热更新等一整套约定，而环境变量是容器化部署下的通用载体。
// 配置读取集中在此，业务包只接收构造好的结构体。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
)

// 环境变量名。集中声明，避免字符串散落在读取逻辑里。
const (
	envDSN             = "TOKENMP_MYSQL_DSN"
	envMaxOpenConns    = "TOKENMP_MYSQL_MAX_OPEN_CONNS"
	envMaxIdleConns    = "TOKENMP_MYSQL_MAX_IDLE_CONNS"
	envConnMaxLifetime = "TOKENMP_MYSQL_CONN_MAX_LIFETIME"
)

// Load 从进程环境读出存储层配置。
func Load() (store.Config, error) {
	return load(os.LookupEnv)
}

// load 接受一个查找函数而不是直接读 os，便于测试构造各种环境。
//
// 缺失的数值项用 store 的默认值（留零即可），只有 DSN 缺失与数值非法返回错误：
// 缺失与非法是两回事 —— 前者有合理默认，后者说明配置写错了，静默兜底会掩盖错误。
func load(lookup func(string) (string, bool)) (store.Config, error) {
	cfg := store.Config{}

	dsn, ok := lookup(envDSN)
	if !ok || strings.TrimSpace(dsn) == "" {
		return store.Config{}, fmt.Errorf("配置缺失：环境变量 %s 未设置", envDSN)
	}
	cfg.DSN = dsn

	maxOpen, err := lookupInt(lookup, envMaxOpenConns)
	if err != nil {
		return store.Config{}, err
	}
	cfg.MaxOpenConns = maxOpen

	maxIdle, err := lookupInt(lookup, envMaxIdleConns)
	if err != nil {
		return store.Config{}, err
	}
	cfg.MaxIdleConns = maxIdle

	lifetime, err := lookupDuration(lookup, envConnMaxLifetime)
	if err != nil {
		return store.Config{}, err
	}
	cfg.ConnMaxLifetime = lifetime

	// 复用 store 的校验，保证环境变量入口与直接构造走同一套规则。
	if err := cfg.Validate(); err != nil {
		return store.Config{}, fmt.Errorf("配置非法：%w", err)
	}
	return cfg, nil
}

// lookupInt 读取一个整数字段，缺失或为空返回零值（由上层填默认）。
func lookupInt(lookup func(string) (string, bool), key string) (int, error) {
	raw, ok := lookup(key)
	if !ok || raw == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("配置非法：环境变量 %s=%q 不是整数", key, raw)
	}
	return v, nil
}

// lookupDuration 读取一个时长字段，格式遵循 time.ParseDuration（如 5m、30s）。
func lookupDuration(lookup func(string) (string, bool), key string) (time.Duration, error) {
	raw, ok := lookup(key)
	if !ok || raw == "" {
		return 0, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("配置非法：环境变量 %s=%q 不是合法时长", key, raw)
	}
	return v, nil
}
