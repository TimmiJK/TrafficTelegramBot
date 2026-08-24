# 🚦 TrafficBot — мониторинг трафика 3x-ui с Telegram-ботом

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://github.com/TimmiJK/TrafficTelegramBot)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-15-4169E1?logo=postgresql&logoColor=white)](https://github.com/postgres/postgres)
[![Docker](https://img.shields.io/badge/Docker-Compose-2496ED?logo=docker&logoColor=white)](https://github.com/docker)
[![Telegram](https://img.shields.io/badge/Telegram-Bot-26A5E4?logo=telegram&logoColor=white)](https://github.com/go-telegram-bot-api/telegram-bot-api)
[![3x-ui](https://img.shields.io/badge/3x--ui-Xray-orange)](https://github.com/MHSanaei/3x-ui)
![CI](https://img.shields.io/badge/CI-GitHub_Actions-2088FF?logo=githubactions&logoColor=white)

Сервис собирает трафик клиентов панели **3x-ui (Xray)**, хранит историю в
**PostgreSQL** и отдаёт отчёты через **Telegram-бота**.

Устройства автоматически группируются по префиксу имени до первого `_`:

```text
user_mob ─┐
user_pc  ─┼──► группа "user"
user_tab ─┘
```

Billing-периоды считаются по датам продления сервера: скользящие окна
по 30 дней **(или от `BILLING_DAY` как fallback)**.

---

## 🔄 Как это работает

```text
┌──────────────┐      poll (read-only)       ┌──────────────┐
│    3x-ui     │ ───────────────────────────►│  TrafficBot  │
│   SQLite     │                             │     Go       │
└──────────────┘                             └──────┬───────┘
                                                    │                                                 
                                                 дельты
                                                    │
                                                    ▼
                                             ┌──────────────┐
                                             │  PostgreSQL  │
                                             └──────┬───────┘
                                                    │
                                              SUM по группам
                                                    │
                                                    ▼
                                             ┌──────────────┐
                         команды / отчёты    │ Telegram Bot │
                              ┌──────────────┤              │
                              │              └──────────────┘
                              ▼
                       пользователь / админ


GitHub:
feature → CI → Pull Request → main → CD
                                      │
                                      ├── SSH
                                      ├── migrate up
                                      ├── docker compose up -d --build
                                      └── ✅ / ❌ Telegram


Watchdog:
GitHub Actions → SSH → проверка сервера/контейнеров → 🚨 Telegram
```

### Основной поток

1. Каждые `POLL_INTERVAL` секунд поллер читает счётчики `up/down` всех клиентов
   из SQLite-базы 3x-ui в режиме **read-only**.
2. Текущие значения сравниваются с последними значениями из `state`.
3. Вычисленные **дельты** записываются в `usage`.
4. Сбросы счётчиков детектируются: при сбросе дельта берётся равной текущим 
   значениям счётчиков (отрицательные дельты не пишутся).
5. `syncGroups` поддерживает справочник `groups`, извлекая префикс до первого `_`.
6. Границы billing-периода — скользящие 30-дневные окна от дат продления сервера
   (`BILLING_START` / `BILLING_EXPIRY`), при их отсутствии — `BILLING_DAY`.
   Открытые периоды («по сейчас») включают текущую секунду.
7. Telegram-бот выполняет команды и строит отчёты по группам.
8. При превышении общего лимита трафика администратору отправляется алерт
   **один раз за billing-период**.

---

## 🧩 Архитектура

```text
                         ┌─────────────────────┐
                         │       3x-ui         │
                         │      SQLite         │
                         └──────────┬──────────┘
                                    │
                              read-only poll
                                    │
                                    ▼
                         ┌─────────────────────┐
                         │     TrafficBot      │
                         │        Go           │
                         │                     │
                         │  • poller           │
                         │  • delta calculator │
                         │  • group sync       │
                         │  • Telegram bot     │
                         │  • alerts           │
                         └───────┬─────┬───────┘
                                 │     │
                         deltas  │     │ commands
                                 │     │ reports
                                 ▼     ▼
                       ┌────────────┐ ┌──────────────┐
                       │ PostgreSQL │ │ Telegram Bot │
                       │            │ │              │
                       │ state      │ │ /usage       │
                       │ usage      │ │ /all         │
                       │ groups     │ │ /myID        │
                       │ bindings   │ │ /bind        │
                       │ alerts     │ │ /unbind      │
                       └────────────┘ └──────────────┘
```

---

## 📊 Что хранится в PostgreSQL

| Таблица | Назначение |
|---|---|
| `state` | Последние счётчики устройств для расчёта дельт |
| `usage` | История потреблённого `up/down` трафика |
| `groups` | Справочник групп пользователей |
| `bindings` | Привязки Telegram chat ID к группам |
| `alert_state` | Состояние отправки алерта за текущий billing period |
| `schema_migrations` | Служебная таблица `golang-migrate` |

Миграции лежат в `migrations/` и выполняются **до запуска новой версии приложения**.

```text
migration 001
     │
     ▼
PostgreSQL
     │
     ├── state
     ├── usage
     ├── groups
     ├── bindings
     └── alert_state
```

---

## 📁 Структура проекта

```text
.
├── main.go                  # приложение: poller + bot + alerts
├── main_test.go             # unit-тесты
├── main_integration_test.go # интеграционные тесты (test PG + fake Telegram)
│
├── migrations/              # SQL-миграции
│
├── Dockerfile               # multi-stage build
├── docker-compose.yml       # PostgreSQL + application
├── Makefile                 # команды проекта
├── .env                     # конфигурация, НЕ коммитится
│
└── .github/
    └── workflows/
        ├── ci.yml           # gofmt / go vet / build
        ├── cd.yml           # автоматический deploy
        └── watchdog.yml     # проверка сервера каждые 5 минут
```

---

## 🛠 Требования

- Docker + Docker Compose
- Go **1.25+** для локальной разработки
- Панель 3x-ui на том же сервере
- Telegram-бот, созданный через `@BotFather`
- PostgreSQL 15+

---

## ⚙️ Конфигурация

Создай `.env`:

```env
BOT_TOKEN=your_bot_token

DB_HOST=localhost
DB_PORT=5432
DB_USER=your_db_user_name
DB_PASSWORD=your_password
DB_NAME=your_db_name

XUI_DB_HOST_PATH=find_path_to_x-ui(generally: etc/path/x-ui/x-ui.db)

DEFAULT_TZ=your_region_timezone
POLL_INTERVAL=600

BILLING_DAY=29
BILLING_START=2007-07-07
BILLING_EXPIRY=2007-08-07

ADMIN_CHAT_ID=123456789
ADMIN_USER_NAME=admin

TRAFFIC_LIMIT_GB=our_limit_of_traffic(if unlim: 99999)
```

Создай **Secrets** для GitHub Actions:

```text
DEPLOY_HOST
DEPLOY_USER
DEPLOY_KEY
TELEGRAM_BOT_TOKEN
TELEGRAM_CHAT_ID
```

### Основные переменные

> ⚠️ `.env` содержит секреты и **не должен коммититься в Git**.

| Переменная | Описание |
|---|---|
| `BOT_TOKEN` | Токен Telegram-бота |
| `DB_USER` | Пользователь PostgreSQL |
| `DB_PASSWORD` | Пароль PostgreSQL |
| `DB_NAME` | Имя базы данных |
| `DB_PORT` | Порт PostgreSQL |
| `XUI_DB_HOST_PATH` | Путь к SQLite базе 3x-ui на хосте |
| `DEFAULT_TZ` | Таймзона отчётов |
| `POLL_INTERVAL` | Интервал опроса 3x-ui, сек. (0/отрицательные игнорируются) |
| `BILLING_START` | Начало текущего billing-периода (из панели хостинга) |
| `BILLING_EXPIRY` | Конец текущего billing-периода включительно (из панели хостинга) |
| `BILLING_DAY` | Fallback: день начала billing period, если даты не заданы |
| `ADMIN_CHAT_ID` | Telegram ID администратора |
| `ADMIN_USER_NAME` | Username администратора |
| `TRAFFIC_LIMIT_GB` | Лимит общего трафика за billing period |

Secrets для GitHub Actions

| Переменная | Описание |
|---|---|
| `DEPLOY_HOST` | IP вашего сервера |
| `DEPLOY_USER` | Никнейм вашего юзера на сервере |
| `DEPLOY_KEY` | Сгенерированный ssh ключ(нейронка в помощь) |
| `TELEGRAM_BOT_TOKEN` | Токен Telegram-бота куда будут преходить уведомления о deploy |
| `TELEGRAM_CHAT_ID` | ID юзера которому будут приходить сообщения от бота |


---

## 🚀 Запуск

### Docker

```bash
make docker-up
```

или:

```bash
docker compose up -d --build
```

Проверить контейнеры:

```bash
docker compose ps
```

Посмотреть логи:

```bash
make docker-logs
```

Остановить:

```bash
make docker-down
```

### Локально

```bash
go test ./...
go run .
```

При запуске приложение:

```text
PostgreSQL + SQLite 3x-ui
          │
          ▼
    первый poll
          │
          ├── baseline
          └── sync groups
          │
          ▼
       poller
          │
          ├── delta calculation
          ├── usage storage
          ├── limit check
          └── Telegram commands
```

---

## 🤖 Команды Telegram-бота

| Команда | Кто | Описание |
|---|---|---|
| `/start` | Все | Справка |
| `/myID` | Все | Узнать свой chat ID |
| `/usage` | Все | Отчёт по своим привязкам |
| `/today` | Все | Трафик за сегодня |
| `/week` | Все | Трафик за текущую неделю |
| `/month` | Все | Трафик за текущий billing month |
| `/yesterday` | Все | Отчёт за вчера |
| `/prevweek` | Все | Предыдущая неделя |
| `/prevmonth` | Все | Предыдущий billing month |
| `/bind <chatID> <имя>` | Админ | Привязать Telegram chat к группе |
| `/unbind <chatID> <имя>` | Админ | Отвязать chat |
| `/all [период]` | Админ | Сводка по всем группам |

Доступные периоды:

```text
today
week
month
yesterday
prevweek
prevmonth
```

---

## 📅 Расчётные периоды

| Период | Границы |
|---|---|
| День | `00:00` → `00:00` следующего дня |
| Неделя | Понедельник `00:00` → следующий понедельник |
| Billing month | Скользящее 30-дневное окно от дат продления сервера |

### 🔁 Скользящий billing (основной режим)

Периоды считаются от дат из панели хостинга:
- BILLING_START=2026-07-30    # начало текущего периода
- BILLING_EXPIRY=2026-08-29   # конец текущего периода (включительно)

Правило продления:
```text
expiry_next = expiry + 30 дней
start_next  = expiry + 1 день
7/30  ──────────────  8/29    текущий период
8/30  ──────────────  9/28    следующий («до 28-го»)
9/29  ──────────────  10/28   и так далее
```

Если провайдер вашего сервера продлевает ровно на 30 дней, достаточно один раз 
задать две даты — все будущие окна вычисляются автоматически,
обновлять `.env` при каждом продлении не нужно.

Индекс текущего периода определяется по `now`:
- now <= BILLING_EXPIRY        ► текущий период [BILLING_START, BILLING_EXPIRY]
- now >  BILLING_EXPIRY        ► окно сдвигается на 30 дней за каждое продление

### 📆 Fallback: BILLING_DAY

Если `BILLING_EXPIRY` не задан, используется календарная схема от `BILLING_DAY`:

```text
BILLING_DAY = 29
Февраль:
29-е число отсутствует
       │
       ▼
период автоматически клампится до последнего дня месяца
```

Время хранится как Unix timestamp, а границы периодов рассчитываются
в таймзоне контейнера.

---

## 🔗 Группировка устройств

Имя устройства разбирается по первому символу `_`.

```text
user_mob      ─┐
user_pc       ─┤
user_tab      ─┼──► user
user_phone    ─┤
user_laptop   ─┘

work_pc      ─┐
work_phone   ─┼──► work
work_tab     ─┘
```

В PostgreSQL используется:

```sql
split_part(user_key, '_', 1)
```

Например:

```text
split_part('user_mob_123', '_', 1)
                    │
                    ▼
                  "user"
```

---

## 🔐 Привязки пользователей

Связь Telegram chat → группа защищается внешним ключом:

```text
Telegram chat
      │
      │ /bind
      ▼
   bindings
      │
      │ FK
      ▼
    groups
      │
      ▼
  known group
```

Неизвестную группу привязать нельзя:

```text
/bind 123456 random
              │
              ▼
       ❌ группа не найдена
```

---

## 🔄 Расчёт дельт

3x-ui предоставляет накопительные счётчики:

```text
up:   100 GB
down: 200 GB
```

Следующий poll:

```text
up:   105 GB
down: 230 GB
```

TrafficBot сохраняет:

```text
Δup   = 105 - 100 = 5 GB
Δdown = 230 - 200 = 30 GB
```

В `usage` записывается именно дельта:

```text
┌──────────────┬───────┬────────┐
│ user_key     │ up    │ down   │
├──────────────┼───────┼────────┤
│ user_mob     │ 5 GB  │ 30 GB  │
└──────────────┴───────┴────────┘
```

Если счётчик был сброшен:

```text
было:    500 GB
стало:     20 GB
```

TrafficBot определяет reset и не записывает отрицательную дельту.

---

## 📈 Агрегация отчётов

Для группировки устройств используется префикс:

```sql
split_part(user_key, '_', 1)
```

Отчёт по группам выглядит концептуально так:

```text
┌──────────┬──────────┬──────────┬──────────┐
│ GROUP    │ UP       │ DOWN     │ TOTAL    │
├──────────┼──────────┼──────────┼──────────┤
│ user     │ 12.4 GB  │ 31.7 GB  │ 44.1 GB  │
│ work     │  5.2 GB  │  8.8 GB  │ 14.0 GB  │
│ home     │  1.1 GB  │  3.2 GB  │  4.3 GB  │
└──────────┴──────────┴──────────┴──────────┘
```

Группы сортируются по:

```text
TOTAL = UP + DOWN
```

от большего к меньшему.

---

## 🚨 Алерты

При превышении общего лимита:

```text
Traffic
   │
   ▼
UP + DOWN
   │
   ▼
TRAFFIC_LIMIT_GB
   │
   ├── OK ───────► ничего
   │
   └── exceeded ─► Telegram alert 🚨
                         │
                         ▼
                    ADMIN_CHAT_ID
```

Алерт отправляется **один раз за billing period**.

Состояние сохраняется в `alert_state`, поэтому рестарт приложения
не приводит к повторной отправке того же алерта.

---

## 🔁 CI/CD

```text
    Developer
       │
       │ git push feature
       ▼
┌──────────────┐
│ GitHub       │
│ Actions      │
└──────┬───────┘
       │
       ├── gofmt
       ├── go vet
       ├── go test
       └── go build
       │
       ▼
    Pull Request
       │
       ▼
      main
       │
       │ merge
       ▼
┌──────────────┐
│      CD      │
└──────┬───────┘
       │
       ├── SSH
       ├── git pull
       ├── wait PostgreSQL
       ├── migrate up
       ├── docker compose build
       ├── docker compose up -d
       ├── docker image prune -f
       └── Telegram notification
```

### CI

`ci.yml` запускается при push в `feature` и Pull Request в `main`.

Проверяет:

```text
gofmt
  │
  ▼
go vet
  │
  ▼
go test
  │
  ▼
go build
```

### CD

`cd.yml` запускается после merge в `main`.

Последовательность:

```text
SSH
 ↓
git pull
 ↓
PostgreSQL ready?
 ↓
migrate up
 ↓
docker compose up -d --build
 ↓
docker image prune -f
 ↓
Telegram ✅ / ❌
```

`concurrency` предотвращает одновременное выполнение нескольких deploy.

---

## 👀 Watchdog

Отдельный GitHub Actions workflow запускается каждые 5 минут:

```text
GitHub Actions
      │
      ▼
     SSH
      │
      ▼
┌───────────────┐
│ Server        │
│               │
│ containers    │
│ restart-loop  │
│ availability  │
└───────┬───────┘
        │
        ├── OK ────────► nothing
        │
        └── ERROR ─────► 🚨 Telegram
```

Watchdog проверяет:

- доступность сервера;
- состояние контейнеров;
- restart-loop;
- ошибки выполнения проверки.

Положительный `✅` отправляется только при ручном запуске workflow,
чтобы не создавать Telegram-спам каждые 5 минут.

---

## 📝 Логи

Приложение использует `slog` и пишет JSON-логи:

```text
stdout
  │
  ├── Docker logs
  │
  └── logs/traffic-bot.log
```

Для файловых логов используется `lumberjack`:

```text
100 MB
3 backups
28 days
gzip compression
```

Docker `json-file` также использует ротацию:

```text
50 MB × 3
```

Посмотреть логи:

```bash
docker compose logs -f app
```

или:

```bash
make docker-logs
```

---

## 💾 Бэкапы PostgreSQL

Создать backup:

```bash
docker exec -t traffic_bot-postgres \
  pg_dump -U "$DB_USER" "$DB_NAME" \
  > backup_$(date +%F).sql
```

Восстановить:

```bash
cat backup.sql | \
docker exec -i traffic_bot-postgres \
psql -U "$DB_USER" "$DB_NAME"
```

---

## 🧪 Тесты

Unit — без внешних зависимостей:

```bash
go test ./... -v
```

Интеграционные — против настоящего PostgreSQL; без переменной
окружения честно пропускаются:

```bash
docker run -d --name test-pg \
  -e POSTGRES_USER=test -e POSTGRES_PASSWORD=test \
  -e POSTGRES_DB=traffic_test -p 5433:5432 \
  postgres:15-alpine
TEST_DATABASE_URL="postgres://test:test@localhost:5433/traffic_test?sslmode=disable" \
  go test ./... -v
```

В CI интеграционные тесты идут автоматически в service postgres.

Основные проверяемые части:

- форматирование байт (включая отрицательные и max int64);
- календарная логика;
- billing day и кламп коротких месяцев;
- високосный февраль;
- скользящие 30-дневные billing-периода;
- границы периодов «секунда в секунду»;
- стыковка окон продления;
- понедельник как начало недели;
- парсинг environment variables (0/отрицательные → default);
- права администратора (включая групповые chat ID);
- карта периодов;
- state/usage/groups/bindings/FK против реальной БД;
- pollTraffic end-to-end (baseline → дельты → reset);
- limit alert: отправка, дедупликация, «ниже лимита» (fake Telegram API).

---

## 🌐 Обновление на сервере

Обычный workflow:

```text
feature
   │
   │ git push
   ▼
  CI ✅
   │
   ▼
Pull Request
   │
   ▼
 merge → main
   │
   ▼
  CD 🚀
   │
   ├── migrate up
   ├── build
   ├── deploy
   └── Telegram notification
```

---

## 📌 Основные технологии

```text
Go
 │
 ├── PostgreSQL
 ├── SQLite
 ├── Telegram Bot API
 ├── Docker
 ├── Docker Compose
 ├── golang-migrate
 ├── GitHub Actions
 └── Xray / 3x-ui
```

---

## 📜 License

Проект предназначен для личного использования и мониторинга собственного
3x-ui/Xray сервера.
