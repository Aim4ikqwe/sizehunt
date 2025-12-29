-- 014_enable_rls.up.sql

-- Enable RLS on tables
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE signals ENABLE ROW LEVEL SECURITY;
ALTER TABLE binance_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE okx_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE bybit_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE promo_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE proxy_configs ENABLE ROW LEVEL SECURITY;

-- Helper to get current user ID from session
-- Use current_setting('app.user_id', true) to avoid error if not set
CREATE OR REPLACE FUNCTION get_app_user_id() RETURNS BIGINT AS $$
    SELECT NULLIF(current_setting('app.user_id', true), '')::BIGINT;
$$ LANGUAGE SQL STABLE;

-- Policies for users (can only see/edit own account)
CREATE POLICY user_self_policy ON users
    USING (id = get_app_user_id());

-- Policies for signals
CREATE POLICY signal_owner_policy ON signals
    USING (user_id = get_app_user_id());

-- Policies for exchange keys
CREATE POLICY binance_keys_owner_policy ON binance_keys
    USING (user_id = get_app_user_id());

CREATE POLICY okx_keys_owner_policy ON okx_keys
    USING (user_id = get_app_user_id());

CREATE POLICY bybit_keys_owner_policy ON bybit_keys
    USING (user_id = get_app_user_id());

-- Policies for subscriptions
CREATE POLICY subscriptions_owner_policy ON subscriptions
    USING (user_id = get_app_user_id());

-- Policies for proxy_configs
CREATE POLICY proxy_configs_owner_policy ON proxy_configs
    USING (user_id = get_app_user_id());

-- Policies for promo_codes (Admins can see all, users can't see list)
-- We need to check is_admin from users table
CREATE POLICY promo_codes_admin_policy ON promo_codes
    USING (
        EXISTS (
            SELECT 1 FROM users 
            WHERE id = (current_setting('app.user_id', true))::BIGINT 
            AND is_admin = true
        )
    );
