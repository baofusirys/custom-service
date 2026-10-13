-- 回滚 012：messages.created_at 恢复为秒级 DATETIME（毫秒部分被截断，同秒消息先后关系丢失）。
-- 仅在回滚到 v0.7.4 及更早代码时执行；如需完全恢复，使用迁移前自动备份 /srv/cs-backup/pre-migrate/ 下对应文件。
SET NAMES utf8mb4;
SET time_zone = '+08:00';
ALTER TABLE messages MODIFY COLUMN created_at DATETIME NOT NULL;
