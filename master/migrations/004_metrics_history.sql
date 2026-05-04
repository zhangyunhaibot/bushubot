-- 历史指标快照（每 5 分钟一条，自动清理 30 天前的）
CREATE TABLE IF NOT EXISTS metrics_snapshots (
    id            BIGSERIAL PRIMARY KEY,
    customer_id   INTEGER REFERENCES customers(id),
    mem_used_mb   INTEGER,
    mem_total_mb  INTEGER,
    disk_used_gb  INTEGER,
    disk_total_gb INTEGER,
    load_1m       DECIMAL(6,2),
    cpu_count     INTEGER,
    snapshot_at   TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_metrics_customer_time
    ON metrics_snapshots(customer_id, snapshot_at DESC);
