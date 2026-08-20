CREATE TABLE IF NOT EXISTS state (
    user_key  TEXT PRIMARY KEY,
    last_up   BIGINT NOT NULL,
    last_down BIGINT NOT NULL,
    last_ts   BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS usage (
    id       BIGSERIAL PRIMARY KEY,
    user_key TEXT   NOT NULL,
    ts       BIGINT NOT NULL,
    up       BIGINT NOT NULL,
    down     BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS bindings (
    tg_chat_id BIGINT NOT NULL,
    user_key   TEXT NOT NULL,
    tz         TEXT NOT NULL DEFAULT 'UTC',
    PRIMARY KEY (tg_chat_id, user_key)
);

CREATE INDEX IF NOT EXISTS idx_usage_user_ts
    ON usage (user_key, ts);

CREATE INDEX IF NOT EXISTS idx_usage_ts
    ON usage (ts);