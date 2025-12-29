-- 014_enable_rls.down.sql

DROP POLICY IF EXISTS user_self_policy ON users;
DROP POLICY IF EXISTS signal_owner_policy ON signals;
DROP POLICY IF EXISTS binance_keys_owner_policy ON binance_keys;
DROP POLICY IF EXISTS okx_keys_owner_policy ON okx_keys;
DROP POLICY IF EXISTS bybit_keys_owner_policy ON bybit_keys;
DROP POLICY IF EXISTS subscriptions_owner_policy ON subscriptions;
DROP POLICY IF EXISTS proxy_configs_owner_policy ON proxy_configs;
DROP POLICY IF EXISTS promo_codes_admin_policy ON promo_codes;

DROP FUNCTION IF EXISTS get_app_user_id();

ALTER TABLE users DISABLE ROW LEVEL SECURITY;
ALTER TABLE signals DISABLE ROW LEVEL SECURITY;
ALTER TABLE binance_keys DISABLE ROW LEVEL SECURITY;
ALTER TABLE okx_keys DISABLE ROW LEVEL SECURITY;
ALTER TABLE bybit_keys DISABLE ROW LEVEL SECURITY;
ALTER TABLE subscriptions DISABLE ROW LEVEL SECURITY;
ALTER TABLE promo_codes DISABLE ROW LEVEL SECURITY;
ALTER TABLE proxy_configs DISABLE ROW LEVEL SECURITY;
