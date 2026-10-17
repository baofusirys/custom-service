-- 回滚 016：删除拦截记录表与敏感词/告警邮件设置（拦截记录全部丢失，此回滚有损；需保留请用迁移前自动备份恢复）。
-- 代码回滚到 0.9.1 时可以不执行本脚本（旧代码不读这些表和设置，保留无害）。
SET NAMES utf8mb4;
SET time_zone = '+08:00';
DROP TABLE IF EXISTS hall_filter_events;
DELETE FROM settings WHERE key_name LIKE 'hall\_filter\_%' OR key_name LIKE 'hall\_alert\_%';
