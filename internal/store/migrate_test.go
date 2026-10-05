package store

import (
	"reflect"
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
