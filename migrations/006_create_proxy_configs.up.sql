-- +migrate Up
CREATE TABLE proxy_configs (
                               id SERIAL PRIMARY KEY,
                               user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                               ss_addr TEXT NOT NULL,
                               ss_port INTEGER NOT NULL CHECK (ss_port BETWEEN 1 AND 65535),
                               ss_method TEXT NOT NULL,
                               ss_password TEXT NOT NULL,
                               local_port INTEGER NOT NULL CHECK (local_port BETWEEN 1024 AND 65535),
                               status TEXT NOT NULL DEFAULT 'stopped' CHECK (status IN ('running', 'stopped', 'deleted', 'error')),
                               created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                               updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_proxy_configs_user_id ON proxy_configs(user_id);
CREATE INDEX idx_proxy_configs_status ON proxy_configs(status);