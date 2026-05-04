-- 通知发送计数 + 上次尝试时间，用于 ACK 模式
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS last_attempt_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_notifications_pending
    ON notifications(customer_id, delivered);
