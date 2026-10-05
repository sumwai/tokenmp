// Package store 是 MySQL 存储层：连接、迁移与初版 schema 的读写入口。
//
// 这里只做连接与迁移，不承载业务规则；DDL 以版本化 SQL 文件内嵌，
// 见 migrations/ 目录。全库不建外键，一致性由应用层事务保证。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	// 驱动以副作用方式注册 "mysql"，调用方只需传入 DSN。
	_ "github.com/go-sql-driver/mysql"
)

// 连接池默认值。零值语义是「用默认值」而不是 database/sql 的「无上限」：
// 后者在配置漏传时会让进程按请求量无限开连接，把上游打挂，
// 因此这里把「没配」与「配成 0」一并收敛到保守的默认值。
const (
	DefaultMaxOpenConns    = 25
	DefaultMaxIdleConns    = 5
	DefaultConnMaxLifetime = 5 * time.Minute
)

// Config 是打开 MySQL 连接所需的参数。
//
// DSN 与连接池参数都从外部注入，store 不读环境变量也不读配置文件，
// 这样存储层不依赖任何具体的配置来源，测试与嵌入方都能直接构造。
type Config struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// resolve 校验配置并填上默认值。
//
// 校验放在 Open 之前且返回明确错误，而不是让驱动在第一次查询时报出
// 难懂的网络错误：配置问题应当与连接问题区分开。
func (c Config) resolve() (Config, error) {
	if strings.TrimSpace(c.DSN) == "" {
		return Config{}, errors.New("store: DSN 不能为空")
	}
	if c.MaxOpenConns < 0 {
		return Config{}, fmt.Errorf("store: MaxOpenConns 不能为负数，得到 %d", c.MaxOpenConns)
	}
	if c.MaxIdleConns < 0 {
		return Config{}, fmt.Errorf("store: MaxIdleConns 不能为负数，得到 %d", c.MaxIdleConns)
	}
	if c.ConnMaxLifetime < 0 {
		return Config{}, fmt.Errorf("store: ConnMaxLifetime 不能为负数，得到 %s", c.ConnMaxLifetime)
	}

	if c.MaxOpenConns == 0 {
		c.MaxOpenConns = DefaultMaxOpenConns
	}
	if c.MaxIdleConns == 0 {
		c.MaxIdleConns = DefaultMaxIdleConns
	}
	if c.ConnMaxLifetime == 0 {
		c.ConnMaxLifetime = DefaultConnMaxLifetime
	}
	// 空闲连接数超过总连接数时 database/sql 会静默按总连接数截断，
	// 配置写了却未生效比直接报错更难排查，故在此收敛。
	if c.MaxIdleConns > c.MaxOpenConns {
		c.MaxIdleConns = c.MaxOpenConns
	}
	return c, nil
}

// Validate 报告配置是否可用。填默认值不算错误，因此这里只判断输入本身。
func (c Config) Validate() error {
	_, err := c.resolve()
	return err
}

// Store 持有连接池，是访问数据库的唯一入口。
type Store struct {
	db *sql.DB
}

// Open 建立连接池并做一次连通性检查。
//
// 刻意不在这里跑迁移：迁移是会改 schema 的写操作，应该由部署流程显式触发，
// 而不是任何一次进程启动的副作用。Migrate 是独立方法。
func Open(ctx context.Context, cfg Config) (*Store, error) {
	resolved, err := cfg.resolve()
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("mysql", resolved.DSN)
	if err != nil {
		return nil, fmt.Errorf("store: 打开数据库连接失败: %w", err)
	}
	db.SetMaxOpenConns(resolved.MaxOpenConns)
	db.SetMaxIdleConns(resolved.MaxIdleConns)
	db.SetConnMaxLifetime(resolved.ConnMaxLifetime)

	s := &Store{db: db}
	if err := s.Ping(ctx); err != nil {
		// 连通性检查失败时主动关掉连接池，避免调用方拿到半初始化的 Store。
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Ping 检查数据库是否可达。
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("store: 数据库不可达: %w", err)
	}
	return nil
}

// Close 释放连接池。
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("store: 关闭连接池失败: %w", err)
	}
	return nil
}

// DB 暴露底层连接池，供后续数据访问代码复用同一份连接配置。
func (s *Store) DB() *sql.DB {
	return s.db
}
