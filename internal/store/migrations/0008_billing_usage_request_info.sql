-- 用量流水补充请求级信息：请求模型、客户端协议与是否跨协议。
--
-- 保持向后兼容：既有流水历史行均为 NULL 或 0。
-- 预处理语句在同一连接上执行，保证可重跑。

SET @add_requested_model = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE billing_usage ADD COLUMN requested_model VARCHAR(128) NULL COMMENT ''客户端请求的模型名；NULL 表示历史行''',
    'SET @requested_model_noop = 1'
  )
  FROM information_schema.COLUMNS
  WHERE table_schema = DATABASE()
    AND table_name = 'billing_usage'
    AND column_name = 'requested_model'
);
PREPARE add_requested_model_stmt FROM @add_requested_model;
EXECUTE add_requested_model_stmt;
DEALLOCATE PREPARE add_requested_model_stmt;

SET @add_protocol = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE billing_usage ADD COLUMN protocol VARCHAR(32) NULL COMMENT ''客户端使用的线协议；NULL 表示历史行''',
    'SET @protocol_noop = 1'
  )
  FROM information_schema.COLUMNS
  WHERE table_schema = DATABASE()
    AND table_name = 'billing_usage'
    AND column_name = 'protocol'
);
PREPARE add_protocol_stmt FROM @add_protocol;
EXECUTE add_protocol_stmt;
DEALLOCATE PREPARE add_protocol_stmt;

SET @add_cross_protocol = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE billing_usage ADD COLUMN cross_protocol TINYINT NOT NULL DEFAULT 0 COMMENT ''是否跨协议转发''',
    'SET @cross_protocol_noop = 1'
  )
  FROM information_schema.COLUMNS
  WHERE table_schema = DATABASE()
    AND table_name = 'billing_usage'
    AND column_name = 'cross_protocol'
);
PREPARE add_cross_protocol_stmt FROM @add_cross_protocol;
EXECUTE add_cross_protocol_stmt;
DEALLOCATE PREPARE add_cross_protocol_stmt;
