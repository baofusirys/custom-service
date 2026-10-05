-- 安全加固：账号会话版本。递增后该账号签发的全部旧 JWT 立即失效。
SET NAMES utf8mb4;
SET time_zone = '+08:00';
SET @has_agent_version := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='agents' AND COLUMN_NAME='token_version');
SET @ddl_agent := IF(@has_agent_version=0, 'ALTER TABLE agents ADD COLUMN token_version BIGINT NOT NULL DEFAULT 0', 'DO 0');
PREPARE s_agent FROM @ddl_agent; EXECUTE s_agent; DEALLOCATE PREPARE s_agent;

SET @has_file_token := (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='files' AND COLUMN_NAME='access_token_hash');
SET @ddl_file := IF(@has_file_token=0, CONCAT('ALTER TABLE files ADD COLUMN access_token_hash CHAR(64) NOT NULL DEFAULT ', QUOTE('')), 'DO 0');
PREPARE s_file FROM @ddl_file; EXECUTE s_file; DEALLOCATE PREPARE s_file;

SET @has_file_token_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='files' AND INDEX_NAME='idx_files_access_token');
SET @ddl_file_idx := IF(@has_file_token_idx=0, 'CREATE INDEX idx_files_access_token ON files(access_token_hash)', 'DO 0');
PREPARE s_file_idx FROM @ddl_file_idx; EXECUTE s_file_idx; DEALLOCATE PREPARE s_file_idx;
