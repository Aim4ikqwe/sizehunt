-- Создание таблицы сигналов Bybit
CREATE TABLE IF NOT EXISTS bybit_signals (
    id SERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    symbol VARCHAR(30) NOT NULL,
    category VARCHAR(10) NOT NULL DEFAULT 'linear',
    target_price NUMERIC(20, 8) NOT NULL,
    min_quantity NUMERIC(20, 8) NOT NULL,
    trigger_on_cancel BOOLEAN NOT NULL DEFAULT false,
    trigger_on_eat BOOLEAN NOT NULL DEFAULT false,
    eat_percentage NUMERIC(5, 4) DEFAULT 0.0,
    original_qty NUMERIC(20, 8) NOT NULL,
    last_qty NUMERIC(20, 8) NOT NULL,
    auto_close BOOLEAN NOT NULL DEFAULT false,
    watch_category VARCHAR(10) NOT NULL DEFAULT 'linear',
    close_category VARCHAR(10) NOT NULL DEFAULT 'linear',
    original_side VARCHAR(4) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true
);

-- Создание индексов
CREATE INDEX IF NOT EXISTS idx_bybit_signals_user_active ON bybit_signals (user_id, is_active);
CREATE INDEX IF NOT EXISTS idx_bybit_signals_symbol ON bybit_signals (symbol);
CREATE INDEX IF NOT EXISTS idx_bybit_signals_active ON bybit_signals (is_active);
