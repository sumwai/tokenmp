//go:build integration

// 真实 MySQL 的迁移验证。单独用 integration 构建标签圈起来，
// 因为 make check 必须只依赖 go 与 golangci-lint，不能要求一个可用的数据库。
package store_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/sumwai/tokenmp/internal/store"
)

// envTestDSN 指向一个可丢弃的库：本测试会先删掉已知表再重建。
const envTestDSN = "TOKENMP_TEST_MYSQL_DSN"

// knownTables 与 migrations/0001_init.sql 对应，删除顺序无关紧要（全库无外键）。
var knownTables = []string{
	"billing_usage",
	"account_bucket",
	"account_api_key",
	"account",
	"upstream_model_map",
	"upstream_credential",
	"upstream_channel",
	"merchant",
	"schema_migrations",
}

func TestMigrateCreatesSchemaAndIsIdempotent(t *testing.T) {
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过真实 MySQL 迁移验证", envTestDSN)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := store.Open(ctx, store.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("关闭连接失败：%v", err)
		}
	})

	dropKnownTables(ctx, t, s.DB())
	// 清理用独立的 context：函数内的 ctx 带超时且被 defer 取消，
	// t.Cleanup 在函数返回后执行，此时它已经失效。
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cleanupCancel()
		dropKnownTables(cleanupCtx, t, s.DB())
	})

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}

	for _, table := range knownTables {
		if !tableExists(ctx, t, s.DB(), table) {
			t.Errorf("迁移后表 %s 不存在", table)
		}
	}

	assertPlatformMerchant(ctx, t, s.DB())

	// 重复启动的幂等性：第二次迁移不应因表已存在而失败，也不应重复插入种子行。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移失败：%v", err)
	}
	assertMigrationVersionCount(ctx, t, s.DB(), 1)
	assertPlatformMerchantCount(ctx, t, s.DB(), 1)
}

func dropKnownTables(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range knownTables {
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatalf("删除表 %s 失败：%v", table, err)
		}
	}
}

func tableExists(ctx context.Context, t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?",
		table).Scan(&count)
	if err != nil {
		t.Fatalf("查询表 %s 是否存在失败：%v", table, err)
	}
	return count > 0
}

func assertPlatformMerchant(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	var kind string
	if err := db.QueryRowContext(ctx, "SELECT kind FROM merchant WHERE id = 1").Scan(&kind); err != nil {
		t.Fatalf("查询平台自营商家失败：%v", err)
	}
	if kind != "platform" {
		t.Errorf("商家 1 的 kind = %q，期望 platform", kind)
	}
}

func assertPlatformMerchantCount(ctx context.Context, t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM merchant WHERE code = 'platform'").Scan(&count); err != nil {
		t.Fatalf("统计平台自营商家失败：%v", err)
	}
	if count != want {
		t.Errorf("平台自营商家行数 = %d，期望 %d", count, want)
	}
}

func assertMigrationVersionCount(ctx context.Context, t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("统计迁移版本失败：%v", err)
	}
	if count != want {
		t.Errorf("schema_migrations 行数 = %d，期望 %d", count, want)
	}
}

// TestMigrateRejectsUnknownChannelType 验证写入口的严格性在有真实驱动时同样成立；
// 读方向的宽容在无数据库的单测里覆盖。
func TestValidateChannelTypeIntegration(t *testing.T) {
	if err := store.ValidateChannelType(store.ChannelTypeFromDB("openai_chat")); err != nil {
		t.Errorf("已知协议不应报错：%v", err)
	}
	if err := store.ValidateChannelType(store.ChannelTypeFromDB("not_a_protocol")); err == nil {
		t.Error("未知协议应当被拒绝")
	}
}
