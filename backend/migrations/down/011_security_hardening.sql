-- 回滚 011：仅在恢复备份并确认旧版本代码兼容后执行。
SET NAMES utf8mb4;
SET time_zone = '+08:00';
DROP INDEX idx_files_access_token ON files;
ALTER TABLE files DROP COLUMN access_token_hash;
ALTER TABLE agents DROP COLUMN token_version;
