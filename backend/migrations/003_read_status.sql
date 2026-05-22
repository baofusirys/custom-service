-- =========================================================
-- v0.3.0 已读状态：conversations 表加 2 个时间戳，
-- 通过比较 messages.created_at vs last_read_*_at 判断"是否已读"
-- =========================================================

SET NAMES utf8mb4;
SET time_zone = '+08:00';

-- 用 IF NOT EXISTS 的写法 MySQL 8 不直接支持，
-- 但我们的迁移系统通过 schema_migrations 保证每个版本只跑一次，
-- 所以直接 ADD COLUMN 即可（重复执行会被迁移系统跳过）。
ALTER TABLE conversations
  ADD COLUMN last_read_agent_at   DATETIME NULL,
  ADD COLUMN last_read_visitor_at DATETIME NULL;
