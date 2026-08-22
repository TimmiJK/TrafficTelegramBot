CREATE TABLE IF NOT EXISTS clients (
    user_key TEXT PRIMARY KEY
);

INSERT INTO clients (user_key)
SELECT DISTINCT user_key FROM state
ON CONFLICT (user_key) DO NOTHING;