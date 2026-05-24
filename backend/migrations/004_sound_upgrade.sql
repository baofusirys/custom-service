-- =========================================================
-- v0.2.2 通知音色升级：从程序合成 (chime/classic/...) → 真实录音 (agent1/visitor1)
--
-- 起因：程序合成的 11 种音色在 iOS Release 模式下音量明显偏小，且 Web 端浏览器
--   降权 / 异步初始化导致同步问题。爷爷给了 6 个真实录音 WAV (3 客服端 + 3 访客端)，
--   音质好、音量足、所有端 (admin/widget/mobile_app) 都通用。
--
-- 升级策略：把数据库里残留的旧合成音色 key 强制更新为新默认。
--   - 已被管理员手动改成新 key 的（agent1~3 / visitor1~3）不影响
--   - 已是 'none' 静音的也不影响
--   - 凡是 chime/classic/ding/soft/alert/bell/doorbell/trill/fanfare/chord 这 10 个
--     旧合成 key，统一回滚到默认 agent1（客服端）/ visitor1（访客端）
-- =========================================================

SET NAMES utf8mb4;
SET time_zone = '+08:00';

UPDATE settings SET value='agent1', updated_at=NOW()
  WHERE key_name='agent_notify_sound'
    AND value IN ('classic','chime','ding','soft','alert','bell','doorbell','trill','fanfare','chord');

UPDATE settings SET value='visitor1', updated_at=NOW()
  WHERE key_name='visitor_notify_sound'
    AND value IN ('classic','chime','ding','soft','alert','bell','doorbell','trill','fanfare','chord');
