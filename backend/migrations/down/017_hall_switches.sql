-- 回滚 017：删除文字聊天、语音开关与最后修改记录（无损：代码回滚后不再读取，默认即开启）。
SET NAMES utf8mb4;
SET time_zone = '+08:00';
DELETE FROM settings WHERE key_name IN ('hall_chat_enabled','hall_voice_enabled','hall_switch_last');
