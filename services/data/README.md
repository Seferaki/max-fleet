# data-api — Python-сервис MAX Fleet

Владеет бизнес-правилами, PostgreSQL, миграциями, фотографиями (S3) и очередями inbox/outbox. Go обращается только к HTTP API контракта v1 (`contracts/data-api.openapi.yaml`); SQL в Go нет.

## Структура

| Путь | Что внутри |
|---|---|
| `app/main.py` | FastAPI-маршруты контракта: чтения, `/commands`, фото, worker-очереди, health |
| `app/api/` | Service/worker auth, строгие схемы payload (`extra=forbid`, null ≠ пропуск) |
| `app/domain/commands.py` | 24 бизнес-команды; блокировки в порядке employee → vehicle → assignment → attempt → trip → return → inspection |
| `app/domain/executor.py` | Идемпотентность: результат пишется в той же транзакции, что домен, audit и outbox |
| `app/domain/photos.py` | staged-запись → S3 → привязка к ракурсу; проверка формата, 10 MiB / 25 MP, SHA-256 |
| `app/domain/queues.py` | Inbox (порядок по actor, lease + fencing), lease poller-а, доставка уведомлений |
| `app/worker.py` | Истечение hold, удаление неподвязанных staged-фото, очистка idempotency-кэша |
| `app/models.py`, `migrations/` | 21 таблица `docs/DATABASE.md`; схема только через Alembic |
| `app/seed.py` | Синтетический seed (id как в Go data-mock) и bootstrap администратора |

## Запуск

```bash
# секреты: scripts/bootstrap.sh или scripts/bootstrap.ps1 (при кириллице в профиле Windows — MAX_FLEET_SECRETS_DIR=C:\MAXFleetSecrets)
MAX_FLEET_SECRETS_DIR=<каталог секретов> SEED_SYNTHETIC=1 docker compose -f deploy/compose.data.yaml up -d --build
MAX_FLEET_SECRETS_DIR=<каталог секретов> uv run python services/data/scripts/smoke.py
```

data-api слушает `127.0.0.1:18000` (только loopback). Роли БД: `maxfleet_migrator` — владелец схемы, `maxfleet_app` — только DML.

## Проверки

```bash
cd services/data
uv sync
uv run ruff check . && uv run mypy app
TEST_DATABASE_URL=postgresql+psycopg://postgres:<пароль>@127.0.0.1:55432/postgres uv run pytest
```

Тесты создают отдельную базу `maxfleet_test` на указанном сервере PostgreSQL (не SQLite) и проверяют ответы по JSON Schema из OpenAPI.
