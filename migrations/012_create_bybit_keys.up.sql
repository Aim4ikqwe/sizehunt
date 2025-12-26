CREATE TABLE IF NOT EXISTS bybit_keys (
    user_id BIGINT PRIMARY KEY REFERENCES users(id),
    api_key TEXT NOT NULL,
    secret_key TEXT NOT NULL
);
