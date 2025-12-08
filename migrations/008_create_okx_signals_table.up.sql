-- Создание таблицы сигналов OKX
CREATE TABLE IF NOT EXISTS okx_signals (
    id SERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    inst_id VARCHAR(30) NOT NULL,
    inst_type VARCHAR(10) NOT NULL DEFAULT 'SWAP',
    target_price NUMERIC(20, 8) NOT NULL,
    min_quantity NUMERIC(20, 8) NOT NULL,
    trigger_on_cancel BOOLEAN NOT NULL DEFAULT false,
    trigger_on_eat BOOLEAN NOT NULL DEFAULT false,
    eat_percentage NUMERIC(5, 4) DEFAULT 0.0,
    original_qty NUMERIC(20, 8) NOT NULL,
    last_qty NUMERIC(20, 8) NOT NULL,
    auto_close BOOLEAN NOT NULL DEFAULT false,
    original_side VARCHAR(4) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true
);

-- Создание индексов
CREATE INDEX IF NOT EXISTS idx_okx_signals_user_active ON okx_signals (user_id, is_active);
CREATE INDEX IF NOT EXISTS idx_okx_signals_inst_id ON okx_signals (inst_id);
CREATE INDEX IF NOT EXISTS idx_okx_signals_active ON okx_signals (is_active);
