-- Добавление поля is_admin к таблице пользователей
ALTER TABLE users ADD COLUMN is_admin BOOLEAN DEFAULT FALSE;
