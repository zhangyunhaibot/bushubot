CREATE TABLE IF NOT EXISTS customers (
    id              SERIAL PRIMARY KEY,
    name            VARCHAR(100) NOT NULL,
    tg_user_id      BIGINT NOT NULL,
    bot_token       VARCHAR(100) NOT NULL,
    license_key     VARCHAR(64) UNIQUE NOT NULL,
    server_ip       VARCHAR(45),
    current_version VARCHAR(32),
    agent_version   VARCHAR(32),                  -- agent 自报的版本
    last_heartbeat_at TIMESTAMPTZ,
    enabled         BOOLEAN DEFAULT TRUE,
    note            TEXT,
    -- 预留字段（后期做云 API 自动开服时启用）
    cloud_provider  VARCHAR(32),
    cloud_account_ref VARCHAR(64),
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_customers_license ON customers(license_key);

CREATE TABLE IF NOT EXISTS releases (
    id           SERIAL PRIMARY KEY,
    version      VARCHAR(32) UNIQUE NOT NULL,
    notes        TEXT,
    is_latest    BOOLEAN DEFAULT FALSE,
    published_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS agent_events (
    id            SERIAL PRIMARY KEY,
    customer_id   INTEGER REFERENCES customers(id),
    event_type    VARCHAR(32),
    version       VARCHAR(32),
    error_message TEXT,
    created_at    TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS notifications (
    id           SERIAL PRIMARY KEY,
    customer_id  INTEGER REFERENCES customers(id),
    type         VARCHAR(32),
    version      VARCHAR(32),
    message      TEXT,
    delivered    BOOLEAN DEFAULT FALSE,
    created_at   TIMESTAMPTZ DEFAULT NOW()
);

-- 全局配置表：master 控制台可改，agent 心跳时拿到
CREATE TABLE IF NOT EXISTS settings (
    key        VARCHAR(64) PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 初始化默认设置（用 ON CONFLICT 防止重复执行报错）
INSERT INTO settings (key, value) VALUES
    ('latest_agent_version',          ''),  -- 例如 agent-v0.2
    ('min_supported_agent_version',   ''),  -- 例如 agent-v0.1，留空表示不强制
    ('agent_release_repo',            'zhangyunhaibot/bushubot')
ON CONFLICT (key) DO NOTHING;
