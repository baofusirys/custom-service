-- [110] 消息时间精度：messages.created_at 由秒级 DATETIME 提升为毫秒 DATETIME(3)。
-- 原因：同一秒内的多条消息（访客连发、图片+文字、问候语+首条消息）按秒排序时顺序不确定，客服端显示错乱。
-- 存量数据毫秒部分为 .000，不改变既有先后关系；幂等：已是 DATETIME(3) 及以上时跳过。
-- 回滚见 down/012_message_time_precision.sql（会截断毫秒，属有损回滚，需配合恢复迁移前自动备份）。
SET NAMES utf8mb4;
SET time_zone = '+08:00';
SET @msg_time_precision := (SELECT DATETIME_PRECISION FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='messages' AND COLUMN_NAME='created_at');
SET @ddl_msg_time := IF(@msg_time_precision IS NOT NULL AND @msg_time_precision < 3, 'ALTER TABLE messages MODIFY COLUMN created_at DATETIME(3) NOT NULL', 'DO 0');
PREPARE s_msg_time FROM @ddl_msg_time; EXECUTE s_msg_time; DEALLOCATE PREPARE s_msg_time;
