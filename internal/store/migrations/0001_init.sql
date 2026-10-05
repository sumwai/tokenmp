-- 初版 schema：商家、上游渠道与凭据、账户与统一账本、用量流水。
--
-- 全库不建外键：一致性由应用层事务保证。外键在批量导入、在线 DDL 与
-- 后续可能的分库分表上都是阻碍，而本系统的写入都经过 store 单一入口。
-- 只建索引。
--
-- 金额一律 DECIMAL：FLOAT/DOUBLE 是二进制浮点，无法精确表示十进制小数，
-- 在对账场景会累积误差，故不作为金额类型出现。
--
-- 命名前缀即归属：upstream_ 是上游供方资产，account_ 是下游用户资产，
-- billing_ 是两侧共用的计费事实，merchant 是商家基础数据。

CREATE TABLE IF NOT EXISTS merchant (
  id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  code       VARCHAR(64)     NOT NULL,
  name       VARCHAR(128)    NOT NULL,
  kind       VARCHAR(16)     NOT NULL COMMENT 'platform=平台自营 | partner=入驻商家。平台自营也是一行数据，不是特例。',
  status     VARCHAR(16)     NOT NULL DEFAULT 'active' COMMENT 'active | disabled',
  settle_info JSON           NULL COMMENT '分账口径占位：平台抽成、结算周期。',
  created_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_merchant_code (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='商家：上游供方与平台自营都是一等实体';

-- 第一阶段只有一个平台自营商家。id 固定为 1，供 merchant_id 默认值引用。
INSERT INTO merchant (id, code, name, kind, status)
VALUES (1, 'platform', '平台自营', 'platform', 'active')
ON DUPLICATE KEY UPDATE id = id;

CREATE TABLE IF NOT EXISTS upstream_channel (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  merchant_id   BIGINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '渠道归属的商家',
  name          VARCHAR(128)    NOT NULL DEFAULT '' COMMENT '渠道名，商家内用于区分同一协议的多条渠道',
  vendor        VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '上游厂商标签，仅供展示与对账分组，不参与路由',
  type          VARCHAR(32)     NOT NULL COMMENT '协议方言：openai_chat | openai_responses | anthropic_messages。协议是数据面的分发键，厂商交给 vendor；新增协议 = 新增适配器 + 新增取值，不改表。',
  cred_group    VARCHAR(64)     NOT NULL COMMENT '凭据分组，商家内唯一；同厂商多端点共享一份凭据',
  base_url      VARCHAR(255)    NOT NULL,
  enabled       TINYINT         NOT NULL DEFAULT 1,
  priority      INT             NOT NULL DEFAULT 100 COMMENT '路由排序，值大者优先',
  weight        INT             NOT NULL DEFAULT 100 COMMENT '同优先级内的加权随机权重',
  rate_limit_qps         INT    NOT NULL DEFAULT 0 COMMENT '每秒请求上限，0 表示不限',
  rate_limit_concurrency INT    NOT NULL DEFAULT 0 COMMENT '并发请求上限，0 表示不限',
  config        JSON            NULL COMMENT '渠道级扩展配置，结构随协议方言演进，不为此改表',
  created_at    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_channel_identity (merchant_id, type, name),
  KEY idx_channel_route (type, enabled, priority)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='上游渠道：一行一个协议端点';

CREATE TABLE IF NOT EXISTS upstream_credential (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  merchant_id BIGINT UNSIGNED NOT NULL DEFAULT 1,
  cred_group  VARCHAR(64)     NOT NULL,
  name        VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '同组内区分多行凭据，用于轮换',
  secret      JSON            NOT NULL COMMENT '凭据原文（api_key 等）。用 JSON 承载不同协议方言的附加字段，结构不进表定义。',
  enabled     TINYINT         NOT NULL DEFAULT 1,
  created_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_credential_group (merchant_id, cred_group, enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='上游凭据：按 cred_group 挂，同组可多行用于轮换';

CREATE TABLE IF NOT EXISTS upstream_model_map (
  id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  channel_id       BIGINT UNSIGNED NOT NULL,
  model            VARCHAR(128)    NOT NULL COMMENT '客户端请求的模型名',
  upstream_model   VARCHAR(128)    NOT NULL COMMENT '转发给上游的模型名，可与客户端模型名不同',
  price_multiplier DECIMAL(10,4)   NOT NULL DEFAULT 1 COMMENT '渠道级倍率，结算时与套餐、账户倍率相乘',
  request_overrides JSON           NULL COMMENT '覆盖请求参数的 JSON，如温度、最大 token',
  enabled          TINYINT         NOT NULL DEFAULT 1,
  created_at       DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_model_map (channel_id, model),
  KEY idx_model_map_model (model, enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='渠道与模型映射：协议由所属渠道的 type 承载';

CREATE TABLE IF NOT EXISTS account (
  id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  code                VARCHAR(64)     NOT NULL,
  name                VARCHAR(128)    NOT NULL,
  default_merchant_id BIGINT UNSIGNED NULL COMMENT '默认结算商家；NULL 表示走平台自营',
  price_multiplier    DECIMAL(10,4)   NOT NULL DEFAULT 1 COMMENT '账户级倍率，结算链的最后一环',
  status              VARCHAR(16)     NOT NULL DEFAULT 'active' COMMENT 'active | disabled',
  created_at          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_account_code (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='账户。刻意没有 balance 列：会消耗的存量统一放 account_bucket，避免余额列与额度计数器双写不一致。';

CREATE TABLE IF NOT EXISTS account_api_key (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  account_id   BIGINT UNSIGNED NOT NULL,
  merchant_id  BIGINT UNSIGNED NULL COMMENT '该密钥绑定的商家，粒度细于账户；NULL 表示跟随账户默认',
  name         VARCHAR(64)     NOT NULL DEFAULT '',
  key_hash     VARCHAR(128)    NOT NULL COMMENT '密钥哈希，不存明文',
  key_prefix   VARCHAR(16)     NOT NULL DEFAULT '' COMMENT '明文前缀，仅供展示与检索',
  enabled      TINYINT         NOT NULL DEFAULT 1,
  expires_at   DATETIME        NULL,
  last_used_at DATETIME        NULL,
  created_at   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_api_key_hash (key_hash),
  KEY idx_api_key_account (account_id, enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='客户端凭证';

CREATE TABLE IF NOT EXISTS account_bucket (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  account_id  BIGINT UNSIGNED NOT NULL,
  merchant_id BIGINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '存量包是谁卖的',
  unit        VARCHAR(16)     NOT NULL COMMENT 'currency | token | credit',
  total       DECIMAL(24,8)   NOT NULL,
  remaining   DECIMAL(24,8)   NOT NULL,
  expires_at  DATETIME        NULL,
  fallback    VARCHAR(16)     NOT NULL COMMENT 'charge_balance=扣完转后付 | reject=扣完拒绝',
  source      VARCHAR(16)     NOT NULL COMMENT 'purchase | grant | recharge',
  priority    INT             NOT NULL DEFAULT 100 COMMENT '扣减顺序，先过期先扣',
  created_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_bucket_account_unit (account_id, unit, priority)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='统一账本：余额 / token 包 / 赠送积分，都是会消耗的存量。与窗口限额的分界是存量对上限。';

CREATE TABLE IF NOT EXISTS billing_usage (
  id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  merchant_id      BIGINT UNSIGNED NOT NULL DEFAULT 1,
  account_id       BIGINT UNSIGNED NOT NULL,
  channel_id       BIGINT UNSIGNED NOT NULL,
  model            VARCHAR(128)    NOT NULL,
  `usage`          JSON            NOT NULL COMMENT 'metric -> 数量，适配器原样上报的用量分量',
  pricing_id       BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '定价版本占位，定价表落地前固定 0；刻意不建外键',
  pricing_snapshot JSON            NULL COMMENT '结算时的单价与阶梯，价格变更不改写历史流水，对账可复算',
  gross_amount     DECIMAL(24,8)   NOT NULL DEFAULT 0 COMMENT '基础价 × 用量',
  multiplier       DECIMAL(10,4)   NOT NULL DEFAULT 1 COMMENT '解析后的最终倍率：渠道 -> 套餐 -> 账户相乘',
  settlement       JSON            NULL COMMENT '多账本扣减明细，一行流水可对应多行扣减',
  created_at       DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_usage_account_time (account_id, created_at),
  KEY idx_usage_merchant_time (merchant_id, created_at),
  KEY idx_usage_channel_time (channel_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='用量流水，append-only：只插入不更新，事实不可篡改';
