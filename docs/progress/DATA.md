# Прогресс data engineer

История исходной ветки `codex/data` сохранена на `9eb2211b29e48fce8a6afc410bc986fa98a4988e`. Ниже зафиксирован аудит интегратора в `codex/integration`: Python-код v1.13 и `issue.resolve` синхронизированы с OpenAPI; живой DE-контур и регрессии проверены. Исходная ветка data не переписывалась. [Задание](../DATA_ENGINEER.md), [модель БД](../DATABASE.md), [план](../IMPLEMENTATION_PLAN.md).

```yaml
status_schema: 1
track: data-integration-audit
owner: A
branch: codex/integration
current_task: INT-03
current_substep: "INT-03 recovery test e6888015088d4e4feff391efaf3af17d3d7e4452 опубликован в checkpoint 6b8f033255fd8229020987883d09e71fd3d70370. Go worker reconstruction на 4/8, Python durable state/replay проверены. Next: post-commit response-loss idempotency test"
last_verified_code_commit: "e6888015088d4e4feff391efaf3af17d3d7e4452"
contract_commit: "caa134ddffcc0edd501851ae82f12020c992e75a"
migration_head: "0002"
data_ready_for_integration: true
checkpoint_state: WIP
next_step: "Следующий INT-03 substep: проверить повтор return.complete с тем же idempotency key после synthetic lost response after commit; затем сервисные restart/concurrency. Code `e6888015088d4e4feff391efaf3af17d3d7e4452` + checkpoint `6b8f033255fd8229020987883d09e71fd3d70370` опубликованы. QA NOT RUN, Linux host-secret permissions не проверены"
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
| DE-09 | READY FOR INT (audit в integration) | Code `5ea435ecaa11f3364a112f476b9cb3f88b223be9`, smoke version fix `36c18f2b7e143e12c036e280a10c21fd8415cbda`, integration contract-sync fix `09a11b3689740bd5fce26a10d4f417587db6472e`; `ruff`, mypy, PostgreSQL 17.6 pytest: 43 passed; Data Compose healthy; live Go→Python/PostgreSQL/S3 scenario published in `84ad9b51d732787a8e6057577ed7048f209a4015`, status `1766bccdb9ec4f8ec46c79c95b4b2d668670a57e`. Исходный `codex/data` оставлен без переписывания | DE-09 functional gate passed; QA and Linux host-secret permissions not checked |

## Последний checkpoint

Реализован внутренний API v1.13 (36 маршрутов) в `services/data/`. Проверено локально (Windows 11, Docker Desktop, PostgreSQL 17.6):

- `TEST_DATABASE_URL=<отдельный disposable PostgreSQL 17.6> uv run pytest -q` — 43 passed (реальный PostgreSQL, S3 — in-memory адаптер в тестах);
- `uv run ruff check app tests migrations` и `uv run mypy app` — без ошибок;
- `docker compose -f deploy/compose.data.yaml up -d --build` с `SEED_SYNTHETIC=1` — migrate exit 0, data-api healthy;
- `uv run ruff check scripts/smoke.py` — PASS; `scripts/smoke.py` против живого контура (реальные PostgreSQL + SeaweedFS S3): взятие → 8 фото → поездка → возврат → 8 фото → `manual_map` → завершение и чтение прошлого осмотра — PASS;
- Integration code `09a11b3689740bd5fce26a10d4f417587db6472e`: Python `IssueResolve` теперь принимает только `status` для `in_progress`; терминальные решения по-прежнему требуют непустой комментарий и подтверждение. `ruff check app/api/schemas.py tests/test_admin_queues.py`, `mypy app`, полный `pytest -q` на отдельном PostgreSQL 17.6 — PASS, 43 теста; регрессия проверяет отказ старого payload, 403 сотруднику и сохранение `assigned_to`/`resolved_by`.
- runtime-роль БД не может менять схему; анонимный запрос к S3 → 403.

Контрактные вопросы сопоставлены с OpenAPI v1.13, Go client/mock и Python. Data API готов для интеграции: Go v1.13 integration test `84ad9b51d732787a8e6057577ed7048f209a4015` прошёл против PostgreSQL/S3 и опубликован вместе с status checkpoint `1766bccdb9ec4f8ec46c79c95b4b2d668670a57e`. Сотрудник подал замечание через Go post-return dialog и получил 403 на admin action; Go admin dialog назначил `assigned_to` и разрешил его с `resolved_by`. INT-03 recovery code `e6888015088d4e4feff391efaf3af17d3d7e4452` опубликован в checkpoint `6b8f033255fd8229020987883d09e71fd3d70370`: Go worker восстанавливает 4/8 photo progress из Python, replay ссылается на ту же inbox row и не обрабатывается повторно. Это синтетический actor/MAX transport; реальный MAX, Bridge, Linux host-secret permissions, сервисные отказы, backup/restore и QA не проверялись.

Найдено при нагрузке: в контейнере с read-only ФС Starlette не мог буферизовать multipart > 1 MiB во временный файл (400 на фото 5 MiB) — добавлен tmpfs `/tmp` 128 MiB для data-api.

Окружение: на Windows с кириллицей в профиле Docker Desktop не монтирует файлы из `%LOCALAPPDATA%` — секреты кладутся в ASCII-путь через `MAX_FLEET_SECRETS_DIR` (добавлено в `scripts/bootstrap.ps1`). На Linux docker secrets из файлов с правами 0600 другого владельца могут быть недоступны пользователю 10001 контейнера — проверить на INT.

## Контрактные вопросы — решения интегратора в `codex/integration`

Устаревшие вопросы из исходного handoff закрыты сверкой текущего v1.13 и фактических реализаций. Версию контракта повышать не потребовалось. Integration-регрессия подтвердила, что переход issue в `in_progress` принимает только `status`, а терминальный `resolved` требует комментарий и подтверждение; старый Python payload отклоняется:

1. **`return.complete`.** OpenAPI использует общий `CommandResult` с `aggregate`; mock и Python возвращают агрегат Return. Go декодирует конкретный DTO по операции.
2. **Версии фото и осмотра.** Текущий OpenAPI v1.13 явно говорит, что фото повышает Inspection и родительский checkout/return; `inspection.update` и `inspection.confirm_photos` также повышают версии. Go mock и Python совпадают.
3. **Ошибки правил/challenge.** `RULES_REQUIRED` и `CHALLENGE_EXPIRED` — 422 в OpenAPI, mock и Python.
4. **Admin ACL.** Чтение `/admin/*` возвращает `ACCESS_DENIED`; команды требуют `ADMIN_REQUIRED`. Контракт, mock и Python совпадают.
5. **Admin challenge.** Go BE-09 и Python используют поля intent, proof ID и повторную проверку SHA-256 intent; контрактные и Go регрессии пройдены в опубликованных checkpoint.
6. **`conversation.save`.** `target_id` — employee самого actor; начальная версия 1 согласована с `/state`, первое сохранение создаёт версию 2.
7. **`employee.grant` существующего MAX ID** возвращает 409 `INVALID_STATE`.
8. **Получатели уведомлений.** Admin получают все события; driver получает `trip_admin_closed`, employee — `access_changed`. Это соответствует PRODUCT_SPEC §13.6 и Python/Go mock.
9. **Previous inspection photo.** Маршрут есть в контракте, Python и Go mock.

## Замечания DE для MVP и границы решений

- Одометр на активной брони/поездке теперь можно аудируемо исправить только у vehicle snapshot с CAS по `vehicle.version`; assignment, исходный осмотр и фото не меняются. Прирост свыше 1000 км за поездку уже просит дополнительное подтверждение в Go; верхний предел не вводится.
- Issue хранит `assigned_to` и terminal resolution metadata; категории `parking`, `car_lock` и `post_return` поддержаны. Возврат задаёт один составной вопрос «машина закрыта, ключи возвращены», сохраняя ответы `car_locked` и `keys_returned` раздельно.
- Hold остаётся 15 минут согласно исходному требованию. Контакты сотрудников/ответственных не добавлялись до приватного seed и решения H-03; срок хранения PII/фото и регламент backup нужно утвердить по H-04 до живого пилота.
- `retired_at` остаётся P1 после приёмки P0. Непроверенные Linux permissions, независимая QA и реальный MAX не выводятся как готовые.

## Результаты нагрузки и восстановления

Стенд исходного DE-08 измерения: Windows 11, Docker Desktop 29.4 (4 vCPU, 12 GiB для VM), PostgreSQL 17.6, SeaweedFS S3, data-api — 1 процесс uvicorn, pool 5+5. Contract SHA `aa56f0e`, schema head `0001`; эти performance/backup цифры не повторялись после интеграционного v1.13 и миграции `0002`.

| Проверка | Результат |
|---|---|
| 50 users, 20 rps, 600 с (70% чтений, 30% команд, конкуренция 50 человек за 10 машин) | 12 000 запросов; p50 14.9 / p95 34.4 / p99 45.4 мс; 5xx — 0; 409 — 1 681 (ожидаемые: занятая машина, CAS диалога) |
| 10 загрузок по 5 MiB одновременно | 10/10 × 200; среднее 2.5 с, максимум 2.9 с |
| Гонки | 20 сотрудников → 1 машина: 1 победитель; 1 сотрудник → 10 машин: 1 hold; block vs start: поездка либо отказ; двойной complete: один 200 |
| Backup/restore на новых томах | 21 таблица / 3 258 строк совпали; 27 объектов с совпавшим SHA-256; 0 битых ссылок; alembic head 0001 |

Команды: `scripts/load.py`, `scripts/explain.sql`, `scripts/backup-restore.sh` (каталог `backups/` в .gitignore).
