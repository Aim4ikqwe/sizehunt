# SizeHunt - Торговый ассистент для криптовалютных бирж

SizeHunt - это мощное backend-приложение, разработанное для автоматизации торговли на криптовалютных биржах Binance и OKX. Приложение предоставляет API для управления торговыми сигналами, ключами API, подписками и прокси-серверами с поддержкой Shadowsocks.

## Особенности

- **Поддержка нескольких бирж**: Binance и OKX
- **Управление торговыми сигналами**: Создание, отслеживание и автоматическое исполнение сигналов
- **Безопасное хранение API-ключей**: Шифрование ключей с использованием AES-256
- **Прокси-поддержка**: Интеграция с Shadowsocks для обхода ограничений доступа
- **WebSocket-соединения**: Реальное время получения рыночных данных и обновлений позиций
- **Система подписок**: Подписки с поддержкой промокодов
- **Метрики**: Интеграция с Prometheus для мониторинга производительности
- **JWT-аутентификация**: Безопасная аутентификация пользователей
- **Docker-контейнеризация**: Легковесное развертывание

## Технологии

- **Язык программирования**: Go (Golang) 1.25
- **Веб-фреймворк**: chi
- **База данных**: PostgreSQL
- **Контейнеризация**: Docker, Docker Compose
- **Миграции**: migrate/migrate
- **Библиотеки**:
  - github.com/go-chi/chi/v5
  - github.com/go-chi/cors
  - github.com/golang-jwt/jwt/v5
 - github.com/lib/pq
  - github.com/adshao/go-binance/v2
  - github.com/docker
  - golang.org/x/crypto
  - github.com/prometheus/client_golang

## Архитектура

Проект использует Clean Architecture с четким разделением на слои:

- **cmd**: Точка входа в приложение
- **internal/binance**: Логика работы с Binance API
- **internal/okx**: Логика работы с OKX API
- **internal/user**: Управление пользователями
- **internal/token**: Управление токенами
- **internal/subscription**: Система подписок
- **internal/promocode**: Промокоды
- **internal/proxy**: Управление прокси-серверами
- **internal/config**: Конфигурация приложения
- **pkg**: Общие утилиты (база данных, хеширование)

## Установка и запуск

### Требования

- Go 1.25+
- Docker и Docker Compose
- PostgreSQL (опционально, используется в Docker)

### Локальный запуск

1. Клонируйте репозиторий:
   ```bash
   git clone (https://github.com/Aim4ikqwe/sizehunt)
   cd sizehunt
   ```

2. Установите зависимости:
   ```bash
   go mod tidy
   ```

3. Настройте переменные окружения в файле `.env`:
   ```env
   DATABASE_URL=postgres://sizehunt:sizehunt@localhost:543/sizehunt?sslmode=disable
   JWT_SECRET=supersecretkey12345678901234567890123456
   ENCRYPTION_SECRET=supersecretkey123456789012345678
   METRICS_USERNAME=metrics
   METRICS_PASSWORD=metrics
   ```

4. Запустите приложение с помощью Docker Compose:
   ```bash
   docker-compose up -d
   ```

5. Запустите миграции:
   ```bash
   docker run --network=container:sizehunt_pg migrate/migrate -path=migrations -database="postgres://sizehunt:sizehunt@localhost:543/sizehunt?sslmode=disable" up
   ```

6. Запустите сервер:
   ```bash
   go run cmd/server/main.go
   ```

### Сборка Docker-образа

```bash
docker build -t sizehunt .
```

## API Эндпоинты

### Публичные эндпоинты

- `POST /auth/register` - Регистрация пользователя
- `POST /auth/login` - Вход пользователя
- `POST /auth/refresh` - Обновление токена
- `GET /health` - Проверка состояния сервера
- `GET /health/network` - Проверка сетевого подключения

### Защищенные эндпоинты (требуют JWT токена)

#### Управление прокси
- `POST /api/proxy` - Сохранить конфигурацию прокси
- `DELETE /api/proxy` - Удалить конфигурацию прокси
- `GET /api/proxy/status` - Получить статус прокси

#### Binance
- `GET /api/binance/book` - Получить стакан ордеров
- `GET /api/binance/order-at-price` - Получить ордера по цене
- `POST /api/binance/keys` - Сохранить API-ключи
- `DELETE /api/binance/keys` - Удалить API-ключи
- `POST /api/binance/signal` - Создать торговый сигнал
- `GET /api/binance/signals` - Получить торговые сигналы
- `DELETE /api/binance/signals/{id}` - Удалить торговый сигнал
- `GET /api/binance/keys/status` - Получить статус ключей

#### OKX
- `GET /api/okx/book` - Получить стакан ордеров
- `GET /api/okx/order-at-price` - Получить ордера по цене
- `POST /api/okx/keys` - Сохранить API-ключи
- `DELETE /api/okx/keys` - Удалить API-ключи
- `POST /api/okx/signal` - Создать торговый сигнал
- `GET /api/okx/signals` - Получить торговые сигналы
- `DELETE /api/okx/signals/{id}` - Удалить торговый сигнал
- `GET /api/okx/keys/status` - Получить статус ключей

#### Подписки и промокоды
- `POST /api/payment/create` - Создать платеж
- `POST /api/payment/webhook` - Вебхук оплаты
- `POST /api/promocode` - Создать промокод
- `POST /api/promocode/apply` - Применить промокод
- `GET /api/promocodes` - Получить все промокоды
- `GET /api/promocode/{id}` - Получить промокод
- `DELETE /api/promocode/{id}` - Удалить промокод

## Безопасность

- API-ключи пользователей хранятся в зашифрованном виде с использованием AES-256
- JWT-токены с refresh/accessToken системой
- CORS-политика для ограничения доступа
- Rate limiting для защиты от DDoS-атак
- Шифрование паролей в базе данных

## Прокси-функциональность

Приложение поддерживает интеграцию с Shadowsocks через Docker-контейнеры:

- Автоматическое создание и запуск прокси-контейнеров для пользователей с активными сигналами
- Проверка подключения к биржам через прокси
- Мониторинг состояния контейнеров
- Автоматическая остановка прокси при отсутствии активных сигналов

## Метрики

Приложение предоставляет метрики для мониторинга через Prometheus:

- `/metrics` - Эндпоинт для сбора метрик (требует аутентификации)
- Количество HTTP-запросов
- Длительность HTTP-запросов
- Количество активных сигналов
- Количество запущенных прокси-контейнеров
- Количество вызовов API Binance
- Обновления позиций

## Миграции базы данных

Проект включает систему миграций для управления структурой базы данных:

- 001_init: Инициализация базы данных
- 002_create_binance_keys: Таблица для Binance ключей
- 003_create_refresh_tokens: Таблица для refresh токенов
- 004_create_subscriptions: Таблица для подписок
- 005_create_signals_table: Таблица для сигналов
- 006_create_proxy_configs: Таблица для конфигураций прокси
- 007_create_okx_keys: Таблица для OKX ключей
- 008_create_okx_signals_table: Таблица для OKX сигналов
- 009_add_close_inst_fields: Добавление полей для закрытия позиций
- 010_add_is_admin_to_users: Добавление поля администратора
- 011_create_promo_codes_table: Таблица для промокодов

## Фоновые задачи

- Мониторинг WebSocket-соединений
- Отслеживание активных сигналов
- Управление прокси-контейнерами
- Проверка состояния биржевых подключений

## Автор
tg:@aim4ik1
Разработано как часть проекта SizeHunt.
