-- [083] Bug① 数据修复：把历史上被「30 分钟超时自动关闭」误埋的「有未读」会话恢复为 open。
--
-- 背景（大白话）：
--   旧逻辑里，访客隔 30 分钟再进来，系统会把上一段会话直接关掉(status=closed)再开一段新的。
--   但它没检查「这段旧会话里是不是还有客服没看过的消息(unread_agent>0)」。
--   结果：有未读的会话被关 → 工作台会话列表只查 status='open' → 这条会话不显示了 →
--   客服明明收到过「访客进入/新消息」通知，点进列表却找不到人，未读消息被埋。
--   （消息本身没丢，一直在 messages 表，只是承载它的会话被关、列表不展示。）
--
-- 修复：把这类「已关闭但仍有客服未读」的会话恢复成 open、清空 closed_at，
--   让客服能在工作台重新看到并处理。
--
-- 边界与安全：
--   - 只动 status='closed' AND unread_agent>0 的会话；正常关闭(无未读)的会话一律不动。
--   - 幂等：全新部署的库没有这类数据，UPDATE 命中 0 行；schema_migrations 记录后不再重复执行。
--   - 配套代码修复见 store.go EnsureFreshConversation：今后超时重开前先查未读，有未读直接复用旧会话，从源头杜绝再产生。
UPDATE conversations
SET status = 'open', closed_at = NULL
WHERE status = 'closed' AND unread_agent > 0;
