CREATE TABLE refresh_tokens (
                                id SERIAL PRIMARY KEY,
                                user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                token TEXT NOT NULL UNIQUE,
                                expires_at TIMESTAMP NOT NULL,
                                created_at TIMESTAMP DEFAULT NOW()
);