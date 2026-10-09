package store

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSplitStatements(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   []string
	}{
		{
			name:   "单条语句去掉末尾分号",
			script: "SELECT 1;",
			want:   []string{"SELECT 1"},
		},
		{
			name:   "多条语句按分号切开",
			script: "CREATE TABLE a (id INT);\nCREATE TABLE b (id INT);",
			want:   []string{"CREATE TABLE a (id INT)", "CREATE TABLE b (id INT)"},
		},
		{
			name:   "行注释整段丢弃",
			script: "-- 建表\nCREATE TABLE a (id INT); -- 尾注释\n",
			want:   []string{"CREATE TABLE a (id INT)"},
		},
		{
			name:   "井号行注释同样丢弃",
			script: "# 注释\nSELECT 1;",
			want:   []string{"SELECT 1"},
		},
		{
			name:   "块注释含分号不切分",
			script: "/* 注释; 还是注释 */\nSELECT 1;",
			want:   []string{"SELECT 1"},
		},
		{
			name:   "单引号内的分号不是分隔符",
			script: "INSERT INTO t VALUES ('a;b');",
			want:   []string{"INSERT INTO t VALUES ('a;b')"},
		},
		{
			name:   "反引号内的分号不是分隔符",
			script: "CREATE TABLE t (`col;semi` INT);",
			want:   []string{"CREATE TABLE t (`col;semi` INT)"},
		},
		{
			name:   "连续单引号是转义不结束字符串",
			script: "INSERT INTO t VALUES ('it''s; ok');",
			want:   []string{"INSERT INTO t VALUES ('it''s; ok')"},
		},
		{
			name:   "反斜杠转义不结束字符串",
			script: `INSERT INTO t VALUES ('a\';b');`,
			want:   []string{`INSERT INTO t VALUES ('a\';b')`},
		},
		{
			name:   "末尾无分号的语句也收下",
			script: "SELECT 1",
			want:   []string{"SELECT 1"},
		},
		{
			name:   "只有注释时没有语句",
			script: "-- 空迁移\n/* 什么都没有 */\n",
			want:   nil,
		},
		{
			name:   "连续分号不产生空语句",
			script: "SELECT 1;;SELECT 2;",
			want:   []string{"SELECT 1", "SELECT 2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitStatements(tt.script)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitStatements() = %#v，期望 %#v", got, tt.want)
			}
		})
	}
}

// TestSplitStatementsKeepsActualMigrationWhole 用真实迁移文件兜底：
// 解析结果不能为空，且不得把语句切碎到只剩片段。
func TestSplitStatementsKeepsActualMigrationWhole(t *testing.T) {
	raw, err := migrationsFS.ReadFile(migrationsDir + "/0001_init.sql")
	if err != nil {
		t.Fatalf("读取内嵌迁移失败：%v", err)
	}
	statements := splitStatements(string(raw))
	if len(statements) == 0 {
		t.Fatal("真实迁移解析出 0 条语句")
	}
	for _, stmt := range statements {
		if strings.HasPrefix(stmt, "--") {
			t.Errorf("注释混进了待执行语句：%q", stmt)
		}
	}
	// 初始迁移包含 8 张业务表、1 张版本表之外的种子插入，语句数至少应是表数加一。
	if len(statements) < 9 {
		t.Errorf("真实迁移只解析出 %d 条语句，疑似被切碎", len(statements))
	}
}

// TestBillingMigrationContent 锁定 0002 的形状：新表建全、DDL 里不出现
// 被本次枚举减法删掉的列，且沿用 IF NOT EXISTS 与无外键的约定。
//
// 断言前先用 splitStatements 去掉注释：注释里会出现「刻意没有 status 列」
// 这类说明文字，直接扫描原文会把说明当成列定义。
func TestBillingMigrationContent(t *testing.T) {
	raw, err := migrationsFS.ReadFile(migrationsDir + "/0002_billing.sql")
	if err != nil {
		t.Fatalf("读取内嵌迁移失败：%v", err)
	}
	statements := splitStatements(string(raw))
	script := strings.Join(statements, "\n")

	billingTables := []string{
		"billing_pricing",
		"billing_price_component",
		"billing_price_rule",
		"sys_calendar",
		"account_quota",
		"account_quota_event",
		"billing_adjustment",
		"merchant_product",
		"account_purchase",
	}
	for _, table := range billingTables {
		if !strings.Contains(script, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Errorf("0002 缺少 CREATE TABLE IF NOT EXISTS %s", table)
		}
	}

	// 全库不建外键：一致性由应用层事务保证。
	if strings.Contains(strings.ToUpper(script), "FOREIGN KEY") {
		t.Error("0002 不应建外键")
	}

	// 枚举减法：这些列不再以列定义的形态出现。列名后面跟类型，故用
	// 行首 + 名字 + 空白的模式；被删除列的说明文字可能留在 COMMENT 字符串里，
	// 那种出现不算列定义。
	forbiddenColumns := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s*status\s`),
		regexp.MustCompile(`(?m)^\s*reset_policy\s`),
		regexp.MustCompile(`(?m)^\s*currency\s`),
	}
	for _, re := range forbiddenColumns {
		if loc := re.FindString(script); loc != "" {
			t.Errorf("0002 不应出现列定义 %q", strings.TrimSpace(loc))
		}
	}

	// 定价版本用 retired_at IS NULL 表达 active。
	if !strings.Contains(script, "retired_at") {
		t.Error("billing_pricing 缺少 retired_at")
	}
	// 窗口类型拆成两列，不是组合串。
	if !strings.Contains(script, "window_kind") || !strings.Contains(script, "period") {
		t.Error("account_quota 缺少 window_kind / period 拆分列")
	}
	// 计价分量不带 currency 列，结算单位由 unit_settle 表达。
	if !strings.Contains(script, "unit_settle") {
		t.Error("billing_price_component 缺少 unit_settle")
	}
}

// TestUnitRateMigrationShape 锁定 0003 的形状：只加一列、可空、自动检测列是否
// 已存在。MySQL 没有 ADD COLUMN IF NOT EXISTS，若改成裸 ALTER，在「DDL 已生效、
// 版本登记失败」的重跑场景会因列已存在而中断，这里用断言阻止那次简化。
func TestUnitRateMigrationShape(t *testing.T) {
	raw, err := migrationsFS.ReadFile(migrationsDir + "/0003_account_bucket_unit_rate.sql")
	if err != nil {
		t.Fatalf("读取内嵌迁移失败：%v", err)
	}
	statements := splitStatements(string(raw))
	script := strings.Join(statements, "\n")

	if len(statements) != 4 {
		t.Errorf("0003 应拆成 4 条语句（SET / PREPARE / EXECUTE / DEALLOCATE），得到 %d：%#v", len(statements), statements)
	}
	if !strings.Contains(script, "ALTER TABLE account_bucket ADD COLUMN unit_rate DECIMAL(24,8) NULL") {
		t.Error("0003 应加可空的 DECIMAL(24,8) unit_rate 列")
	}
	for _, want := range []string{"information_schema.COLUMNS", "PREPARE", "EXECUTE", "DEALLOCATE PREPARE"} {
		if !strings.Contains(script, want) {
			t.Errorf("0003 缺少可重跑所需的 %q", want)
		}
	}
	// 只加列不改存量：不得出现 UPDATE / INSERT，也不建新表。
	for _, forbidden := range []string{"UPDATE account_bucket", "INSERT INTO", "CREATE TABLE"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("0003 不应包含 %q", forbidden)
		}
	}
}

// TestAPIKeyMigrationShape 锁定 0004 的形状：给 billing_usage 只加一列与一个索引，
// 列带 NOT NULL DEFAULT 0，且加列 / 加索引都先查 information_schema 再执行。
// 与 0003 同理，若改成裸 ALTER，重跑会因对象已存在而中断。
func TestAPIKeyMigrationShape(t *testing.T) {
	raw, err := migrationsFS.ReadFile(migrationsDir + "/0004_billing_usage_api_key.sql")
	if err != nil {
		t.Fatalf("读取内嵌迁移失败：%v", err)
	}
	statements := splitStatements(string(raw))
	script := strings.Join(statements, "\n")

	if len(statements) != 8 {
		t.Errorf("0004 应拆成 8 条语句（列与索引各一组 SET / PREPARE / EXECUTE / DEALLOCATE），得到 %d：%#v",
			len(statements), statements)
	}
	for _, want := range []string{
		"ALTER TABLE billing_usage ADD COLUMN api_key_id BIGINT UNSIGNED NOT NULL DEFAULT 0",
		"ADD KEY idx_usage_api_key_time (api_key_id, created_at)",
		"information_schema.COLUMNS",
		"information_schema.STATISTICS",
		"PREPARE",
		"EXECUTE",
		"DEALLOCATE PREPARE",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("0004 缺少可重跑所需的 %q", want)
		}
	}
	// 只加列与索引、不改存量：不得出现 UPDATE / INSERT，也不建新表。
	for _, forbidden := range []string{"UPDATE billing_usage", "INSERT INTO", "CREATE TABLE"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("0004 不应包含 %q", forbidden)
		}
	}
}

func TestBillingUsageRequestInfoMigrationShape(t *testing.T) {
	raw, err := migrationsFS.ReadFile(migrationsDir + "/0008_billing_usage_request_info.sql")
	if err != nil {
		t.Fatalf("读取内嵌迁移失败：%v", err)
	}
	statements := splitStatements(string(raw))
	script := strings.Join(statements, "\n")

	if len(statements) != 12 {
		t.Errorf("0008 应拆成 12 条语句（3 列各一组 SET / PREPARE / EXECUTE / DEALLOCATE），得到 %d：%#v",
			len(statements), statements)
	}
	for _, want := range []string{
		"ALTER TABLE billing_usage ADD COLUMN requested_model",
		"ALTER TABLE billing_usage ADD COLUMN protocol",
		"ALTER TABLE billing_usage ADD COLUMN cross_protocol",
		"information_schema.COLUMNS",
		"PREPARE",
		"EXECUTE",
		"DEALLOCATE PREPARE",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("0008 缺少可重跑所需的 %q", want)
		}
	}
}

// TestMerchantOwnerMigrationShape 断言 0011 给 merchant 加归属列与唯一索引：
// 商家域（/api/v1/partner/*）的作用域由该列推导，两段 DDL 都要可重跑。
func TestMerchantOwnerMigrationShape(t *testing.T) {
	raw, err := migrationsFS.ReadFile(migrationsDir + "/0011_merchant_owner.sql")
	if err != nil {
		t.Fatalf("读取内嵌迁移失败：%v", err)
	}
	statements := splitStatements(string(raw))
	script := strings.Join(statements, "\n")

	if len(statements) != 8 {
		t.Errorf("0011 应拆成 8 条语句（列与索引各一组 SET / PREPARE / EXECUTE / DEALLOCATE），得到 %d：%#v",
			len(statements), statements)
	}
	for _, want := range []string{
		"ALTER TABLE merchant ADD COLUMN owner_user_id",
		"ALTER TABLE merchant ADD UNIQUE KEY uk_merchant_owner",
		"information_schema.COLUMNS",
		"information_schema.STATISTICS",
		"PREPARE",
		"EXECUTE",
		"DEALLOCATE PREPARE",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("0011 缺少可重跑所需的 %q", want)
		}
	}
}

// TestRequestLogMigrationShape 断言 0009 建立三张表与各自的关键索引：
// 请求记录主表按请求标识唯一，尝试表按 (请求, 序号) 唯一，计数表按四维组合唯一。
func TestRequestLogMigrationShape(t *testing.T) {
	raw, err := migrationsFS.ReadFile(migrationsDir + "/0009_request_log.sql")
	if err != nil {
		t.Fatalf("读取内嵌迁移失败：%v", err)
	}
	statements := splitStatements(string(raw))
	if len(statements) != 3 {
		t.Errorf("0009 应拆成 3 条语句（请求记录 / 尝试 / 计数缓存各一条建表），得到 %d：%#v",
			len(statements), statements)
	}
	script := strings.Join(statements, "\n")
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS request_log",
		"CREATE TABLE IF NOT EXISTS request_attempt",
		"CREATE TABLE IF NOT EXISTS request_stats_daily",
		"UNIQUE KEY uk_request_log_request (request_id)",
		"UNIQUE KEY uk_request_attempt (request_id, attempt)",
		"UNIQUE KEY uk_request_stats_daily (account_id, `day`, model, api_key_id, status)",
		"payload_available",
		"request_shape",
		"error_response_shape",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("0009 缺少 %q", want)
		}
	}
}

// TestPurchaseIdempotencyMigrationShape 锁定 0010 的形状：给 account_purchase 加可空的
// 幂等键列与 (account_id, idempotency_key) 唯一索引，两处都先查 information_schema
// 再执行。与 0003 同理，若改成裸 ALTER / 裸 CREATE INDEX，重跑会因对象已存在而中断。
func TestPurchaseIdempotencyMigrationShape(t *testing.T) {
	raw, err := migrationsFS.ReadFile(migrationsDir + "/0010_account_purchase_idempotency.sql")
	if err != nil {
		t.Fatalf("读取内嵌迁移失败：%v", err)
	}
	statements := splitStatements(string(raw))
	script := strings.Join(statements, "\n")

	if len(statements) != 8 {
		t.Errorf("0011 应拆成 8 条语句（列与索引各一组 SET / PREPARE / EXECUTE / DEALLOCATE），得到 %d：%#v",
			len(statements), statements)
	}
	for _, want := range []string{
		"ALTER TABLE account_purchase ADD COLUMN idempotency_key VARCHAR(64) NULL",
		"CREATE UNIQUE INDEX uk_purchase_idempotency ON account_purchase (account_id, idempotency_key)",
		"information_schema.COLUMNS",
		"information_schema.STATISTICS",
		"PREPARE",
		"EXECUTE",
		"DEALLOCATE PREPARE",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("0011 缺少可重跑所需的 %q", want)
		}
	}
	// 只加列与索引、不回填存量：不得出现 UPDATE / INSERT，也不建新表。
	for _, forbidden := range []string{"UPDATE account_purchase", "INSERT INTO", "CREATE TABLE"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("0010 不应包含 %q", forbidden)
		}
	}
}

func TestParseMigrationName(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		wantVersion int64
		wantName    string
		wantErr     bool
	}{
		{name: "标准形态", filename: "0001_init.sql", wantVersion: 1, wantName: "init"},
		{name: "多位版本号", filename: "0012_add_pricing.sql", wantVersion: 12, wantName: "add_pricing"},
		{name: "名称含下划线", filename: "0003_add_model_map.sql", wantVersion: 3, wantName: "add_model_map"},
		{name: "缺少版本分隔符", filename: "init.sql", wantErr: true},
		{name: "版本号为零", filename: "0000_init.sql", wantErr: true},
		{name: "版本号非数字", filename: "abc_init.sql", wantErr: true},
		{name: "名称为空", filename: "0001_.sql", wantErr: true},
		{name: "非 sql 后缀", filename: "0001_init.txt", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, gotName, err := parseMigrationName(tt.filename)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际得到 version=%d name=%q", version, gotName)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if version != tt.wantVersion || gotName != tt.wantName {
				t.Errorf("解析结果 = (%d, %q)，期望 (%d, %q)", version, gotName, tt.wantVersion, tt.wantName)
			}
		})
	}
}

func TestLoadMigrationsSortsAndParses(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/0002_second.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
		"migrations/0001_first.sql":  &fstest.MapFile{Data: []byte("SELECT 1;")},
		"migrations/README.md":       &fstest.MapFile{Data: []byte("说明，不是迁移")},
	}

	got, err := loadMigrations(fsys, "migrations")
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应加载 2 个迁移，实际 %d 个", len(got))
	}
	if got[0].version != 1 || got[0].name != "first" {
		t.Errorf("第一个迁移 = (%d, %q)，期望 (1, \"first\")", got[0].version, got[0].name)
	}
	if got[1].version != 2 || got[1].name != "second" {
		t.Errorf("第二个迁移 = (%d, %q)，期望 (2, \"second\")", got[1].version, got[1].name)
	}
	if len(got[0].statements) != 1 || got[0].statements[0] != "SELECT 1" {
		t.Errorf("第一个迁移语句 = %#v，期望 [\"SELECT 1\"]", got[0].statements)
	}
}

func TestLoadMigrationsRejectsDuplicateVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/0001_a.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		"migrations/0001_b.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
	}
	if _, err := loadMigrations(fsys, "migrations"); err == nil {
		t.Error("重复版本号应当报错，避免执行顺序不确定")
	}
}
