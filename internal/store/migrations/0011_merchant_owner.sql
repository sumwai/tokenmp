-- 商家补充归属的登录主体：商家域（页面 /api/v1/partner/*）的作用域来源。
--
-- 商家域只接受会话推导出的商家，而 merchant 与 web_user 之间原本没有任何关联，
-- 故在此建一条显式绑定。NULL 表示平台自营或尚未绑定。
-- 预处理语句在同一连接上执行，保证可重跑。

SET @add_owner_user_id = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE merchant ADD COLUMN owner_user_id BIGINT UNSIGNED NULL COMMENT ''归属的登录主体；NULL 表示平台自营或尚未绑定''',
    'SET @owner_user_id_noop = 1'
  )
  FROM information_schema.COLUMNS
  WHERE table_schema = DATABASE()
    AND table_name = 'merchant'
    AND column_name = 'owner_user_id'
);
PREPARE add_owner_user_id_stmt FROM @add_owner_user_id;
EXECUTE add_owner_user_id_stmt;
DEALLOCATE PREPARE add_owner_user_id_stmt;

SET @add_uk_owner_user_id = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE merchant ADD UNIQUE KEY uk_merchant_owner (owner_user_id)',
    'SET @uk_owner_user_id_noop = 1'
  )
  FROM information_schema.STATISTICS
  WHERE table_schema = DATABASE()
    AND table_name = 'merchant'
    AND index_name = 'uk_merchant_owner'
);
PREPARE add_uk_owner_user_id_stmt FROM @add_uk_owner_user_id;
EXECUTE add_uk_owner_user_id_stmt;
DEALLOCATE PREPARE add_uk_owner_user_id_stmt;
