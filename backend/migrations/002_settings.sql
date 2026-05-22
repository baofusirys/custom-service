-- =========================================================
-- v0.2.0 settings 表：管理后台可配置项（通知声音、问候、访客进入提醒等）
-- key 是 MySQL 保留字，用 key_name 替代
-- =========================================================

SET NAMES utf8mb4;
SET time_zone = '+08:00';

CREATE TABLE IF NOT EXISTS settings (
  key_name   VARCHAR(64) NOT NULL PRIMARY KEY,
  value      TEXT NULL,
  updated_at DATETIME NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- 默认值（首次部署时插入；已存在则跳过）
INSERT INTO settings (key_name, value, updated_at) VALUES
  ('agent_notify_sound',  'chime',  NOW()),
  ('visitor_notify_sound','classic',NOW()),
  ('notify_visitor_enter','true',   NOW()),
  ('greeting_enabled',    'true',   NOW()),
  ('greeting_text',       '您好，欢迎光临！请问有什么可以帮您？', NOW())
ON DUPLICATE KEY UPDATE updated_at=updated_at;
