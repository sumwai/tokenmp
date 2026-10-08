-- 请求记录：客户端视角的一次请求一行，附尝试时间线与按天聚合缓存。
--
-- 与 billing_usage 的分工：流水是计费事实（谁扣了多少），本表是运维与排障事实
-- （这次调用发生了什么）。两者都按 request_id 关联，但一行流水的粒度是「一次进入
-- 终态的转发」，一行请求记录的粒度是「客户端发起的一次请求」——重试的失败尝试只进
-- request_attempt，不单独成行。
--
-- 与日志的分工：结构化日志是实时出口，本表是留存出口。写入只发生在请求终态之后，
-- 实现失败不得影响转发结果。
--
-- 三张表都不建外键：一致性由应用层保证，只建索引（与既有约定同源）。

-- 请求记录主表。字段分三层：摘要层、归属层、报文层。
--
-- status / http_status / duration_ms / written_bytes 可空：它们由请求终态写入，
-- 而归属与用量可能先于终态落库（用量在回写客户端之前写入）。空值表示「这次请求
-- 没有走到终态」，列表与聚合都按 status IS NOT NULL 过滤，不把中断的记录当成事实。
CREATE TABLE IF NOT EXISTS request_log (
  id                     BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  request_id             VARCHAR(64)     NOT NULL COMMENT '客户端请求的关联键，列表按它定位单条记录',
  merchant_id            BIGINT UNSIGNED NOT NULL DEFAULT 1,
  account_id             BIGINT UNSIGNED NOT NULL,
  api_key_id             BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '签发本次调用的密钥；0 表示无密钥维度',
  created_at             DATETIME        NOT NULL COMMENT '请求时刻，取用量写入时刻',
  status                 VARCHAR(16)     NULL COMMENT 'success | failed | cancelled；NULL 表示未进入终态',
  http_status            INT             NULL COMMENT '返回给客户端的 HTTP 状态码',
  upstream_status        INT             NULL COMMENT '最后一次上游尝试的状态码；未取得时为 NULL',
  failure_class          VARCHAR(32)     NULL COMMENT '失败分类；成功时为 NULL',
  error_code             VARCHAR(64)     NULL COMMENT '网关错误码；成功时为 NULL',
  duration_ms            BIGINT          NULL COMMENT '上游调用耗时，不含向客户端写出的耗时',
  requested_model        VARCHAR(128)    NULL COMMENT '客户端请求的模型名',
  upstream_model         VARCHAR(128)    NULL COMMENT '实际发往上游的模型名',
  protocol               VARCHAR(32)     NULL COMMENT '客户端使用的线协议',
  upstream_protocol      VARCHAR(32)     NULL COMMENT '本次请求发往上游的线协议',
  cross_protocol         TINYINT         NOT NULL DEFAULT 0 COMMENT '两侧协议是否不同',
  stream                 TINYINT         NOT NULL DEFAULT 0,
  written_bytes          BIGINT          NULL COMMENT '已写给客户端的响应体字节数',
  client_ip              VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '连接层地址；存在反代时为代理地址，只作排障线索',
  user_agent             VARCHAR(512)    NOT NULL DEFAULT '' COMMENT '客户端自报的 User-Agent，可按上限截断；可伪造',
  `usage`                JSON            NULL COMMENT '取得的 token 用量；未取得时为 NULL',
  payload_available      TINYINT         NOT NULL DEFAULT 0 COMMENT '是否存在可取的脱敏报文',
  request_shape          JSON            NULL COMMENT '脱敏后的客户端请求结构',
  upstream_request_shape JSON            NULL COMMENT '脱敏后实际发往上游的请求结构',
  error_response_shape   JSON            NULL COMMENT '脱敏后的错误响应结构',
  rewritten_parts        JSON            NULL COMMENT '网关对报文做过的改写标注',
  PRIMARY KEY (id),
  UNIQUE KEY uk_request_log_request (request_id),
  KEY idx_request_log_account_time (account_id, created_at),
  KEY idx_request_log_payload_time (created_at, payload_available)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='请求记录：客户端视角一次请求一行，附摘要与归属字段';

-- 尝试时间线。重试、渠道回退与渠道内凭据轮换各占一条，按尝试序号排列。
--
-- 独立成表而不是在主表存 JSON 数组：一次请求一行、一次尝试一行是同一粒度的两级，
-- 展开成行后「某渠道失败率」这类聚合是普通查询，而 JSON 数组要先解析再聚合。
CREATE TABLE IF NOT EXISTS request_attempt (
  id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  request_id        VARCHAR(64)     NOT NULL,
  attempt           INT             NOT NULL COMMENT '尝试序号，从 1 起',
  outcome           VARCHAR(16)     NOT NULL COMMENT 'ok | failed | cancelled | skipped',
  upstream_status   INT             NULL COMMENT '本次尝试取得的上游状态码；未取得时为 NULL',
  failure_class     VARCHAR(32)     NULL,
  error_code        VARCHAR(64)     NULL,
  client_protocol   VARCHAR(32)     NULL,
  upstream_protocol VARCHAR(32)     NULL,
  cross_protocol    TINYINT         NOT NULL DEFAULT 0,
  duration_ms       BIGINT          NOT NULL DEFAULT 0 COMMENT '本次上游调用耗时',
  requested_model   VARCHAR(128)    NULL,
  upstream_model    VARCHAR(128)    NULL,
  rewritten_parts   JSON            NULL,
  created_at        DATETIME        NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_request_attempt (request_id, attempt),
  KEY idx_request_attempt_request (request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='请求尝试时间线：重试与回退各占一行';

-- 按天聚合缓存。随明细写入同步累加，明细归档后仍可回答趋势与错误率。
--
-- 三个维度（日期、模型、终态）合成一行计数，而不是每个 group_by 一张表：
-- 分组维度是查询侧的选择，不是事实的层次，合成一行后三种分组都是对同一份数据的
-- 求和，口径必然一致。计数字段只累加不重算，因此本表在明细超期后仍是唯一依据。
CREATE TABLE IF NOT EXISTS request_stats_daily (
  id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  account_id BIGINT UNSIGNED NOT NULL,
  `day`      DATE            NOT NULL COMMENT '按写入时刻取本地日期',
  model      VARCHAR(128)    NOT NULL COMMENT '客户端请求的模型名',
  api_key_id BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '签发本次调用的密钥；0 表示无密钥维度',
  status     VARCHAR(16)     NOT NULL,
  total      BIGINT          NOT NULL DEFAULT 0,
  PRIMARY KEY (id),
  UNIQUE KEY uk_request_stats_daily (account_id, `day`, model, api_key_id, status),
  KEY idx_request_stats_account_day (account_id, `day`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='请求计数按天聚合缓存：明细归档后的唯一依据';
