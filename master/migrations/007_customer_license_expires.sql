-- 给 customers 加 license 到期时间字段, 用于客户详情页显示倒计时
-- (token 本身不存数据库, 防泄露; 这里单独存 expires_at 作为运维可见信息)
ALTER TABLE customers ADD COLUMN IF NOT EXISTS license_expires_at TIMESTAMP;

-- backfill 已存在客户: 按默认 365 天估算 (create_at + 365d)
-- 不准也无碍业务: 客户实际 license 由 token 内 ExpiresAt 决定; 管理员任何一次 /issue
-- 或重签 license 都会用准确值覆盖这里
UPDATE customers
SET license_expires_at = created_at + INTERVAL '365 days'
WHERE license_expires_at IS NULL AND created_at IS NOT NULL;
