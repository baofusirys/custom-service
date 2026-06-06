-- [085] 「已联系」口径重构：新增 agent_replied 标记列（本会话客服是否回复过 sender='agent'）。
--
-- 背景（大白话）：
--   原「已联系」是看"当前这条会话里有没有访客消息"来判定。但访客离开超 30 分钟再进来会被
--   超时重建会话——历史消息留在旧的 closed 会话，新 open 会话是"干净"的 → 判定为未联系。
--   叠加后端只返回最近 200 条 open 会话，导致实际接待过 450 个客户、工作台只显示 4 个。
--   爷爷重新定口径：「已联系」= 客服真正回复过(sender='agent')，且按"客户(访客)历史"聚合
--   （该访客名下任一会话被回复过就算），不再只看当前这一段会话。
--
--   本列是为这个口径做的高效标记：客服每次发消息时置 1（见 service.go MarkAgentReplied），
--   「已联系」列表用它按访客聚合，避免每次实时 JOIN messages 表（爷爷要求加载要快）。
--
-- 幂等：用 information_schema 守护 ADD COLUMN / CREATE INDEX，重复执行不报错；回填 UPDATE 天然幂等。

-- 1) 加列（幂等）
SET @has_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'conversations' AND COLUMN_NAME = 'agent_replied');
SET @ddl := IF(@has_col = 0,
  'ALTER TABLE conversations ADD COLUMN agent_replied TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''客服是否在本会话回复过(sender=agent)''',
  'DO 0');
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;

-- 2) 回填存量：客服回复过的会话置 1（幂等：只补 agent_replied=0 的）
UPDATE conversations c SET agent_replied = 1
WHERE agent_replied = 0
  AND EXISTS (SELECT 1 FROM messages m WHERE m.conv_id = c.id AND m.sender = 'agent');

-- 3) 索引 idx_visitor_updated：已联系分页按访客聚合取最新会话用（幂等）
SET @has_idx1 := (SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'conversations' AND INDEX_NAME = 'idx_visitor_updated');
SET @ddl1 := IF(@has_idx1 = 0,
  'CREATE INDEX idx_visitor_updated ON conversations (visitor_id, updated_at)',
  'DO 0');
PREPARE s1 FROM @ddl1; EXECUTE s1; DEALLOCATE PREPARE s1;

-- 4) 索引 idx_agent_replied：按"被回复过"筛访客用（幂等）
SET @has_idx2 := (SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'conversations' AND INDEX_NAME = 'idx_agent_replied');
SET @ddl2 := IF(@has_idx2 = 0,
  'CREATE INDEX idx_agent_replied ON conversations (agent_replied)',
  'DO 0');
PREPARE s2 FROM @ddl2; EXECUTE s2; DEALLOCATE PREPARE s2;
