-- 幂等下单：account_purchase 增加幂等键与唯一索引。
--
-- 下单是「记一笔购买事实 + 发一份派生账本」的两步写入，重复提交（双击、超时重试）
-- 必须只产生一笔订单。幂等键由客户端生成、随请求提交，服务端以
-- (account_id, idempotency_key) 唯一索引兜底：第二次提交命中唯一键，直接返回首次创建
-- 的订单，而不是再发一份存量。
--
-- 键必须带账户维度：幂等键由客户端自选，不带账户会让两个账号用同一个键互相撞车。
--
-- 列可空：迁移前的历史订单没有幂等键，留 NULL。MySQL 的唯一索引允许多个 NULL，
-- 历史行因此不会互相冲突，也不需要回填。
--
-- 与 0003 同一取舍：MySQL 的 ALTER TABLE / CREATE INDEX 没有 IF NOT EXISTS，
-- 先查 information_schema 再决定执行还是空操作，经预处理语句执行，使本迁移可重跑
-- （DDL 已生效但版本登记失败时，重跑不会因列或索引已存在而中断）。预处理语句是会话级
-- 的，迁移因此必须在同一连接上逐条执行，见 migrate.go 的 applyMigration。
SET @add_idempotency_key = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE account_purchase ADD COLUMN idempotency_key VARCHAR(64) NULL COMMENT ''客户端生成的幂等键；(account_id, idempotency_key) 唯一，重复提交返回首次订单''',
    'SET @idempotency_key_noop = 1'
  )
  FROM information_schema.COLUMNS
  WHERE table_schema = DATABASE()
    AND table_name = 'account_purchase'
    AND column_name = 'idempotency_key'
);
PREPARE add_idempotency_key_stmt FROM @add_idempotency_key;
EXECUTE add_idempotency_key_stmt;
DEALLOCATE PREPARE add_idempotency_key_stmt;
SET @add_purchase_idempotency_index = (
  SELECT IF(
    COUNT(*) = 0,
    'CREATE UNIQUE INDEX uk_purchase_idempotency ON account_purchase (account_id, idempotency_key)',
    'SET @purchase_idempotency_index_noop = 1'
  )
  FROM information_schema.STATISTICS
  WHERE table_schema = DATABASE()
    AND table_name = 'account_purchase'
    AND index_name = 'uk_purchase_idempotency'
);
PREPARE add_purchase_idempotency_index_stmt FROM @add_purchase_idempotency_index;
EXECUTE add_purchase_idempotency_index_stmt;
DEALLOCATE PREPARE add_purchase_idempotency_index_stmt;
