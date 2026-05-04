-- 广播历史表：记录每一次广播的元数据
CREATE TABLE IF NOT EXISTS broadcasts (
    id           SERIAL PRIMARY KEY,
    template     VARCHAR(64) NOT NULL,        -- maintenance / maintenance_done / version_preview / upgrade_failed / custom / auto_alert
    title        VARCHAR(200),                -- 广播标题（首行用）
    content      TEXT NOT NULL,               -- 完整内容
    target_count INTEGER NOT NULL DEFAULT 0,  -- 目标客户数（启用客户数）
    sent_by      VARCHAR(32) DEFAULT 'admin', -- admin / system_alerter
    note         TEXT,
    created_at   TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_broadcasts_created ON broadcasts(created_at DESC);

-- 给 agent_events 加 alerted 标记，告警过的不再重复告警
ALTER TABLE agent_events ADD COLUMN IF NOT EXISTS alerted BOOLEAN DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS idx_agent_events_alerting ON agent_events(event_type, alerted, created_at);
