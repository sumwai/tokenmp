-- 计费 schema：定价、条件倍率、日历、窗口限额、商品与购买、调账。
--
-- 与 0001 同源的三条约定：
--   前缀即归属：billing_ 是两侧共用的计费事实与规则，account_ 是下游用户资产，
--     merchant_ / sys_ 是基础数据。
--   全库不建外键：一致性由应用层事务保证，只建索引。
--   金额一律 DECIMAL：FLOAT/DOUBLE 无法精确表示十进制小数，对账会累积误差。
--
-- 枚举收敛到 internal/billing/enum.go 单一出处：本文件的 COMMENT 与 const 块
-- 同步维护。新增 metric / period / 结算单位等取值只加 const 并在计费代码里
-- 识别，不改表结构、不发迁移。

-- 定价版本。改价 = 新版本行 + 旧版本 retired_at 置位，不 update 旧价格。
-- active 用 retired_at IS NULL 表达，刻意不设 status 字符串：那是可推导的枚举，
-- 多一列就多一处双写不一致。
CREATE TABLE IF NOT EXISTS billing_pricing (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  merchant_id  BIGINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '牌价归属的商家。同模型多商家 = 多行，比价是普通查询。',
  model        VARCHAR(128)    NOT NULL,
  version      INT             NOT NULL COMMENT '版本号，商家与模型内递增',
  effective_at DATETIME        NOT NULL COMMENT '生效时刻；未来版本在生效前不参与查询',
  retired_at   DATETIME        NULL COMMENT 'NULL 即 active；被新版本取代时置位，历史流水不受影响',
  created_at   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_pricing_version (merchant_id, model, version),
  KEY idx_pricing_active (merchant_id, model, retired_at, effective_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='定价版本：改价=新版本行，不 UPDATE 旧价格';

-- 计价分量。同一张表、同一套结算函数，口径只是数据。
-- 刻意没有 currency 列：积分不是货币，结算单位由 unit_settle 表达。
CREATE TABLE IF NOT EXISTS billing_price_component (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  pricing_id  BIGINT UNSIGNED NOT NULL,
  metric      VARCHAR(32)     NOT NULL COMMENT '计量指标：input_token | output_token | cache_read_token | cache_write_5m | cache_write_1h | request。新增 metric = enum.go 加 const + 计费代码识别，不发迁移。',
  unit_settle VARCHAR(16)     NOT NULL COMMENT '折算到哪个结算单位：currency（货币）| token（token 存量）| credit（赠送积分）。',
  unit_price  DECIMAL(24,8)   NOT NULL COMMENT '按 basis_qty 折算的单价 / 折算率',
  basis_qty   DECIMAL(24,8)   NOT NULL DEFAULT 1 COMMENT '单价的基准数量，如 1000000 表示每 1M；结算量 = usage[metric] × unit_price / basis_qty',
  tier_from   DECIMAL(24,8)   NULL COMMENT '阶梯区间下界（含）；NULL 表示无下界',
  tier_to     DECIMAL(24,8)   NULL COMMENT '阶梯区间上界（不含）；NULL 表示无上界',
  tier_basis  VARCHAR(32)     NULL COMMENT '阶梯依据的聚合口径，如 request_input（单请求输入量）| period_total（周期累计量）；由结算代码解析，不加枚举约束',
  created_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_component_pricing (pricing_id, metric)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='计价分量：metric -> 结算单位的折算率';

-- 条件倍率。峰谷、周末、节假日、调休、活动统一为带适用条件的规则行，
-- 组合再多也不加列：匹配算法只有一个函数，代码量不随规则数增长。
CREATE TABLE IF NOT EXISTS billing_price_rule (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  scope         VARCHAR(16)     NOT NULL COMMENT 'pricing | model_map | plan | account。scope_id 指向对应实体主键，刻意不建外键。',
  scope_id      BIGINT UNSIGNED NOT NULL,
  metric        VARCHAR(32)     NULL COMMENT '限定指标；NULL = 全部指标',
  multiplier    DECIMAL(10,4)   NOT NULL,
  valid_from    DATETIME        NULL COMMENT '绝对区间起点（含）；NULL 表示不限',
  valid_to      DATETIME        NULL COMMENT '绝对区间终点（不含）；NULL 表示不限',
  time_from     TIME            NULL COMMENT '一日内时段起点（含）；NULL 表示不限。与 time_to 一起表达峰谷。',
  time_to       TIME            NULL COMMENT '一日内时段终点（不含）；NULL 表示不限',
  weekday_mask  TINYINT UNSIGNED NULL COMMENT '星期位掩码：bit0=周一 ... bit6=周日；NULL 表示不限',
  day_kind_mask TINYINT UNSIGNED NULL COMMENT '日期性质位掩码，位位置见 enum.go 的 DayKindBit*：1=workday,2=weekend,4=holiday,8=makeup_workday；NULL 表示不限',
  calendar      VARCHAR(32)     NULL COMMENT 'sys_calendar.calendar；day_kind_mask 非空时的日历来源',
  priority      INT             NOT NULL DEFAULT 100 COMMENT '匹配时优先级，值大者胜出',
  created_at    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_rule_scope (scope, scope_id, metric, priority)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='条件倍率规则：时段 / 星期 / 日历 / 有效期';

-- 日历。放假通知 = 插数据行，零代码变更零发版。
CREATE TABLE IF NOT EXISTS sys_calendar (
  id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  calendar   VARCHAR(32)     NOT NULL COMMENT '日历名，如 cn；同一天在不同日历下可以有不同性质',
  `date`     DATE            NOT NULL,
  day_kind   VARCHAR(16)     NOT NULL COMMENT 'workday | weekend | holiday | makeup_workday。新增性质 = enum.go 加 const + 日历读取代码识别，不发迁移。',
  created_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_calendar_date (calendar, `date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='日历：工作日 / 周末 / 法定假 / 调休补班';

-- 窗口型限额定义。限额是上限，与 bucket 的存量语义不同。
-- 刻意没有 reset_policy 列：window_kind 已决定重置语义（rolling 无重置时刻，
-- calendar 到期重置）；重置事实记在 account_quota_event。
CREATE TABLE IF NOT EXISTS account_quota (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  scope        VARCHAR(16)     NOT NULL COMMENT 'account | api_key | channel | plan。scope_id 指向对应实体主键，刻意不建外键。',
  scope_id     BIGINT UNSIGNED NOT NULL,
  metric       VARCHAR(32)     NOT NULL COMMENT '计量的指标：额度 / token / 请求次数都走 metric 表达',
  window_kind  VARCHAR(16)     NOT NULL COMMENT 'rolling（滚动，按流水时间戳聚合）| calendar（自然周期，到期重置）',
  period       VARCHAR(16)     NOT NULL COMMENT '5h | day | week | month | total。与 window_kind 拆两列，避免组合串枚举。',
  limit_amount DECIMAL(24,8)   NOT NULL,
  action       VARCHAR(16)     NOT NULL COMMENT 'reject | throttle',
  created_at   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_quota (scope, scope_id, metric, window_kind, period),
  KEY idx_quota_scope (scope, scope_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='窗口型限额定义：5 小时滚动 / 日 / 周 / 月';

-- 限额重置事件。事实只追加，聚合口径 created_at > baseline_at，
-- 完整审计链，滚动窗口天然兼容。
CREATE TABLE IF NOT EXISTS account_quota_event (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  quota_id    BIGINT UNSIGNED NOT NULL,
  `event`     VARCHAR(16)     NOT NULL COMMENT 'reset（窗口重置）。新增事件 = enum.go 加 const + 限额聚合代码识别，不发迁移。',
  baseline_at DATETIME        NOT NULL COMMENT '重置基准时刻；用量聚合范围是 created_at > baseline_at',
  reason      VARCHAR(255)    NOT NULL DEFAULT '',
  operator    VARCHAR(64)     NOT NULL COMMENT '审计必填：触发重置的操作者',
  created_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_quota_event (quota_id, baseline_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='窗口型限额事件：重置基准，append-only';

-- 调账。误扣补偿走这里，金额账与次数账分开互不抵消。
CREATE TABLE IF NOT EXISTS billing_adjustment (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  account_id   BIGINT UNSIGNED NOT NULL,
  delta_amount DECIMAL(24,8)   NOT NULL COMMENT '正数补扣、负数退费；单位由关联的账本口径决定',
  reason       VARCHAR(255)    NOT NULL,
  operator     VARCHAR(64)     NOT NULL COMMENT '审计必填：发起调账的操作者',
  created_at   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_adjustment_account_time (account_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='调账流水：补扣 / 退费 / 人工干预';

-- 商品档位。档位是数据行，上架新品不发版。
CREATE TABLE IF NOT EXISTS merchant_product (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  merchant_id   BIGINT UNSIGNED NOT NULL DEFAULT 1,
  name          VARCHAR(128)    NOT NULL,
  unit          VARCHAR(16)     NOT NULL COMMENT 'currency | token | credit，取值集与 billing_price_component.unit_settle 相同',
  qty           DECIMAL(24,8)   NOT NULL COMMENT '每份包含的数量，如 100M token',
  price         DECIMAL(24,8)   NOT NULL COMMENT '售价',
  model_scope   JSON            NULL COMMENT '可用模型范围；NULL = 不限',
  validity_days INT             NOT NULL DEFAULT 0 COMMENT '有效天数；0 表示不过期',
  created_at    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_product (merchant_id, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='商品档位：10 元 100M token 包';

-- 购买记录。购买派生 account_bucket 行，本表是事实来源。
CREATE TABLE IF NOT EXISTS account_purchase (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  account_id   BIGINT UNSIGNED NOT NULL,
  merchant_id  BIGINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '向哪个商家购买，决定派生 bucket 的归属',
  product_id   BIGINT UNSIGNED NOT NULL,
  qty          DECIMAL(24,8)   NOT NULL COMMENT '购买份数',
  price_paid   DECIMAL(24,8)   NOT NULL COMMENT '实付金额',
  purchased_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_purchase_account_time (account_id, purchased_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='购买记录：账户向商家买了什么';
