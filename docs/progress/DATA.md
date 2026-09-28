# Прогресс data engineer

Обновляет только data engineer в своей ветке. Чтение backend-кода не требуется. [Задание](../DATA_ENGINEER.md), [модель БД](../DATABASE.md), [план](../IMPLEMENTATION_PLAN.md).

```yaml
status_schema: 1
track: data
owner: Anton
branch: codex/data
current_task: DE-09
current_substep: "DE-01…DE-08 выполнены; gate ждёт решений backend по контрактным вопросам 1–5"
last_verified_code_commit: "см. git log codex/data — checkpoint feat(DE-01…DE-07)"
contract_commit: "aa56f0e05b3c2458eee1fe88550d183ece9075af"
migration_head: "0001"
data_ready_for_integration: false
checkpoint_state: WIP
next_step: "Согласовать с backend контрактные вопросы 1–5 (ниже), затем выставить DATA_READY_FOR_INTEGRATION; проверить права docker secrets на Linux"
human_required: []
```

| ID | Статус | Commit / проверки | Следующий подшаг / блокер |
|---|---|---|---|
| DE-01 | DONE | FastAPI/SQLAlchemy 2/Alembic/psycopg3/boto3, uv.lock; `deploy/compose.data.yaml` (PostgreSQL 17.6 + SeaweedFS S3 по digest, migrate, data-api, data-worker); /health/live, /health/ready (БД + alembic head + bucket), service/worker auth. Локально: compose up, smoke `scripts/smoke.py` PASS | CI `.github/workflows/data.yml` добавлен, первый прогон на GitHub — после push |
| DE-02 | DONE | Миграция 0001: 21 таблица, 42 FK, 69 CHECK, 20 UNIQUE, partial unique для hold/trip/draft; отложенные циклические FK. empty→head, повтор head, downgrade→upgrade, `alembic check` без расхождений. Seed идемпотентный, bootstrap-admin из секрет-файла. Роли БД: migrator (владелец схемы) и app (только DML, не superuser — DROP отклонён) | — |
| DE-03 | DONE | Все GET контракта; ACL/IDOR: чужой trip/return/asset/issue → 404, не-admin → ACCESS_DENIED; object_key и правильный ответ примера наружу не выходят; ответы валидируются JSON Schema из OpenAPI (`tests/test_contract.py`) | — |
| DE-04 | DONE | hold/math/правила/старт; 20 сотрудников на 1 машину → 1 assignment; 1 сотрудник на 10 машин → 1; TTL vs start; block vs start (3 прогона: поездка либо отказ, без промежуточно свободной машины); повтор ключа, конфликт тела | — |
| DE-05 | DONE | Фото: 7/8/9, дубликат события/хэша, замена ракурса, неверный формат/MIME, 10 MiB → 413, 30 MP → 415, сбой S3 → 503 без занятого слота, сбой БД после S3 → прежний снимок цел, новый остаётся staged; 4 фото замечания → 400; orphan cleanup не трогает привязанные | — |
| DE-06 | DONE | Возврат, отмена→новый ID, UNSAFE_RETURN, повреждение→needs_review, admin close с missing_data, block/unblock с math proof, employee.grant/access, issue.resolve, vehicle.edit/correct_snapshot (только свободная машина)/annotate; двойной complete → один 200 | — |
| DE-07 | DONE | Inbox: дубликаты, порядок по actor, claim/ack/retry, fencing команд по lease; истёкший lease старого worker не ack-ает и не выполняет команду после перехвата; integration lease/checkpoint CAS; outbox + получатели в доменной транзакции; claim/ack/retry/dead уведомлений | — |
| DE-08 | DONE | `scripts/load.py`: 50 users, 20 rps, 600 с — 12 000 запросов, p50 14.9 мс, p95 34.4 мс, p99 45.4 мс, 5xx 0%; 10×5 MiB параллельно — 10/10 200, max 2.9 с; EXPLAIN — все выборки < 1 мс по индексам; `scripts/backup-restore.sh` — 21 таблица / 3258 строк совпали, 27 объектов SHA-256 совпали, 0 битых ссылок | — |
| DE-09 | IN_PROGRESS | CI data на GitHub зелёный (ruff, mypy, pytest на PostgreSQL, Docker build, secret-scan); cold start `down -v` → `up` проверен | Решения backend по контрактным вопросам 1–5; права docker secrets на Linux |

## Последний checkpoint

Реализован весь внутренний API v1 (36 маршрутов) в `services/data/`. Проверено локально (Windows 11, Docker Desktop, PostgreSQL 17.6):

- `uv run pytest` — 40 passed (реальный PostgreSQL, S3 — in-memory адаптер в тестах);
- `uv run ruff check .` и `uv run mypy app` — без ошибок;
- `docker compose -f deploy/compose.data.yaml up -d --build` с `SEED_SYNTHETIC=1` — migrate exit 0, data-api healthy;
- `scripts/smoke.py` против живого контура (реальные PostgreSQL + SeaweedFS S3): взятие → 8 фото → поездка → возврат → 8 фото → точка → завершение — PASS;
- runtime-роль БД не может менять схему; анонимный запрос к S3 → 403.

GitHub CI `data` зелёный. Готовность к INT не заявляется до решения контрактных вопросов.

Найдено при нагрузке: в контейнере с read-only ФС Starlette не мог буферизовать multipart > 1 MiB во временный файл (400 на фото 5 MiB) — добавлен tmpfs `/tmp` 128 MiB для data-api.

Окружение: на Windows с кириллицей в профиле Docker Desktop не монтирует файлы из `%LOCALAPPDATA%` — секреты кладутся в ASCII-путь через `MAX_FLEET_SECRETS_DIR` (добавлено в `scripts/bootstrap.ps1`). На Linux docker secrets из файлов с правами 0600 другого владельца могут быть недоступны пользователю 10001 контейнера — проверить на INT.

## Контрактные вопросы

Python повторяет поведение Go data-mock там, где mock и текст контракта расходятся, чтобы Go на INT получил знакомые ответы. Нужно решение backend (и при необходимости правка контракта/mock):

1. **`return.complete` → aggregate.** OpenAPI объявляет `Trip`, data-mock возвращает `Return`. Python сейчас возвращает `Return` (как mock).
2. **Версии checkout/return при фото и `inspection.update`.** Описание OpenAPI: «Фото не повышает checkout/return version». Mock повышает версию родителя и при фото, и при `inspection.update`, и при `inspection.confirm_photos`. Python — как mock.
3. **HTTP-статус `RULES_REQUIRED` и `CHALLENGE_EXPIRED`.** Таблица контракта — 422, mock — 409. Python — 422 по контракту (Go-клиент ветвится по коду ошибки, не по статусу).
4. **Не-admin на `/admin/*` (чтение).** Контракт перечисляет `ADMIN_REQUIRED`, mock и Go-тесты ждут `ACCESS_DENIED`. Python: чтение — `ACCESS_DENIED` (как mock), админские команды — `ADMIN_REQUIRED`.
5. **Admin challenge (не реализован в mock).** Python требует в `intent_payload` ровно поля: block — `operation,target_id,expected_version,reason`; unblock — + `review_completed`; access — `…,can_start_trip,reason`; grant — `target_id=null, expected_version=null, max_user_id, display_name`; admin_close — `…,reason`. `challenge.answer` возвращает `challenge_proof_id = challenge.id`; итоговая команда передаёт его как `challenge_id` и проверяет SHA-256 намерения. Нужна сверка с BE-09.
6. **`conversation.save`.** `target_id` = `employee.id` самого actor; при отсутствии состояния текущая версия = 1 (как `conversation_version` в `/state`), первое сохранение даёт версию 2.
7. **`employee.grant` на существующий MAX ID** → 409 `INVALID_STATE` (в контракте «явный конфликт» без кода).
8. **Получатели уведомлений.** Mock — только администраторы. Python — администраторы + водитель при `trip_admin_closed` + сотрудник при `access_changed` (PRODUCT_SPEC §13.6 п.8).
9. **`GET /vehicles/{id}/previous-inspection/photos/{slot}`** есть в OpenAPI, но не в mock; в Python реализован.

## Результаты нагрузки и восстановления

Стенд: Windows 11, Docker Desktop 29.4 (4 vCPU, 12 GiB для VM), PostgreSQL 17.6, SeaweedFS S3, data-api — 1 процесс uvicorn, pool 5+5. Contract SHA `aa56f0e`.

| Проверка | Результат |
|---|---|
| 50 users, 20 rps, 600 с (70% чтений, 30% команд, конкуренция 50 человек за 10 машин) | 12 000 запросов; p50 14.9 / p95 34.4 / p99 45.4 мс; 5xx — 0; 409 — 1 681 (ожидаемые: занятая машина, CAS диалога) |
| 10 загрузок по 5 MiB одновременно | 10/10 × 200; среднее 2.5 с, максимум 2.9 с |
| Гонки | 20 сотрудников → 1 машина: 1 победитель; 1 сотрудник → 10 машин: 1 hold; block vs start: поездка либо отказ; двойной complete: один 200 |
| Backup/restore на новых томах | 21 таблица / 3 258 строк совпали; 27 объектов с совпавшим SHA-256; 0 битых ссылок; alembic head 0001 |

Команды: `scripts/load.py`, `scripts/explain.sql`, `scripts/backup-restore.sh` (каталог `backups/` в .gitignore).
