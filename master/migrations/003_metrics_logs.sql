-- 给 customers 加资源指标字段（每次心跳更新最新值，不保留历史）
ALTER TABLE customers ADD COLUMN IF NOT EXISTS mem_used_mb   INTEGER;
ALTER TABLE customers ADD COLUMN IF NOT EXISTS mem_total_mb  INTEGER;
ALTER TABLE customers ADD COLUMN IF NOT EXISTS disk_used_gb  INTEGER;
ALTER TABLE customers ADD COLUMN IF NOT EXISTS disk_total_gb INTEGER;
ALTER TABLE customers ADD COLUMN IF NOT EXISTS load_1m       DECIMAL(6,2);
ALTER TABLE customers ADD COLUMN IF NOT EXISTS cpu_count     INTEGER;
ALTER TABLE customers ADD COLUMN IF NOT EXISTS uptime_seconds BIGINT;

-- 拉日志结果（保留最近 N 份）
CREATE TABLE IF NOT EXISTS agent_logs (
    id           SERIAL PRIMARY KEY,
    customer_id  INTEGER REFERENCES customers(id),
    service      VARCHAR(64),
    content      TEXT NOT NULL,
    bytes        INTEGER,
    received_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_agent_logs_customer ON agent_logs(customer_id, received_at DESC);
