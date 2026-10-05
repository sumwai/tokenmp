-- 用量流水的 API key 维度：限额判定从账户粒度扩展到单把接入 key。
--
-- 只加列不改存量：既有流水没有 key 归属，DEFAULT 0 表示「无 key 维度」，
-- 不计入任何 api_key 限额。0 不是自增主键的有效取值，因此无需回填历史行，
-- 也不会与真实 key 冲突。
--
-- 列为 NOT NULL DEFAULT 0 而不是可空：聚合按 api_key_id = ? 精确匹配，
-- NULL 参与比较恒为 NULL，会把该行漏掉；默认值让写入方不必关心这一列。
--
-- 索引 (api_key_id, created_at) 与既有的 (account_id, created_at) 同形：
-- api_key 限额的聚合按 key 过滤、按时间取窗口下界。
--
-- MySQL 没有 ADD COLUMN IF NOT EXISTS，直接 ALTER 只差一步就破坏 0001/0002
-- 建立的「DDL 幂等」约定：DDL 已生效但版本登记失败时，重跑会因列已存在而中断。
-- 这里先查 information_schema 再决定执行 ALTER 还是空操作，经预处理语句执行，
-- 使本迁移可重跑；加索引同理。
--
-- 预处理语句是会话级的，因此迁移必须在同一个连接上逐条执行，见 migrate.go 的
-- applyMigration：连接池会把不同语句分发到不同连接，会话状态随之丢失。
SET @add_api_key_id = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE billing_usage ADD COLUMN api_key_id BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT ''调用方 API key 主键；0 表示无 key 维度的历史或内部行，不计入 api_key 限额''',
    'SET @api_key_id_noop = 1'
  )
  FROM information_schema.COLUMNS
  WHERE table_schema = DATABASE()
    AND table_name = 'billing_usage'
    AND column_name = 'api_key_id'
);
PREPARE add_api_key_id_stmt FROM @add_api_key_id;
EXECUTE add_api_key_id_stmt;
DEALLOCATE PREPARE add_api_key_id_stmt;

SET @add_api_key_time_index = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE billing_usage ADD KEY idx_usage_api_key_time (api_key_id, created_at)',
    'SET @api_key_time_index_noop = 1'
  )
  FROM information_schema.STATISTICS
  WHERE table_schema = DATABASE()
    AND table_name = 'billing_usage'
    AND index_name = 'idx_usage_api_key_time'
);
PREPARE add_api_key_time_index_stmt FROM @add_api_key_time_index;
EXECUTE add_api_key_time_index_stmt;
DEALLOCATE PREPARE add_api_key_time_index_stmt;
