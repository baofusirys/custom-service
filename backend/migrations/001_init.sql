-- =========================================================
-- 客服系统初始 schema · v0.1.0
-- 设计原则：
--   1. 所有时间字段 DATETIME（DSN loc=Asia/Shanghai 已保证东八区写入）
--   2. 业务主键统一用 CHAR(36) UUID，避免分布式 ID 碰撞
--   3. 高频查询字段都建索引；不滥建索引
--   4. innodb_row_format=DYNAMIC，长文本不溢出
-- =========================================================

SET NAMES utf8mb4;
SET time_zone = '+08:00';

-- 客服 / 管理员
CREATE TABLE IF NOT EXISTS agents (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  username    VARCHAR(64) NOT NULL,
  pass_hash   VARCHAR(120) NOT NULL,
  role        VARCHAR(16) NOT NULL DEFAULT 'agent', -- admin / agent
  nickname    VARCHAR(64) NOT NULL DEFAULT '',
  active      TINYINT(1) NOT NULL DEFAULT 1,
  created_at  DATETIME NOT NULL,
  last_login  DATETIME NULL,
  UNIQUE KEY uk_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- 访客（含画像）
CREATE TABLE IF NOT EXISTS visitors (
  id          CHAR(36) NOT NULL PRIMARY KEY,
  site_id     VARCHAR(64) NOT NULL DEFAULT 'default',
  ip_cipher   VARCHAR(255) NOT NULL DEFAULT '', -- AES-GCM 加密后的 IP（不存明文）
  ua          VARCHAR(512) NOT NULL DEFAULT '',
  country     VARCHAR(64) NOT NULL DEFAULT '',
  city        VARCHAR(64) NOT NULL DEFAULT '',
  referer     VARCHAR(1024) NOT NULL DEFAULT '',
  last_page   VARCHAR(1024) NOT NULL DEFAULT '',
  identifier  VARCHAR(128) NOT NULL DEFAULT '', -- 客户自填的姓名/邮箱
  first_seen  DATETIME NOT NULL,
  last_seen   DATETIME NOT NULL,
  KEY idx_site_last_seen (site_id, last_seen)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- 会话
CREATE TABLE IF NOT EXISTS conversations (
  id              CHAR(36) NOT NULL PRIMARY KEY,
  site_id         VARCHAR(64) NOT NULL DEFAULT 'default',
  visitor_id      CHAR(36) NOT NULL,
  agent_id        BIGINT UNSIGNED NULL,
  status          VARCHAR(16) NOT NULL DEFAULT 'open', -- open / closed
  unread_visitor  INT NOT NULL DEFAULT 0,
  unread_agent    INT NOT NULL DEFAULT 0,
  started_at      DATETIME NOT NULL,
  updated_at      DATETIME NOT NULL,
  closed_at       DATETIME NULL,
  KEY idx_status_updated (status, updated_at),
  KEY idx_visitor (visitor_id, status),
  KEY idx_agent (agent_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- 消息（按 conv_id + created_at 高频查询）
CREATE TABLE IF NOT EXISTS messages (
  id            CHAR(36) NOT NULL PRIMARY KEY,
  conv_id       CHAR(36) NOT NULL,
  sender        VARCHAR(16) NOT NULL,         -- visitor / agent / sys
  sender_ref    VARCHAR(64) NOT NULL,         -- visitorID 或 agentID
  content       TEXT NOT NULL,
  media_url     VARCHAR(512) NULL,
  media_kind    VARCHAR(16) NULL,             -- image / file
  media_name    VARCHAR(256) NULL,
  media_size    BIGINT NULL,
  delivered_ws  TINYINT(1) NOT NULL DEFAULT 0,
  created_at    DATETIME NOT NULL,
  KEY idx_conv_time (conv_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- 上传文件元数据（真实文件在 /app/uploads/<store_key>）
CREATE TABLE IF NOT EXISTS files (
  id            CHAR(36) NOT NULL PRIMARY KEY,
  conv_id       CHAR(36) NULL,
  upload_by     VARCHAR(16) NOT NULL,
  uploader_ref  VARCHAR(64) NOT NULL,
  filename      VARCHAR(256) NOT NULL,
  store_key     VARCHAR(256) NOT NULL,
  size          BIGINT NOT NULL,
  mime          VARCHAR(128) NOT NULL,
  created_at    DATETIME NOT NULL,
  KEY idx_conv (conv_id),
  KEY idx_uploader (upload_by, uploader_ref)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- 审计日志（管理后台操作 + 关键安全事件）
-- 注意：这里的 audit_logs 仅记录「结构化、可检索」的管理操作；
--      原始 / 详细业务日志、安全日志、WSS 原始报文，全部走 zap 落到文件，bind 到宿主机长效保存。
CREATE TABLE IF NOT EXISTS audit_logs (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  actor       VARCHAR(64) NOT NULL,
  action      VARCHAR(64) NOT NULL,
  target      VARCHAR(256) NOT NULL DEFAULT '',
  detail      TEXT NULL,
  ip          VARCHAR(64) NOT NULL DEFAULT '',
  created_at  DATETIME NOT NULL,
  KEY idx_actor_time (actor, created_at),
  KEY idx_action_time (action, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;
