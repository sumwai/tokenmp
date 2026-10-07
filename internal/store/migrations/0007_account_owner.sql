-- 账户归属：注册即开户后，账户记录拥有它的登录主体。
--
-- 一对一：唯一键表达「一个登录主体至多一个账户」，第二个账户无法落库。
-- 可空：管理命令创建的账户没有登录主体，NULL 表示无主账户；MySQL 的唯一键
-- 允许多个 NULL，因此无主账户可以有任意多个。
--
-- 只做加法：既有账户的 owner_user_id 为 NULL，行为与迁移前一致。
--
-- MySQL 没有 ADD COLUMN / ADD KEY IF NOT EXISTS，先查 information_schema 再决定
-- 执行 DDL 还是空操作，经预处理语句执行，使本迁移可重跑；理由与写法同 0003/0004。
--
-- 预处理语句是会话级的，因此迁移必须在同一个连接上逐条执行，见 migrate.go 的
-- applyMigration：连接池会把不同语句分发到不同连接，会话状态随之丢失。
SET @add_owner_user_id = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE account ADD COLUMN owner_user_id BIGINT UNSIGNED NULL COMMENT ''拥有该账户的登录主体；NULL 表示管理命令创建的无主账户''',
    'SET @owner_user_id_noop = 1'
  )
  FROM information_schema.COLUMNS
  WHERE table_schema = DATABASE()
    AND table_name = 'account'
    AND column_name = 'owner_user_id'
);
PREPARE add_owner_user_id_stmt FROM @add_owner_user_id;
EXECUTE add_owner_user_id_stmt;
DEALLOCATE PREPARE add_owner_user_id_stmt;

SET @add_owner_unique = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE account ADD UNIQUE KEY uk_account_owner (owner_user_id)',
    'SET @owner_unique_noop = 1'
  )
  FROM information_schema.STATISTICS
  WHERE table_schema = DATABASE()
    AND table_name = 'account'
    AND index_name = 'uk_account_owner'
);
PREPARE add_owner_unique_stmt FROM @add_owner_unique;
EXECUTE add_owner_unique_stmt;
DEALLOCATE PREPARE add_owner_unique_stmt;

-- 角色取值新增 partner，列注释同步；注释已含 partner 时本段为空操作，保证可重跑。
SET @update_role_comment = (
  SELECT IF(
    COUNT(*) = 1,
    'ALTER TABLE web_user MODIFY COLUMN role VARCHAR(32) NOT NULL DEFAULT ''member'' COMMENT ''admin | partner | member；取值由服务端下发''',
    'SET @role_comment_noop = 1'
  )
  FROM information_schema.COLUMNS
  WHERE table_schema = DATABASE()
    AND table_name = 'web_user'
    AND column_name = 'role'
    AND COLUMN_COMMENT NOT LIKE '%partner%'
);
PREPARE update_role_comment_stmt FROM @update_role_comment;
EXECUTE update_role_comment_stmt;
DEALLOCATE PREPARE update_role_comment_stmt;
