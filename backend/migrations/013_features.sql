-- [113] 0.8.0 新功能：删除回收站、消息撤回、客户资料（备注/置顶/标为未读/拉黑/历史清空时间）、
--       标签、IP 拉黑、快捷回复。全部幂等：重复执行不报错、不重复建结构。
-- 回滚见 down/013_features.sql（删除新增表与列，回收站/撤回/资料数据会丢失，需配合迁移前自动备份恢复）。
SET NAMES utf8mb4;
SET time_zone = '+08:00';

SET @has_conv_deleted := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='conversations' AND COLUMN_NAME='deleted_at');
SET @ddl_conv_deleted := IF(@has_conv_deleted=0, CONCAT('ALTER TABLE conversations ADD COLUMN deleted_at DATETIME(3) NULL, ADD COLUMN deleted_by VARCHAR(64) NOT NULL DEFAULT ', QUOTE('')), 'DO 0');
PREPARE s_conv_deleted FROM @ddl_conv_deleted; EXECUTE s_conv_deleted; DEALLOCATE PREPARE s_conv_deleted;

SET @has_conv_deleted_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='conversations' AND INDEX_NAME='idx_conv_deleted');
SET @ddl_conv_deleted_idx := IF(@has_conv_deleted_idx=0, 'CREATE INDEX idx_conv_deleted ON conversations(deleted_at)', 'DO 0');
PREPARE s_conv_deleted_idx FROM @ddl_conv_deleted_idx; EXECUTE s_conv_deleted_idx; DEALLOCATE PREPARE s_conv_deleted_idx;

SET @has_msg_recalled := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='messages' AND COLUMN_NAME='recalled_at');
SET @ddl_msg_recalled := IF(@has_msg_recalled=0, CONCAT('ALTER TABLE messages ADD COLUMN recalled_at DATETIME(3) NULL, ADD COLUMN recalled_by VARCHAR(64) NOT NULL DEFAULT ', QUOTE('')), 'DO 0');
PREPARE s_msg_recalled FROM @ddl_msg_recalled; EXECUTE s_msg_recalled; DEALLOCATE PREPARE s_msg_recalled;

CREATE TABLE IF NOT EXISTS visitor_profiles (
  visitor_id        CHAR(36) NOT NULL PRIMARY KEY,
  note              VARCHAR(500) NOT NULL DEFAULT '',
  pinned_at         DATETIME(3) NULL,
  marked_unread     TINYINT(1) NOT NULL DEFAULT 0,
  blocked_at        DATETIME(3) NULL,
  blocked_by        VARCHAR(64) NOT NULL DEFAULT '',
  block_reason      VARCHAR(200) NOT NULL DEFAULT '',
  history_reset_at  DATETIME(3) NULL,
  updated_by        VARCHAR(64) NOT NULL DEFAULT '',
  updated_at        DATETIME(3) NOT NULL,
  KEY idx_profile_pinned (pinned_at),
  KEY idx_profile_blocked (blocked_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

CREATE TABLE IF NOT EXISTS visitor_tags (
  visitor_id  CHAR(36) NOT NULL,
  tag         VARCHAR(16) NOT NULL,
  created_at  DATETIME(3) NOT NULL,
  PRIMARY KEY (visitor_id, tag),
  KEY idx_tag (tag)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

CREATE TABLE IF NOT EXISTS blocked_ips (
  ip_hash     CHAR(64) NOT NULL PRIMARY KEY,
  visitor_id  CHAR(36) NOT NULL,
  blocked_by  VARCHAR(64) NOT NULL DEFAULT '',
  created_at  DATETIME(3) NOT NULL,
  KEY idx_blocked_ip_visitor (visitor_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

CREATE TABLE IF NOT EXISTS quick_replies (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  agent_id    BIGINT UNSIGNED NULL,
  title       VARCHAR(64) NOT NULL DEFAULT '',
  content     TEXT NOT NULL,
  sort_order  INT NOT NULL DEFAULT 0,
  created_by  VARCHAR(64) NOT NULL DEFAULT '',
  created_at  DATETIME(3) NOT NULL,
  updated_at  DATETIME(3) NOT NULL,
  KEY idx_quick_reply_agent (agent_id, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;
