-- [119] 多人大厅：大厅消息表 + 大厅设置默认值。全部幂等：重复执行不报错、不重复建结构、不覆盖已保存的设置。
-- 执行前由后端启动流程自动全量备份到 pre-migrate 目录（见 cmd/server/main.go backupBeforeMigration）。
-- 回滚见 down/015_hall.sql（删除大厅消息表与大厅设置，大厅聊天记录会丢失：此回滚有损，需保留数据请用迁移前自动备份恢复）。
SET NAMES utf8mb4;
SET time_zone = '+08:00';

CREATE TABLE IF NOT EXISTS hall_messages (
  id          CHAR(36) NOT NULL PRIMARY KEY,
  room        VARCHAR(40) NOT NULL,
  sender      ENUM('visitor','agent','system') NOT NULL,
  sender_ref  VARCHAR(64) NOT NULL DEFAULT '',   -- 访客 vid / 客服 id，仅服务端使用，绝不对外返回
  identity    VARCHAR(40) NOT NULL DEFAULT '',   -- LiveKit identity（访客为 HMAC 派生，不含 vid）
  name        VARCHAR(64) NOT NULL DEFAULT '',
  content     TEXT NOT NULL,
  media_url   VARCHAR(512) NULL,
  media_kind  VARCHAR(16) NULL,                  -- image / file
  media_name  VARCHAR(256) NULL,
  media_size  BIGINT NULL,
  created_at  DATETIME(3) NOT NULL,
  deleted_at  DATETIME(3) NULL,                  -- 客服删除或拉黑静默（deleted_by='silenced'）
  deleted_by  VARCHAR(64) NOT NULL DEFAULT '',
  KEY idx_hall_room_time (room, created_at),
  KEY idx_hall_sender (sender, sender_ref, created_at),
  KEY idx_hall_created (created_at)               -- 每日清理 30 天前消息
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;

-- 大厅设置默认值（已存在则保持管理员保存的值不变）
INSERT INTO settings (key_name, value, updated_at) VALUES
  ('hall_enabled',          'true',  NOW()),
  ('hall_video_enabled',    'false', NOW()),
  ('hall_max_speakers',     '6',     NOW()),
  ('hall_max_cameras',      '2',     NOW()),
  ('hall_max_participants', '100',   NOW())
ON DUPLICATE KEY UPDATE updated_at=updated_at;
