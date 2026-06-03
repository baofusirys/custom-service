-- [077] 修复历史孤儿消息（conv_id 为空串或指向已不存在的会话，导致按会话查不到、界面"消失"）。
--
-- 背景：客服 WSS 建连时 c.ConvID 为空，需点开会话(AssignSelf)才绑定；在"建连→接管"窗口内发的
--   消息被空 conv_id 入库 → 孤儿。生产实测 5120 条中 26 条 conv_id 为空，全部 sender=agent。
--   根因已在 [077] 后端代码层堵死（PreprocessAgentMessage 拒绝空 conv + InsertMessage 非空兜底），
--   本迁移负责把"已经产生"的孤儿消息找回正确的会话归属。
--
-- 关联策略：按 sender_ref(客服 agentID / 访客 visitorID) + created_at 落在会话活跃区间内、
--   且 updated_at 与 created_at 时间最接近的那个会话（防关联错乱，不取最早会话）。
-- 安全：全程参数化的相关子查询、REGEXP 防 CAST 非数字 sender_ref；关联不到会话的不动（保留原始数据，人工核）。
-- 事务：migrate.go 在单事务内执行本文件，任一语句失败整体回滚，schema_migrations 去重保证只跑一次。

-- ============ A. 修复 agent 孤儿：按 agent_id + 时间最接近 ============
UPDATE messages m
SET m.conv_id = (
    SELECT c.id FROM conversations c
    WHERE c.agent_id = CAST(m.sender_ref AS UNSIGNED)
      AND c.started_at <= m.created_at
      AND (c.closed_at IS NULL OR c.closed_at >= m.created_at)
    ORDER BY ABS(TIMESTAMPDIFF(SECOND, c.updated_at, m.created_at)) ASC
    LIMIT 1
)
WHERE m.sender = 'agent'
  AND m.sender_ref REGEXP '^[0-9]+$'
  AND (m.conv_id = '' OR NOT EXISTS (SELECT 1 FROM conversations cx WHERE cx.id = m.conv_id))
  AND EXISTS (
    SELECT 1 FROM conversations c2
    WHERE c2.agent_id = CAST(m.sender_ref AS UNSIGNED)
      AND c2.started_at <= m.created_at
      AND (c2.closed_at IS NULL OR c2.closed_at >= m.created_at)
  );

-- ============ B. 修复 visitor 孤儿（如有）：按 visitor_id + 时间最接近 ============
UPDATE messages m
SET m.conv_id = (
    SELECT c.id FROM conversations c
    WHERE c.visitor_id = m.sender_ref
      AND c.started_at <= m.created_at
      AND (c.closed_at IS NULL OR c.closed_at >= m.created_at)
    ORDER BY ABS(TIMESTAMPDIFF(SECOND, c.updated_at, m.created_at)) ASC
    LIMIT 1
)
WHERE m.sender = 'visitor'
  AND (m.conv_id = '' OR NOT EXISTS (SELECT 1 FROM conversations cx WHERE cx.id = m.conv_id))
  AND EXISTS (
    SELECT 1 FROM conversations c2
    WHERE c2.visitor_id = m.sender_ref
      AND c2.started_at <= m.created_at
      AND (c2.closed_at IS NULL OR c2.closed_at >= m.created_at)
  );
