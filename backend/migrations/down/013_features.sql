-- 回滚 013：删除新增表与列（回收站标记、撤回标记、客户资料、标签、IP 拉黑、快捷回复全部丢失）。
-- 此变更回滚有损：如需保留数据，请先用迁移前自动备份 /srv/cs-backup/pre-migrate/ 下对应文件恢复。
SET NAMES utf8mb4;
SET time_zone = '+08:00';
DROP TABLE IF EXISTS quick_replies;
DROP TABLE IF EXISTS blocked_ips;
DROP TABLE IF EXISTS visitor_tags;
DROP TABLE IF EXISTS visitor_profiles;
DROP INDEX idx_conv_deleted ON conversations;
ALTER TABLE conversations DROP COLUMN deleted_at, DROP COLUMN deleted_by;
ALTER TABLE messages DROP COLUMN recalled_at, DROP COLUMN recalled_by;
