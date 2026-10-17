-- [122] 多人大厅敏感词屏蔽：拦截记录表（原文 AES-GCM 加密存储，屏蔽后内容明文，邮件告警发件箱状态）。全部幂等。
-- 执行前由后端启动流程自动全量备份到 pre-migrate 目录。回滚见 down/016_hall_filter.sql（拦截记录会丢失）。
-- 敏感词与邮件设置存在 settings 表（键 hall_filter_* / hall_alert_*），由代码提供默认值，这里不写入。
SET NAMES utf8mb4;
SET time_zone = '+08:00';

CREATE TABLE IF NOT EXISTS hall_filter_events (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  message_id    CHAR(36) NOT NULL,                  -- 对应 hall_messages.id
  room          VARCHAR(40) NOT NULL,
  sender        VARCHAR(16) NOT NULL,               -- visitor / agent
  sender_ref    VARCHAR(64) NOT NULL DEFAULT '',    -- 访客 vid / 客服 id（仅管理员接口与告警邮件可见）
  identity      VARCHAR(40) NOT NULL DEFAULT '',
  name          VARCHAR(64) NOT NULL DEFAULT '',
  categories    VARCHAR(100) NOT NULL DEFAULT '',   -- 逗号分隔：ad,link,porn,gambling,contact,custom
  original_enc  TEXT NOT NULL,                      -- 原文（AES-256-GCM，DATA_AES_KEY 派生子密钥）
  masked        TEXT NOT NULL,                      -- 屏蔽后内容（与 hall_messages.content 一致）
  mail_status   VARCHAR(16) NOT NULL DEFAULT 'pending', -- pending / sent / failed / disabled / silenced
  mail_attempts TINYINT UNSIGNED NOT NULL DEFAULT 0,
  mail_next_at  DATETIME(3) NULL,
  mail_sent_at  DATETIME(3) NULL,
  mail_error    VARCHAR(255) NULL,
  created_at    DATETIME(3) NOT NULL,
  UNIQUE KEY uk_hall_filter_msg (message_id),
  KEY idx_hall_filter_mail (mail_status, mail_next_at),
  KEY idx_hall_filter_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;
