-- [123] 大厅开关：文字聊天、语音两个新开关的默认值（已存在则保持不变）。全部幂等。
-- 回滚见 down/017_hall_switches.sql。
SET NAMES utf8mb4;
SET time_zone = '+08:00';
INSERT INTO settings (key_name, value, updated_at) VALUES
  ('hall_chat_enabled',  'true', NOW()),
  ('hall_voice_enabled', 'true', NOW())
ON DUPLICATE KEY UPDATE updated_at=updated_at;
