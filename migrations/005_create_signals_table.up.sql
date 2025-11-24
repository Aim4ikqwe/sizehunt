-- Создание таблицы сигналов
CREATE TABLE IF NOT EXISTS signals (
                                       id SERIAL PRIMARY KEY,
                                       user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    symbol VARCHAR(20) NOT NULL,
    target_price NUMERIC(20, 8) NOT NULL,
    min_quantity NUMERIC(20, 8) NOT NULL,
    trigger_on_cancel BOOLEAN NOT NULL DEFAULT false,
    trigger_on_eat BOOLEAN NOT NULL DEFAULT false,
    eat_percentage NUMERIC(5, 4) DEFAULT 0.0,
    original_qty NUMERIC(20, 8) NOT NULL,
    last_qty NUMERIC(20, 8) NOT NULL,
    auto_close BOOLEAN NOT NULL DEFAULT false,
    close_market VARCHAR(10) NOT NULL DEFAULT 'futures',
    watch_market VARCHAR(10) NOT NULL DEFAULT 'futures',
    original_side VARCHAR(4) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL,
                                                                                        is_active BOOLEAN NOT NULL DEFAULT true
                                                                                        );

-- Создание индексов отдельно
CREATE INDEX IF NOT EXISTS idx_signals_user_active ON signals (user_id, is_active);
CREATE INDEX IF NOT EXISTS idx_signals_symbol ON signals (symbol);
CREATE INDEX IF NOT EXISTS idx_signals_active ON signals (is_active);