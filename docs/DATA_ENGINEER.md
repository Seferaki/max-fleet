# Data engineer — отдельное задание

Ваша зона: Python API, бизнес-транзакции, PostgreSQL, миграции, метаданные и приватное хранение фото. Backend Go и экран карты разрабатываются отдельно. Задачи DE-01…DE-09 описаны в [плане](IMPLEMENTATION_PLAN.md), статус — [progress/DATA.md](progress/DATA.md), таблицы — [DATABASE](DATABASE.md).

## 1. Что нужно перед началом

1. Получить зафиксированный контракт S-02 и его commit; читать [API_CONTRACT](API_CONTRACT.md).
2. Создать codex/data от общей базы с документацией/контрактом. Не изменять services/gateway/ и web/.
3. Поднять свой compose.data.yaml: data-api, data-worker, migrate, PostgreSQL и S3.
4. Использовать синтетические 10 машин, минимум 3 employee и 2 admin, набор искусственных изображений. MAX token не нужен.
5. Все параметры БД/S3/service auth — из локальных secret-файлов/окружения; реальные DSN в Git отсутствуют.

Результат вашей работы — независимо запускаемый сервис с миграциями, тестами и документацией. Интегратор позднее меняет URL Go-клиента и подключает full Compose. Вы не пишете SQL для ручного копирования в Go.

## 2. Рекомендуемая структура

```text
services/data/
  pyproject.toml + lock
  Dockerfile
  app/
    api/           маршруты, DTO, service auth, actor context
    domain/        бизнес-операции, переходы, проверки доступа
    repositories/  запросы SQLAlchemy/параметризованный SQL
    storage/       S3 adapter, валидация и чтение файлов
    workers/       hold expiry, leases, outbox recipients, orphan cleanup
    config.py
  migrations/      Alembic revisions
  tests/           unit, postgres integration, storage, contract, concurrency
  fixtures/        только синтетические данные
```

Не нужны десятки абстракций для каждого SQL SELECT. Слои отделяют API, бизнес-транзакцию и инфраструктуру; модели БД не возвращаются наружу как DTO.

## 3. Паттерны и конкретные правила

| Подход | Как применить |
|---|---|
| Service layer | Одна операция start_trip/complete_return/claim_vehicle координирует все проверки и изменения |
| Unit of Work | Session/transaction на одну команду; commit один раз на границе сервиса, rollback при исключении |
| Repository | Параметризованные запросы; методы предметной области, а не публичный «исполнить SQL» |
| State machine | Явные допустимые переходы и причины отказов; терминальные записи нельзя «редактировать как обычно» |
| Pessimistic lock + unique constraints | SELECT FOR UPDATE и vehicle_assignments; версия объекта отдельно защищает stale UI |
| Idempotent command | scope/key/body hash, результат в транзакции; повтор после lost response не повторяет эффект |
| Transactional outbox | Событие создаётся вместе с доменным commit; сетевой вызов MAX выполняет Go позднее |
| Inbox + lease | Durable приём и повторяемая обработка; per-actor порядок, lease expiry и fencing |
| Optimistic version | UPDATE ... WHERE version=expected; ноль строк → STALE_VERSION |
| Storage adapter | ObjectStore put/get/head/delete; приватные ключи, тестируемые сбои |
| Append-only audit | Причина/автор/до/после в одной транзакции, без удаления истории |

Для MVP предложена синхронная SQLAlchemy Session и psycopg. Один Session нельзя делить между потоками/задачами. В FastAPI блокирующие операции выполнять в sync-обработчиках или выделенном threadpool, не блокировать event loop из async def. При осознанном выборе async — отдельный AsyncSession на task и полное согласование драйвера. [Правила Session](https://docs.sqlalchemy.org/en/20/orm/session_basics.html).

## 4. Инварианты, которые нельзя оставить только в коде Go

- Один employee и vehicle имеют максимум одно текущее закрепление между holding и active trip.
- Выбор «свободной» машины и создание hold — одна транзакция, а не GET затем безусловный INSERT.
- Срок hold проверяется на каждой операции независимо от фоновой очистки.
- До старта нужны math, принятые правила, данные осмотра и 8 сохранённых фото.
- При before issue hold отменяется; в active trip issue сохраняет trip и блокирует следующую выдачу.
- До обычного complete нужны текущий return, math, анкета, 8 after-фото, своя подтверждённая точка, ключи/закрытие/допустимая парковка.
- Возврат с повреждением/грязью разрешён, но новая выдача заблокирована.
- Blocked employee может вернуть свою активную машину и сообщить о проблеме.
- Admin close разрешает неполные данные с причиной и needs_review; никаких фиктивных координат/фото.
- Любой бизнес-эффект, audit/outbox и idempotent result сохраняются атомарно.

Права проверяются по actor из доверенного service-вызова и данным БД. Нельзя доверять role, employee_id, «я админ» или readiness, присланным клиентом.

## 5. Нагрузка и SQL

Стартовые ограничения: 1 API-процесс и 1 worker; pool_size=5, max_overflow=5 на процесс, pool_timeout=3 с; DB statement_timeout порядка 3 с и lock_timeout порядка 1 с. Уточнить в нагрузочном отчёте: при двух процессах суммарный pool уже до 20, не 10. Держать резерв соединений для миграций и диагностики. Это начальные настройки, не универсальные числа.

- Транзакции короткие: никаких MAX/S3 запросов под row lock.
- Всегда единый порядок блокировок из DATABASE; конфликт уникальности превратить в понятный 409.
- Для deadlock/serialization error допустим ограниченный повтор **всей** транзакции с jitter и стабильным ключом. Не повторять бесконечно любую ошибку.
- Очереди claim-ить небольшими порциями; SKIP LOCKED подходит для конкурирующих workers, но не заменяет per-actor сериализацию.
- Для per-actor обработки закреплять активный lease владельца: например, transaction-scoped advisory lock для выдачи lease + проверка нет другого processing по actor. При expired lease сменить fencing token; каждая ack/изменение очереди проверяет его.
- Основные выборки: available vehicles, active trip for employee, history employee/vehicle, open issues, due notifications. Проверить EXPLAIN (ANALYZE, BUFFERS) на синтетическом объёме.
- Не грузить бинарные фото в БД/JSON списков. Не допускать N+1 для фото и сотрудников в истории; selectinload/join выбирать по реальному плану.
- Лимитировать результаты, body/строки, фото, число retries и время ожидания.
- Redis/partitioning/read replicas/сложный sharding не нужны до измеренной причины.

Цель DE-08: 50 одновременных пользователей, 20 JSON API rps 10 минут, p95 ≤ 500 мс (без внешнего MAX/S3), неожиданные 5xx < 1%; отдельный тест 10 загрузок по 5 MiB. Гонки важнее среднего latency: 20 запросов на одну машину → ровно один победитель. Указать CPU/RAM/диск/версии/объём данных, а не только «нагрузку выдержало».

## 6. Файлы и отказоустойчивость

1. Проверить actor, черновик, ракурс и лимиты до приёма большого тела.
2. Читать поток ограниченными порциями, вычислить hash, проверить реальный формат и размер изображения.
3. Сохранить объект с новым случайным key. Не принимать filename как путь.
4. В короткой транзакции повторно проверить актуальность draft/version, связать ready asset и слот, записать audit/idempotent result.
5. Если БД отказала — прежний слот не потерян; неподвязанный новый объект уберётся позднее.
6. Если S3 отказал — фото не засчитывается; retry не создаёт несколько логических фото.
7. Просмотр всегда авторизован; обезличенный previous inspection — отдельная проекция.
8. При orphan cleanup сначала проверить отсутствие inspection/issue ссылок и давность 24 ч. Не считать staged автоматически мусором во время retry.
9. Не удалять фотографии завершённых поездок до согласования retention. Реальные фото не попадают в fixtures, логи и CI artifacts.

## 7. Миграции, seed, backup

- Alembic revisions — единственный путь обновления схемы. create_all не заменяет production migration.
- Изменения совместимы с предыдущей версией API на время обновления: добавить nullable/новую колонку → заполнить → включить использование → позже убрать старую.
- Migration job выполняется один раз перед readiness API; дополнительно защищён advisory lock от двух запусков.
- Seed идемпотентный, только dev/test. Production bootstrap admin читает приватную конфигурацию и не печатает ID/токены без необходимости.
- Service role не superuser. Для миграций отдельная роль; обычные запросы не могут менять схему.
- Backup включает согласованный snapshot PostgreSQL, набор S3 объектов и manifest с SHA-256. На небольшом MVP проще кратко остановить writers, выполнить backup и возобновить.
- Restore проверяется на новых volumes, по counts/FK и чтению контрольных фото. Файл backup вне Git, доступ ограничен.
- Не выполнять destructive down -v/reset на пользовательских данных; dev reset доступен только явно выбранному disposable compose project.

## 8. Что передать интегратору

| Артефакт | Минимум |
|---|---|
| Код | Версионированный API + domain + SQL/repositories + storage/workers |
| Контракт | Тот же v1 commit, что у backend; все изменения перечислены |
| Миграции | Head revision, порядок, проверенные upgrade/совместимость |
| Конфигурация | Только имена env/secret файлов и значения для несекретных defaults |
| Docker | compose.data.yaml, pinned images, healthchecks, volumes, migration dependency |
| Тесты | Contract, реальные PostgreSQL/S3, ACL, гонки, duplicate/restart |
| Отчёт | Код SHA, команды, результаты latency/concurrency и backup/restore |
| Прогресс | DE-01…DE-09 DONE, data_ready_for_integration=true, известные ограничения |

До готовности не менять Go, чтобы «быстрее подогнать» интеграцию. Если контракт недостаточен — предложить точное изменение DTO/operation с примером и получить единый commit обеим сторонам; продолжить независимые таблицы/тесты.

Промпт для старта — [prompts/DATA_ENGINEER.md](../prompts/DATA_ENGINEER.md).
