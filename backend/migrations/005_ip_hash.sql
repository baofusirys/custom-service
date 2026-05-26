-- [055] 为访客唯一性「关联访客」面板增加可索引 IP 哈希字段。
--
-- 背景：ip_cipher 是 AES-GCM 随机 nonce 加密（同一 IP 每次密文不同），无法 WHERE 查询。
-- 方案：新增 ip_hash 字段 = HMAC-SHA256(DATA_AES_KEY, ip) hex (64 字符)
--   - HMAC 用 DATA_AES_KEY 当 key 确保不可逆 + 不能被外部碰撞猜回 IP
--   - 同一 IP 每次哈希结果相同 → 可索引可查
--   - 原 ip_cipher 字段保留：解密给客服看明文 IP 用
--
-- 用途：客服端访客详情页「关联访客 (N)」面板，查 30 天内同 IP 出现过的其他 vid，
--   不强行合并 vid（隐私边界），只给客服参考「疑似同一人」。
--
-- 兼容：旧数据 ip_hash 为空，新关联查询不显示历史。重要不影响业务。

ALTER TABLE visitors
  ADD COLUMN ip_hash CHAR(64) NOT NULL DEFAULT '' AFTER ip_cipher,
  ADD KEY idx_ip_hash (ip_hash, last_seen);
