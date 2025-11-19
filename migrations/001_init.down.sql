-- Удаляем логи сначала (из-за внешнего ключа)
DROP TABLE IF EXISTS logs;

-- Затем — пользователей
DROP TABLE IF EXISTS users;