-- [088]「已联系」口径重定义为「访客主动操作过」：新增 visitor_engaged 标记列
--   （本会话访客是否主动发过文字/图片消息 或 打过语音电话）+ 回填 + 索引。
--
-- 背景（爷爷定的「已联系」真义）：
--   已联系 = 访客手动打字 / 发图片 / 打语音电话过 —— 访客「主动操作」过，不管客服是否回复。
--   纯浏览页面(page_navigation)、收到系统自动问候(greeting)、只是打开网页(访客进入) 都不算。
--   口径 = messages 里 sender='visitor'(文字/图片) OR (sender='sys' 且 sender_ref LIKE 'voice%')(语音通话，含未接/秒挂)。
--
-- 用途：本列供「已联系」按客户聚合判定（避免每次实时 JOIN messages，爷爷要求加载快）；
--   配套 InsertMessage 实时维护（访客消息/voice 事件落库时置 1）。
--
-- 幂等：information_schema 守护 ADD COLUMN / CREATE INDEX，重复执行不报错；回填 UPDATE 天然幂等。

-- 1) 加列（幂等）
SET @has_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'conversations' AND COLUMN_NAME = 'visitor_engaged');
SET @ddl := IF(@has_col = 0,
  'ALTER TABLE conversations ADD COLUMN visitor_engaged TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''访客主动操作过(发消息/图片/语音电话)''',
  'DO 0');
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;

-- 2) 回填存量：访客发过真实消息 或 打过语音电话的会话置 1（幂等：只补 visitor_engaged=0 的）
UPDATE conversations c SET visitor_engaged = 1
WHERE visitor_engaged = 0
  AND EXISTS (SELECT 1 FROM messages m WHERE m.conv_id = c.id
        AND (m.sender = 'visitor' OR (m.sender = 'sys' AND m.sender_ref LIKE 'voice%')));

-- 3) 索引：「已联系」按 visitor_engaged 筛客户用（幂等）
SET @has_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'conversations' AND INDEX_NAME = 'idx_visitor_engaged');
SET @ddl2 := IF(@has_idx = 0,
  'CREATE INDEX idx_visitor_engaged ON conversations (visitor_engaged)',
  'DO 0');
PREPARE s2 FROM @ddl2; EXECUTE s2; DEALLOCATE PREPARE s2;
