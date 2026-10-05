package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// migrationsFS 内嵌迁移 SQL。
//
// 用 embed 而不是运行时读文件：二进制因此自带 schema 定义，部署时不必
// 额外同步一份 migrations 目录，也不会出现「二进制版本与 SQL 文件版本不一致」。
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationsDir 是内嵌文件系统里的迁移目录。
const migrationsDir = "migrations"

// commentMarkerLen 是 SQL 注释定界符的长度：-- 与 /* 各占 2 个字符，
// 块注释的结束符 */ 同样是 2 个。命名后比裸写 2 更能说明偏移量的来历。
const commentMarkerLen = 2

// createSchemaMigrations 是迁移器自举的版本表。
//
// 它不写在迁移文件里：迁移文件本身需要被记录，记录表必须先存在，
// 否则第一次运行无处登记。该语句幂等，可反复执行。
const createSchemaMigrations = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    BIGINT UNSIGNED NOT NULL,
  name       VARCHAR(255)    NOT NULL,
  applied_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='已执行的迁移版本，重复启动据此跳过'`

// migration 是一个已解析的迁移版本。
type migration struct {
	version    int64
	name       string
	statements []string
}

// Migrate 按版本号顺序执行尚未应用的迁移。
//
// 幂等性由两层保证：schema_migrations 记录已执行版本；本仓库的 DDL 一律
// 使用 CREATE TABLE IF NOT EXISTS，即使上一次执行到一半失败，重跑也不会
// 因表已存在而中断。
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, createSchemaMigrations); err != nil {
		return fmt.Errorf("store: 创建 schema_migrations 失败: %w", err)
	}

	applied, err := s.appliedVersions(ctx)
	if err != nil {
		return err
	}

	migrations, err := loadMigrations(migrationsFS, migrationsDir)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// appliedVersions 读出已执行的版本集合。
func (s *Store) appliedVersions(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("store: 读取已执行迁移失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int64]bool)
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("store: 解析迁移版本行失败: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历迁移版本行失败: %w", err)
	}
	return applied, nil
}

// applyMigration 逐条执行一个版本的语句，全部成功后才登记版本。
//
// 不在事务里执行：MySQL 的 DDL 会隐式提交，事务包不住建表语句，
// 包进去只会给出「原子」的错觉。原子性改由「DDL 幂等 + 最后登记版本」达成。
func (s *Store) applyMigration(ctx context.Context, m migration) error {
	for i, stmt := range m.statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("store: 迁移 %d_%s 第 %d 条语句执行失败: %w", m.version, m.name, i+1, err)
		}
	}
	// ON DUPLICATE KEY UPDATE 而非裸 INSERT：并发实例可能同时通过上面的
	// 已应用检查，各自执行完同样的 DDL 后来登记，唯一键冲突在此跳过，
	// 不影响已经生效的表结构。
	if _, err := s.db.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, name) VALUES (?, ?) ON DUPLICATE KEY UPDATE name = name",
		m.version, m.name); err != nil {
		return fmt.Errorf("store: 登记迁移 %d_%s 失败: %w", m.version, m.name, err)
	}
	return nil
}

// loadMigrations 从文件系统读出全部迁移并按版本号升序排列。
//
// 接受 fs.FS 而不是直接读内嵌变量，便于测试用内存文件系统覆盖异常形态。
func loadMigrations(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("store: 读取迁移目录失败: %w", err)
	}

	var migrations []migration
	seen := make(map[int64]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(entry.Name())
		if err != nil {
			return nil, err
		}
		// 版本号重复意味着执行顺序不确定，必须拒绝而不是任选一条。
		if prev, ok := seen[version]; ok {
			return nil, fmt.Errorf("store: 迁移版本 %d 重复：%s 与 %s", version, prev, entry.Name())
		}
		seen[version] = entry.Name()

		content, err := fs.ReadFile(fsys, dir+"/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("store: 读取迁移文件 %s 失败: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{
			version:    version,
			name:       name,
			statements: splitStatements(string(content)),
		})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	return migrations, nil
}

// parseMigrationName 从文件名解析版本号与名称，形如 0001_init.sql。
//
// 版本号用固定宽度十进制数字前缀：字符串排序会与数值排序不一致
// （"10" 排在 "9" 之前），解析成整数后排序才稳定。
func parseMigrationName(filename string) (int64, string, error) {
	if !strings.HasSuffix(filename, ".sql") {
		return 0, "", fmt.Errorf("store: 迁移文件名 %q 必须以 .sql 结尾", filename)
	}
	base := strings.TrimSuffix(filename, ".sql")
	versionPart, name, found := strings.Cut(base, "_")
	if !found || name == "" {
		return 0, "", fmt.Errorf("store: 迁移文件名 %q 必须形如 <版本>_<名称>.sql", filename)
	}
	version, err := strconv.ParseInt(versionPart, 10, 64)
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf("store: 迁移文件名 %q 的版本号必须是正整数", filename)
	}
	return version, name, nil
}

// splitStatements 把一份迁移文件切成可逐条执行的语句。
//
// 不使用驱动的 multiStatements 选项：打开它等于允许单次查询携带任意多条语句，
// 扩大了注入面。迁移是本进程内部的固定输入，没有为此放宽驱动限制的必要，
// 代价只是需要一个能识别注释与引号的切分器。
//
// 切分按 MySQL 语法实现：-- 与 # 行注释、/* */ 块注释整段丢弃，
// 单引号 / 双引号 / 反引号内的分号不作为分隔符，引号内的转义按各自规则处理。
// 迁移文件由本仓库维护，切分器只需覆盖这些形态，不做通用 SQL 解析。
func splitStatements(script string) []string {
	var (
		statements []string
		current    strings.Builder
		quote      byte // 当前所处引号字符；0 表示不在引号内
	)

	i, n := 0, len(script)
	for i < n {
		c := script[i]

		if quote != 0 {
			current.WriteByte(c)
			switch {
			case c == '\\' && quote != '`' && i+1 < n:
				// 反斜杠转义只在单/双引号内生效；反引号标识符里 \ 是普通字符。
				i++
				current.WriteByte(script[i])
			case c == quote:
				// 连续两个引号是转义写法，写到第二个引号后仍留在引号内。
				if i+1 < n && script[i+1] == quote {
					i++
					current.WriteByte(script[i])
				} else {
					quote = 0
				}
			}
			i++
			continue
		}

		switch {
		case c == '\'' || c == '"' || c == '`':
			quote = c
			current.WriteByte(c)
			i++
		case c == '-' && i+1 < n && script[i+1] == '-':
			i = skipToLineEnd(script, i+commentMarkerLen)
		case c == '#':
			i = skipToLineEnd(script, i+1)
		case c == '/' && i+1 < n && script[i+1] == '*':
			i = skipBlockComment(script, i+commentMarkerLen)
		case c == ';':
			appendStatement(&statements, &current)
			i++
		default:
			current.WriteByte(c)
			i++
		}
	}

	appendStatement(&statements, &current)
	return statements
}

// skipToLineEnd 跳过一行注释，停在换行符之前。
// 换行不消费：它是语句之间的空白，保留能让错误信息里的行号仍对得上原文件。
func skipToLineEnd(script string, i int) int {
	for i < len(script) && script[i] != '\n' {
		i++
	}
	return i
}

// skipBlockComment 跳过 /* */ 块注释，返回注释之后的偏移。
// MySQL 不支持嵌套块注释，因此遇到第一个 */ 即结束；未闭合时跳到末尾。
func skipBlockComment(script string, i int) int {
	for i+1 < len(script) {
		if script[i] == '*' && script[i+1] == '/' {
			return i + commentMarkerLen
		}
		i++
	}
	return len(script)
}

// appendStatement 把当前累积的内容作为一条语句收下，随后清空缓冲。
// 全是空白的片段丢弃，避免把注释或尾随空白当成空语句执行。
func appendStatement(statements *[]string, current *strings.Builder) {
	if stmt := strings.TrimSpace(current.String()); stmt != "" {
		*statements = append(*statements, stmt)
	}
	current.Reset()
}
