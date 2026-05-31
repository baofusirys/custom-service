-- =========================================================
-- [065] 修复 admin 客服端「未读 badge 错算」遗留数据
--
-- 背景：
--   v0.6.0 及之前的 store.InsertMessage 把所有非 agent 消息（含 sys）都 +1 unread_agent，
--   导致客服列表 badge 显示 N 条未读，但点进去只有 N - (sys 条数) 条真实访客消息。
--   例：badge=2 实际只有 1 条访客文字 + 1 条「访客访问了 Cursor账号」的 page_navigation。
--
-- 修复策略：
--   按业务真相重新计算每个 open 会话的 unread_agent =
--     "last_read_agent_at（NULL 视为 started_at）之后，由 visitor 发出的消息条数"
--   sys / agent 自己的消息一律不算未读。
--
-- 依赖：
--   - 003_read_status.sql：引入 last_read_agent_at / last_read_visitor_at 字段
--   - 001_init.sql：conversations 表、messages 表、相关索引
--
-- 幂等：
--   本脚本只做 UPDATE，没有 ADD COLUMN / CREATE TABLE，重复执行结果一致；
--   迁移系统通过 schema_migrations 跳过已执行版本，再加上 SQL 本身幂等，双保险。
--
-- 性能：
--   只处理 status='open' 的会话（已关闭的客服不再看，无需校准）；
--   子查询走 messages.idx_conv_time(conv_id, created_at)，单会话 O(log N)。
--   生产环境 open 会话量级通常 < 1000，整体执行毫秒级，不会锁表造成业务影响。
--
-- 边界：
--   - last_read_agent_at IS NULL（客服从没读过）→ 用 started_at 兜底
--   - 同时校准 unread_visitor 也用同样思路，避免访客端可能存在的对称问题
-- =========================================================

SET NAMES utf8mb4;
SET time_zone = '+08:00';

-- 1) 校准 unread_agent：只数 visitor 真实消息（sys 不算未读）
UPDATE conversations c
LEFT JOIN (
    SELECT m.conv_id, COUNT(*) AS real_unread
    FROM messages m
    JOIN conversations cc ON cc.id = m.conv_id
    WHERE m.sender = 'visitor'
      AND m.created_at > COALESCE(cc.last_read_agent_at, cc.started_at)
      AND cc.status = 'open'
    GROUP BY m.conv_id
) t ON t.conv_id = c.id
SET c.unread_agent = COALESCE(t.real_unread, 0)
WHERE c.status = 'open';

-- 2) 对称校准 unread_visitor（防止未来对称 bug；当前 InsertMessage 对 visitor 端
--    并没有错算的 sys 注入，但提前对齐口径，避免运维困惑）
UPDATE conversations c
LEFT JOIN (
    SELECT m.conv_id, COUNT(*) AS real_unread
    FROM messages m
    JOIN conversations cc ON cc.id = m.conv_id
    WHERE m.sender = 'agent'
      AND m.created_at > COALESCE(cc.last_read_visitor_at, cc.started_at)
      AND cc.status = 'open'
    GROUP BY m.conv_id
) t ON t.conv_id = c.id
SET c.unread_visitor = COALESCE(t.real_unread, 0)
WHERE c.status = 'open';
