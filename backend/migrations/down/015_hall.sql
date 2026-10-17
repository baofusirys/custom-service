-- 回滚 015：删除大厅消息表与大厅设置项（大厅聊天记录全部丢失，此回滚有损）。
-- 如需保留数据，请先用迁移前自动备份 /srv/cs-backup/pre-migrate/ 下对应文件恢复。
-- 说明：大厅附件的 files 记录（conv_id='hall_default'）与磁盘文件不在此删除，回滚后它们不再被任何消息引用，可按需人工清理。
-- 代码回滚到 0.8.2 时可以不执行本脚本（旧代码不读 hall_messages，保留无害）。
SET NAMES utf8mb4;
SET time_zone = '+08:00';
DROP TABLE IF EXISTS hall_messages;
DELETE FROM settings WHERE key_name IN ('hall_enabled','hall_video_enabled','hall_max_speakers','hall_max_cameras','hall_max_participants');
