-- 上游套餐与配额：商家 + cred_group 维度的套餐，以及套餐上的窗口型限额行。
--
-- 为什么按 cred_group 而不是按渠道：同一厂商的多条端点渠道共享一份凭据与一份额度，
-- 配额是凭据的属性而不是端点的属性；采集按 cred_group 去重，路由消费也按它判定。
--
-- 窗口语义与 account_quota 完全同口径（rolling×5h、calendar×{day,week,month}、total），
-- 判定复用 internal/quota 的纯函数，本表不写第二套窗口计算。上游的窗口起点由上游决定，
-- 这里只承载「最近一次采集到的窗口已用量」，采到什么就是什么。
--
-- last_snapshot 存最近一次采集的原始响应，仅供排障；last_checked_at 是快照新鲜度的
-- 唯一依据：采集失败时保留旧值，路由据此把过期快照视为未知并放行。
--
-- 全库不建外键：一致性由应用层事务保证，只建索引。

CREATE TABLE IF NOT EXISTS upstream_plan (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  merchant_id     BIGINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '套餐归属商家。平台自营也是一行数据。',
  cred_group      VARCHAR(64)     NOT NULL COMMENT '凭据分组，商家内唯一。同厂商多端点共享一份套餐额度。',
  name            VARCHAR(128)    NOT NULL COMMENT '套餐名，仅供展示',
  multiplier      DECIMAL(10,4)   NOT NULL DEFAULT 1 COMMENT '套餐倍率，结算链上与渠道、账户倍率相乘',
  valid_from      DATETIME        NULL COMMENT '有效期起点（含）；NULL 表示不限',
  valid_to        DATETIME        NULL COMMENT '有效期终点（不含）；NULL 表示不限',
  last_snapshot   JSON            NULL COMMENT '最近一次探针采集的原始响应，仅供排障',
  last_checked_at DATETIME        NULL COMMENT '最近一次采集成功的时刻；NULL 表示从未采集，快照视为未知',
  created_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_upstream_plan (merchant_id, cred_group)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='上游套餐：商家 + 凭据分组维度的额度容器';

CREATE TABLE IF NOT EXISTS upstream_plan_quota (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  plan_id         BIGINT UNSIGNED NOT NULL,
  metric          VARCHAR(32)     NOT NULL COMMENT '计量指标：取值集见 internal/billing 的 Metric，与 account_quota.metric 同源',
  window_kind     VARCHAR(16)     NOT NULL COMMENT 'rolling（滚动）| calendar（自然周期）。语义与 account_quota 同口径。',
  period          VARCHAR(16)     NOT NULL COMMENT '5h | day | week | month | total。与 window_kind 拆两列，避免组合串枚举。',
  limit_amount    DECIMAL(24,8)   NOT NULL,
  last_used       DECIMAL(24,8)   NOT NULL DEFAULT 0 COMMENT '最近一次采集到的窗口已用量；采集失败时不更新',
  last_checked_at DATETIME        NULL COMMENT '该行最近一次采集成功的时刻',
  created_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_plan_quota (plan_id, metric, window_kind, period),
  KEY idx_plan_quota_plan (plan_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='套餐限额行：窗口语义复用 account_quota 的判定口径';
