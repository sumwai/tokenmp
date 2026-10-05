-- 账本折算率：非货币包在购买时刻锁定的单位货币价值，用于包扣尽后的跨单位转换。
--
-- 只加列、不改存量：既有包没有购买定价可依据，折算率留 NULL，扣减时退化为记欠额。
-- 列可空且不设默认值：NULL 与 0 在本语义下同义（无折算率），显式 NULL 比默认 0
-- 更能表达「这一行不存在折算率」。
--
-- MySQL 的 ALTER TABLE 没有 ADD COLUMN IF NOT EXISTS，直接 ALTER 只差一步就
-- 破坏 0001/0002 建立的「DDL 幂等」约定：DDL 已生效但版本登记失败时，重跑会因
-- 列已存在而中断。这里先查 information_schema 再决定执行 ALTER 还是空操作，
-- 经预处理语句执行，使本迁移可重跑。
--
-- 预处理语句是会话级的，因此迁移必须在同一个连接上逐条执行，见 migrate.go 的
-- applyMigration：连接池会把不同语句分发到不同连接，会话状态随之丢失。
SET @add_unit_rate = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE account_bucket ADD COLUMN unit_rate DECIMAL(24,8) NULL COMMENT ''购买锁定的单位货币价值 = price / qty；非购买来源为 NULL''',
    'SET @unit_rate_noop = 1'
  )
  FROM information_schema.COLUMNS
  WHERE table_schema = DATABASE()
    AND table_name = 'account_bucket'
    AND column_name = 'unit_rate'
);
PREPARE add_unit_rate_stmt FROM @add_unit_rate;
EXECUTE add_unit_rate_stmt;
DEALLOCATE PREPARE add_unit_rate_stmt;
