CREATE TABLE subscriptions (
                               id SERIAL PRIMARY KEY,
                               user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                               status VARCHAR(20) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'expired')),
                               expires_at TIMESTAMP NOT NULL,
                               crypto_tx_id TEXT UNIQUE,
                               created_at TIMESTAMP DEFAULT NOW()
);