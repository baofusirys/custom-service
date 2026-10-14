-- 回滚 014：删除会话列表覆盖索引 idx_conv_list（只删索引、不涉及数据，无损；幂等，索引不存在时跳过）。
-- 说明：代码回滚到 0.8.0 时可以不执行本脚本（0.8.0 不使用该索引，保留无害，只多一点写入维护开销）。
-- 删除索引后 0.8.1 列表查询仍可执行（只 IGNORE idx_conv_deleted，不强制依赖 idx_conv_list），但会退回逐行回表、变慢；
-- 之后若要重新建索引：删除 schema_migrations 中 version='014_list_performance' 的记录，重启后端即自动重建（会先自动备份）。
SET NAMES utf8mb4;
SET time_zone = '+08:00';

SET @has_conv_list_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='conversations' AND INDEX_NAME='idx_conv_list');
SET @ddl_conv_list_idx := IF(@has_conv_list_idx>0, 'DROP INDEX idx_conv_list ON conversations', 'DO 0');
PREPARE s_conv_list_idx FROM @ddl_conv_list_idx; EXECUTE s_conv_list_idx; DEALLOCATE PREPARE s_conv_list_idx;
