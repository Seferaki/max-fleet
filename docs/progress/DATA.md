# Прогресс data engineer

Обновляет только data engineer в своей ветке. Чтение backend-кода не требуется. [Задание](../DATA_ENGINEER.md), [модель БД](../DATABASE.md), [план](../IMPLEMENTATION_PLAN.md).

```yaml
status_schema: 1
track: data
owner: Anton
branch: codex/data
current_task: DE-04
current_substep: "DE-01…DE-03 проверены локально; DE-04…DE-07 реализованы, добиваются недостающие тесты гонок/отказов"
last_verified_code_commit: "см. git log codex/data — checkpoint feat(DE-01…DE-07)"
contract_commit: "aa56f0e05b3c2458eee1fe88550d183ece9075af"
migration_head: "0001"
data_ready_for_integration: false
checkpoint_state: WIP
next_step: "DE-04: гонка vehicle.block vs checkout.start; DE-05: сбой БД после записи S3, 3 фото замечания; DE-07: истечение lease старого worker; затем DE-08 нагрузка и backup/restore"
human_required: []
```

| ID | Статус | Commit / проверки | Следующий подшаг / блокер |
|---|---|---|---|
| DE-01 | DONE | FastAPI/SQLAlchemy 2/Alembic/psycopg3/boto3, uv.lock; `deploy/compose.data.yaml` (PostgreSQL 17.6 + SeaweedFS S3 по digest, migrate, data-api, data-worker); /health/live, /health/ready (БД + alembic head + bucket), service/worker auth. Локально: compose up, smoke `scripts/smoke.py` PASS | CI `.github/workflows/data.yml` добавлен, первый прогон на GitHub — после push |
| DE-02 | DONE | Миграция 0001: 21 таблица, 42 FK, 69 CHECK, 20 UNIQUE, partial unique для hold/trip/draft; отложенные циклические FK. empty→head, повтор head, downgrade→upgrade, `alembic check` без расхождений. Seed идемпотентный, bootstrap-admin из секрет-файла. Роли БД: migrator (владелец схемы) и app (только DML, не superuser — DROP отклонён) | — |
| DE-03 | DONE | Все GET контракта; ACL/IDOR: чужой trip/return/asset/issue → 404, не-admin → ACCESS_DENIED; object_key и правильный ответ примера наружу не выходят; ответы валидируются JSON Schema из OpenAPI (`tests/test_contract.py`) | — |
| DE-04 | IN_PROGRESS | hold/math/правила/старт; 20 сотрудников на 1 машину → 1 assignment; 1 сотрудник на 10 машин → 1; TTL vs start; повтор ключа, конфликт тела | Тест гонки vehicle.block vs checkout.start |
| DE-05 | IN_PROGRESS | Фото: 7/8/9, дубликат события/хэша, замена ракурса, неверный формат/MIME, сбой S3 → 503 без занятого слота; stage + issue.create; orphan cleanup не трогает привязанные | Тесты: сбой БД после S3, лимит 3 фото замечания, 10 MiB/25 MP |
| DE-06 | IN_PROGRESS | Возврат, отмена→новый ID, UNSAFE_RETURN, повреждение→needs_review, admin close с missing_data, block/unblock с math proof, employee.grant/access, issue.resolve; двойной complete → один 200 | Тесты vehicle.edit/correct_snapshot/annotate |
| DE-07 | IN_PROGRESS | Inbox: дубликаты, порядок по actor, claim/ack/retry, fencing команд по lease; integration lease/checkpoint CAS; outbox + получатели в доменной транзакции; claim/ack/retry/dead уведомлений | Тест: истёкший lease старого worker после перехвата другим |
| DE-08 | TODO | — | Нагрузка 50 users / 20 rps / 10 мин, 10×5 MiB, EXPLAIN, backup/restore |
| DE-09 | TODO | — | Gate DATA_READY_FOR_INTEGRATION |

## Последний checkpoint

Реализован весь внутренний API v1 (36 маршрутов) в `services/data/`. Проверено локально (Windows 11, Docker Desktop, PostgreSQL 17.6):

- `uv run pytest` — 34 passed (реальный PostgreSQL, S3 — in-memory адаптер в тестах);
- `uv run ruff check .` и `uv run mypy app` — без ошибок;
- `docker compose -f deploy/compose.data.yaml up -d --build` с `SEED_SYNTHETIC=1` — migrate exit 0, data-api healthy;
- `scripts/smoke.py` против живого контура (реальные PostgreSQL + SeaweedFS S3): взятие → 8 фото → поездка → возврат → 8 фото → точка → завершение — PASS;
- runtime-роль БД не может менять схему; анонимный запрос к S3 → 403.

GitHub CI для data ещё не запускался. Готовность к INT не заявляется.

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

Не запускались. Перед DE-09 записать ресурсы стенда, набор данных, p95, error rate, результаты гонок и backup/restore, реальные code/contract SHA. Без секретов и дампов.
