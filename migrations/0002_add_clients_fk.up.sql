CREATE TABLE IF NOT EXISTS clients (
    user_key TEXT PRIMARY KEY
);

INSERT INTO clients (user_key)
SELECT user_key FROM state
ON CONFLICT (user_key) DO NOTHING;

ALTER TABLE bindings
ADD CONSTRAINT fk_bindings_client
FOREIGN KEY (user_key) REFERENCES clients (user_key);