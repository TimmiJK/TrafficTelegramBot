CREATE TABLE IF NOT EXISTS groups (
    group_key TEXT PRIMARY KEY
);

INSERT INTO groups (group_key)
SELECT DISTINCT split_part(user_key, '_', 1)
FROM clients
ON CONFLICT (group_key) DO NOTHING;

UPDATE bindings
SET user_key = split_part(user_key, '_', 1);

ALTER TABLE bindings DROP CONSTRAINT IF EXISTS fk_bindings_client;

ALTER TABLE bindings
ADD CONSTRAINT fk_bindings_group
FOREIGN KEY (user_key) REFERENCES groups (group_key);