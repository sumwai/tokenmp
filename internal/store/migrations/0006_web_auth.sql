-- 页面账号体系：账号、会话、第三方身份与一次性验证码。
--
-- 与既有 account（模型调用的计费主体）是两个概念：web_user 是登录主体，
-- 一个登录主体经业务关系关联到 account；本迁移只做加法，不触碰既有表。

CREATE TABLE IF NOT EXISTS web_user (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  email         VARCHAR(255)    NOT NULL,
  username      VARCHAR(64)     NOT NULL,
  password_hash VARCHAR(255)    NULL COMMENT 'bcrypt；仅第三方登录创建的账号为 NULL',
  role          VARCHAR(32)     NOT NULL DEFAULT 'member' COMMENT 'admin | member；取值由服务端下发',
  status        VARCHAR(16)     NOT NULL DEFAULT 'active' COMMENT 'active | disabled | erased',
  created_at    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_web_user_email (email),
  UNIQUE KEY uk_web_user_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='页面登录主体。密码只存哈希；OAuth-only 账号密码哈希为 NULL。';

CREATE TABLE IF NOT EXISTS web_session (
  id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id             BIGINT UNSIGNED NOT NULL,
  access_hash         CHAR(64)        NOT NULL COMMENT '访问令牌的 SHA-256 十六进制；明文不落库',
  refresh_hash        CHAR(64)        NOT NULL COMMENT '刷新令牌的 SHA-256 十六进制；一次性轮换',
  prev_refresh_hash   CHAR(64)        NULL COMMENT '上一枚刷新令牌；命中即判定重放并撤销整个会话',
  access_expires_at   DATETIME        NOT NULL,
  refresh_expires_at  DATETIME        NOT NULL,
  revoked_at          DATETIME        NULL,
  created_at          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_session_access (access_hash),
  UNIQUE KEY uk_session_refresh (refresh_hash),
  KEY idx_session_prev (prev_refresh_hash),
  KEY idx_session_user (user_id, revoked_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='页面会话。访问令牌与刷新令牌都只存哈希；改密、注销与登出经 revoked_at 失效。';

CREATE TABLE IF NOT EXISTS web_identity (
  id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id    BIGINT UNSIGNED NOT NULL,
  provider   VARCHAR(32)     NOT NULL COMMENT 'google | github',
  subject    VARCHAR(255)    NOT NULL COMMENT '提供方侧的稳定用户标识',
  email      VARCHAR(255)    NOT NULL DEFAULT '',
  created_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_identity_provider_subject (provider, subject),
  KEY idx_identity_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='第三方登录身份与页面账号的绑定关系。';

CREATE TABLE IF NOT EXISTS web_otp (
  id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  email      VARCHAR(255)    NOT NULL,
  purpose    VARCHAR(16)     NOT NULL COMMENT 'reset | erase；用途之间不可混用',
  code_hash  CHAR(64)        NOT NULL COMMENT '验证码的 SHA-256 十六进制；明文不落库',
  expires_at DATETIME        NOT NULL,
  used_at    DATETIME        NULL,
  created_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_otp_email_purpose (email, purpose, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='邮箱一次性验证码。用途隔离，用后置 used_at。';
