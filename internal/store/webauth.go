package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
)

// 本文件是 0006 迁移引入的页面账号表的读写入口。
//
// 与管理面、计费面的分工一致：查询一律参数化，不做分页与投影裁剪之外的解释；
// 页面会话的令牌只以 SHA-256 十六进制出现，明文令牌在认证层生成后即丢弃。
//
// 行类型用 Web 前缀：account 是模型调用的计费主体，web_user 是登录主体，
// 两者的 id 不互通，名字上必须分开。

// ErrConflict 是唯一键冲突的可判定哨兵。
//
// 调用方用 errors.Is 判定冲突（映射 409），不再解析驱动错误文本或错误码 ——
// 后者会把 MySQL 方言漏进业务层。
var ErrConflict = errors.New("store: 唯一键冲突")

// webUserColumns 是读路径共享的列清单，拼接处与 SELECT 一一对应。
const webUserColumns = `id, email, username, password_hash, role, status, created_at`

// webSessionJoin 是会话与账号的联合查询前缀：会话端点一次拿到两侧字段，
// 省掉按 user_id 的二次查询。
const webSessionJoin = `
SELECT s.id, s.user_id, s.access_hash, s.refresh_hash, s.access_expires_at,
       s.refresh_expires_at, s.revoked_at,
       u.id, u.email, u.username, u.password_hash, u.role, u.status, u.created_at
FROM web_session s
JOIN web_user u ON u.id = s.user_id`

// WebUser 是 web_user 的一行。
type WebUser struct {
	ID           uint64
	Email        string
	Username     string
	PasswordHash string // 空串表示仅第三方登录创建的账号
	Role         string
	Status       string
	CreatedAt    time.Time
}

// WebSession 是 web_session 的一行；明文令牌从不出现。
type WebSession struct {
	ID               uint64
	UserID           uint64
	AccessHash       string
	RefreshHash      string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	RevokedAt        *time.Time
}

// WebSessionWithUser 是会话与归属账号的联合读取结果。
type WebSessionWithUser struct {
	Session WebSession
	User    WebUser
}

// conflictFromWrite 把唯一键冲突翻译成可判定错误，其余错误原样带上下文返回。
func conflictFromWrite(table string, err error) error {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateEntry {
		return fmt.Errorf("%w: 写入 %s 失败", ErrConflict, table)
	}
	return fmt.Errorf("store: 写入 %s 失败: %w", table, err)
}

// WebUserByLogin 按邮箱或用户名查账号；无匹配时错误满足 errors.Is(err, sql.ErrNoRows)。
//
// 登录标识允许两种取值是产品口径：注册要邮箱，登录不要求用户记住用的是哪种。
// 两种标识各有唯一键，因此本查询最多命中一行。
func (s *Store) WebUserByLogin(ctx context.Context, login string) (*WebUser, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+webUserColumns+` FROM web_user WHERE email = ? OR username = ? LIMIT 1`,
		login, login)
	return scanWebUser(row)
}

// WebUserByEmail 按邮箱查账号；用途：注册查重、重置密码定位账号。
func (s *Store) WebUserByEmail(ctx context.Context, email string) (*WebUser, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+webUserColumns+` FROM web_user WHERE email = ? LIMIT 1`, email)
	return scanWebUser(row)
}

// WebUserByID 按主键查账号。
func (s *Store) WebUserByID(ctx context.Context, id uint64) (*WebUser, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+webUserColumns+` FROM web_user WHERE id = ?`, id)
	return scanWebUser(row)
}

// scanWebUser 归一单行扫描；空密码哈希列（NULL）折算为空串。
func scanWebUser(row *sql.Row) (*WebUser, error) {
	var (
		u            WebUser
		passwordHash sql.NullString
	)
	err := row.Scan(&u.ID, &u.Email, &u.Username, &passwordHash, &u.Role, &u.Status, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读取 web_user 失败: %w", err)
	}
	u.PasswordHash = passwordHash.String
	return &u, nil
}

// WebInsertUser 写入新账号；邮箱或用户名已存在时返回满足 errors.Is(err, ErrConflict) 的错误。
func (s *Store) WebInsertUser(ctx context.Context, u WebUser) (uint64, error) {
	var passwordHash any
	if u.PasswordHash != "" {
		passwordHash = u.PasswordHash
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO web_user (email, username, password_hash, role, status) VALUES (?, ?, ?, ?, ?)`,
		u.Email, u.Username, passwordHash, u.Role, u.Status)
	if err != nil {
		return 0, conflictFromWrite("web_user", err)
	}
	id, err := lastInsertID(res)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// WebInsertSession 写入新会话行。
func (s *Store) WebInsertSession(ctx context.Context, sess WebSession) (uint64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO web_session
		   (user_id, access_hash, refresh_hash, access_expires_at, refresh_expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		sess.UserID, sess.AccessHash, sess.RefreshHash, sess.AccessExpiresAt, sess.RefreshExpiresAt)
	if err != nil {
		return 0, conflictFromWrite("web_session", err)
	}
	id, err := lastInsertID(res)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// WebSessionByAccess 按访问令牌哈希读会话与账号；无匹配时返回 sql.ErrNoRows。
func (s *Store) WebSessionByAccess(ctx context.Context, hash string) (*WebSessionWithUser, error) {
	return scanWebSessionWithUser(s.db.QueryRowContext(ctx, webSessionJoin+` WHERE s.access_hash = ?`, hash))
}

// WebSessionByRefresh 按当前刷新令牌哈希读会话与账号。
func (s *Store) WebSessionByRefresh(ctx context.Context, hash string) (*WebSessionWithUser, error) {
	return scanWebSessionWithUser(s.db.QueryRowContext(ctx, webSessionJoin+` WHERE s.refresh_hash = ?`, hash))
}

// WebSessionByPrevRefresh 按上一枚刷新令牌哈希读会话与账号。
//
// 命中即令牌重放：轮换后的旧值只应存在于已失窃的副本里，调用方据此撤销整个会话。
func (s *Store) WebSessionByPrevRefresh(ctx context.Context, hash string) (*WebSessionWithUser, error) {
	return scanWebSessionWithUser(s.db.QueryRowContext(ctx, webSessionJoin+` WHERE s.prev_refresh_hash = ?`, hash))
}

// scanWebSessionWithUser 归一会话联合查询的单行扫描。
func scanWebSessionWithUser(row *sql.Row) (*WebSessionWithUser, error) {
	var (
		out            WebSessionWithUser
		refreshExpired time.Time
		revokedAt      sql.NullTime
		passwordHash   sql.NullString
	)
	err := row.Scan(
		&out.Session.ID, &out.Session.UserID, &out.Session.AccessHash, &out.Session.RefreshHash,
		&out.Session.AccessExpiresAt, &refreshExpired, &revokedAt,
		&out.User.ID, &out.User.Email, &out.User.Username, &passwordHash,
		&out.User.Role, &out.User.Status, &out.User.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读取 web_session 失败: %w", err)
	}
	out.Session.RefreshExpiresAt = refreshExpired
	if revokedAt.Valid {
		t := revokedAt.Time
		out.Session.RevokedAt = &t
	}
	out.User.PasswordHash = passwordHash.String
	return &out, nil
}

// WebRotateSession 以 CAS 方式轮换会话令牌：只有当前刷新令牌仍是 expectRefresh 时才写入。
//
// 返回 false 表示期望值已不匹配（并发轮换或令牌已换），调用方必须视为失败而不是重试覆盖 ——
// 覆盖会把上一次轮换的合法会话顶掉。旧刷新令牌同时落入 prev 列，供重放检测。
func (s *Store) WebRotateSession(ctx context.Context, id uint64, expectRefresh, accessHash, refreshHash string,
	accessExpiresAt, refreshExpiresAt time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE web_session
		    SET prev_refresh_hash = refresh_hash,
		        refresh_hash = ?,
		        access_hash = ?,
		        access_expires_at = ?,
		        refresh_expires_at = ?
		  WHERE id = ? AND refresh_hash = ? AND revoked_at IS NULL`,
		refreshHash, accessHash, accessExpiresAt, refreshExpiresAt, id, expectRefresh)
	if err != nil {
		return false, fmt.Errorf("store: 轮换 web_session 失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: 读取轮换影响行数失败: %w", err)
	}
	return affected > 0, nil
}

// WebRevokeSession 撤销单个会话（登出）；幂等，已撤销的会话重复调用不报错。
func (s *Store) WebRevokeSession(ctx context.Context, id uint64) error {
	// 两条撤销各写字面量 SQL：条件不进字符串拼接，取值仍走参数占位符。
	if _, err := s.db.ExecContext(ctx,
		`UPDATE web_session SET revoked_at = NOW() WHERE id = ? AND revoked_at IS NULL`, id); err != nil {
		return fmt.Errorf("store: 撤销 web_session 失败: %w", err)
	}
	return nil
}

// WebRevokeUserSessions 撤销账号的全部会话（改密、注销）；幂等。
func (s *Store) WebRevokeUserSessions(ctx context.Context, userID uint64) error {
	// 时间取库侧 NOW()：撤销时刻以数据库为准，避免应用与数据库时钟漂移让边界判定失真。
	if _, err := s.db.ExecContext(ctx,
		`UPDATE web_session SET revoked_at = NOW() WHERE user_id = ? AND revoked_at IS NULL`, userID); err != nil {
		return fmt.Errorf("store: 撤销 web_session 失败: %w", err)
	}
	return nil
}

// WebIdentityProviders 返回账号已绑定的第三方登录方式，按字典序去重。
func (s *Store) WebIdentityProviders(ctx context.Context, userID uint64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT provider FROM web_identity WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: 读取 web_identity 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var providers []string
	for rows.Next() {
		var provider string
		if err := rows.Scan(&provider); err != nil {
			return nil, fmt.Errorf("store: 扫描 web_identity 失败: %w", err)
		}
		providers = append(providers, provider)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 web_identity 失败: %w", err)
	}
	// 空切片而不是 nil：响应里 identities 序列化为 [] 而非 null，前端免判空。
	if providers == nil {
		return []string{}, nil
	}
	return providers, nil
}

// WebInsertOTP 写入一枚待用验证码。
func (s *Store) WebInsertOTP(ctx context.Context, email, purpose, codeHash string, expiresAt time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO web_otp (email, purpose, code_hash, expires_at) VALUES (?, ?, ?, ?)`,
		email, purpose, codeHash, expiresAt); err != nil {
		return fmt.Errorf("store: 写入 web_otp 失败: %w", err)
	}
	return nil
}

// WebConsumeOTP 校验并核销一枚验证码；命中且未过期未用时置 used_at 并返回 true。
//
// 校验与核销在同一条 UPDATE 里完成：条件含 used_at IS NULL，
// 并发下两笔相同请求只有一笔能影响行，天然防重放。
func (s *Store) WebConsumeOTP(ctx context.Context, email, purpose, codeHash string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE web_otp
		    SET used_at = ?
		  WHERE email = ? AND purpose = ? AND code_hash = ?
		    AND used_at IS NULL AND expires_at > ?`,
		now, email, purpose, codeHash, now)
	if err != nil {
		return false, fmt.Errorf("store: 核销 web_otp 失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: 读取核销影响行数失败: %w", err)
	}
	return affected > 0, nil
}

// WebUpdatePassword 设置新密码哈希；OAuth-only 账号首次设密码也走这里。
func (s *Store) WebUpdatePassword(ctx context.Context, userID uint64, passwordHash string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE web_user SET password_hash = ? WHERE id = ?`, passwordHash, userID); err != nil {
		return fmt.Errorf("store: 更新 web_user 密码失败: %w", err)
	}
	return nil
}

// WebRevokeOtherSessions 撤销账号除指定会话外的全部会话（改密后保留当前登录）。
func (s *Store) WebRevokeOtherSessions(ctx context.Context, userID, keepSessionID uint64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE web_session SET revoked_at = NOW()
		 WHERE user_id = ? AND id <> ? AND revoked_at IS NULL`, userID, keepSessionID); err != nil {
		return fmt.Errorf("store: 撤销其余 web_session 失败: %w", err)
	}
	return nil
}

// WebEraseUser 注销账号：置 erased 并删除第三方绑定，密码哈希抹除。
//
// 邮箱与用户名保留不释放：两者继续被唯一键占用，避免注销后的标识被他人
// 重新注册后继承历史流水归属；真要释放属于数据治理动作，不在本路径做。
func (s *Store) WebEraseUser(ctx context.Context, userID uint64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE web_user SET status = 'erased', password_hash = NULL WHERE id = ?`, userID); err != nil {
		return fmt.Errorf("store: 注销 web_user 失败: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM web_identity WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("store: 清除 web_identity 失败: %w", err)
	}
	return nil
}

// WebIdentity 是 web_identity 的一行。
type WebIdentity struct {
	UserID   uint64
	Provider string
	Subject  string
	Email    string
}

// WebIdentityByProvider 按提供方与主体标识查绑定；无匹配时返回 sql.ErrNoRows。
func (s *Store) WebIdentityByProvider(ctx context.Context, provider, subject string) (*WebIdentity, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT user_id, provider, subject, email FROM web_identity
		  WHERE provider = ? AND subject = ?`, provider, subject)
	var id WebIdentity
	if err := row.Scan(&id.UserID, &id.Provider, &id.Subject, &id.Email); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 读取 web_identity 失败: %w", err)
	}
	return &id, nil
}

// WebInsertIdentity 写入绑定；同一提供方与主体重复绑定回 ErrConflict。
func (s *Store) WebInsertIdentity(ctx context.Context, id WebIdentity) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO web_identity (user_id, provider, subject, email) VALUES (?, ?, ?, ?)`,
		id.UserID, id.Provider, id.Subject, id.Email); err != nil {
		return conflictFromWrite("web_identity", err)
	}
	return nil
}

// lastInsertID 取自增主键；负值守卫后才转换，与 billing.insertID 同一口径。
func lastInsertID(res sql.Result) (uint64, error) {
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: 读取自增主键失败: %w", err)
	}
	if id < 0 {
		return 0, fmt.Errorf("store: 返回负的自增 id %d", id)
	}
	return uint64(id), nil
}
