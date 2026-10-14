-- [115] 会话列表性能：为「按客户聚合」列表与计数增加覆盖索引 idx_conv_list。
-- 背景：列表/计数要对全部未删除会话按 visitor_id 分组，旧查询走 idx_conv_deleted(deleted_at) 做低选择性 ref 查找
--   （几乎所有行 deleted_at 都是 NULL）并随机回表；生产 1.7 万会话时单次 1.3~2.3 秒。
-- 设计：(deleted_at, visitor_id) 前缀让「deleted_at IS NULL + GROUP BY visitor_id」按索引顺序流式分组、无需临时表；
--   updated_at / visitor_engaged / status 覆盖分组聚合（代表会话排序键、已联系判定），started_at 覆盖第 2 阶段代表会话
--   排序（主键 id 是二级索引隐含后缀），全程不回表。保留 idx_conv_deleted（回收站与自动清理按 deleted_at 范围扫描要用）。
-- 幂等：information_schema 守护，重复执行不报错、不重复建索引。纯加索引、不改数据，回滚见 down/014_list_performance.sql（无损）。
-- 执行前由后端启动流程自动全量备份到 pre-migrate 目录（见 cmd/server/main.go backupBeforeMigration）。
SET NAMES utf8mb4;
SET time_zone = '+08:00';

SET @has_conv_list_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='conversations' AND INDEX_NAME='idx_conv_list');
SET @ddl_conv_list_idx := IF(@has_conv_list_idx=0, 'CREATE INDEX idx_conv_list ON conversations(deleted_at, visitor_id, updated_at, visitor_engaged, status, started_at)', 'DO 0');
PREPARE s_conv_list_idx FROM @ddl_conv_list_idx; EXECUTE s_conv_list_idx; DEALLOCATE PREPARE s_conv_list_idx;
