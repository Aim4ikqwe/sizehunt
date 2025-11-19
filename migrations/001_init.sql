-- Создание таблицы пользователей
CREATE TABLE IF NOT EXISTS users (
                                     id SERIAL PRIMARY KEY,
                                     email TEXT UNIQUE NOT NULL,
                                     password TEXT NOT NULL,
                                     created_at TIMESTAMP DEFAULT NOW()
    );

-- Логи (на будущее)
CREATE TABLE IF NOT EXISTS logs (
                                    id SERIAL PRIMARY KEY,
                                    user_id INT REFERENCES users(id),
    action TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT NOW()
    );
