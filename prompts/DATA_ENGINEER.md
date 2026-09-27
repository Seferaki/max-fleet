# Промпт data engineer

Можно адаптировать под собственный процесс. Токены MAX и работающий Go не нужны.

```text
Реализуй изолированный Python/data-контур MAX Fleet.

Прочитай AGENTS.md, docs/DATA_ENGINEER.md, docs/DATABASE.md,
docs/API_CONTRACT.md, задачи DE-01…DE-09 в docs/IMPLEMENTATION_PLAN.md
и docs/progress/DATA.md. Сверь контракт S-02 и его commit.
Если контракт не заморожен, сначала уточни конкретные недостающие DTO/операции,
продолжая независимую подготовку схемы. Не придумывай несовместимый API.

Работай в codex/data, только в своём контуре: services/data/,
deploy/compose.data.yaml, data tests/CI и docs/progress/DATA.md.
Не меняй Go, frontend и backend lock. Весь SQL, миграции, бизнес-инварианты,
durable state, фото, inbox/outbox и audit находятся в Python/PostgreSQL/S3.

Создай FastAPI + SQLAlchemy 2 + Alembic + psycopg, приватное S3 и Docker.
Выполни все таблицы/связи/ограничения DATABASE, единые assignment для человека
и машины, короткие транзакции, стабильный порядок locks, idempotent commands.
Реальная БД в интеграционных тестах — PostgreSQL, не SQLite.
Photo storage не держи в БД и не объявляй сохранённым до durable записи.

Воспроизводи contract fixtures без MAX и Go. Проверь гонки, повтор после commit,
restart, ACL/IDOR, TTL, фото, cancelled returns, admin close,
отказ storage/БД, нагрузку и backup/restore. Фиксируй реальные результаты,
не заменяй их обещанием. Не добавляй тяжёлую инфраструктуру без измеренной причины.

Создай синтетический seed и приватный bootstrap; секреты генерируй/читай
без вывода, реальные данные не коммить. При необходимом действии человека —
минимальный HUMAN_REQUIRED по OPERATIONS; прочую работу продолжай.

По маленьким подшагам делай проверенные commits/push и обновляй свой progress
с SHA, migration head, contract commit, командами и next_step.
В конце DE-09 выставь DATA_READY_FOR_INTEGRATION только при выполненных критериях.
Передай сервис, Docker, миграции, тесты и отчёт. Соединение с Go выполняется в INT.
```
