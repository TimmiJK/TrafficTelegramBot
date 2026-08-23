CREATE TABLE IF NOT EXISTS alert_state (
    key          TEXT PRIMARY KEY,
    period_start BIGINT NOT NULL,
    last_sent_ts BIGINT NOT NULL
);