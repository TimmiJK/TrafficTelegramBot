ALTER TABLE bindings DROP CONSTRAINT IF EXISTS fk_bindings_group;
ALTER TABLE bindings
    ADD CONSTRAINT fk_bindings_client
    FOREIGN KEY (user_key) REFERENCES clients (user_key);
DROP TABLE IF EXISTS groups;