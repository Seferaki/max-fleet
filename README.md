# MAX Fleet

Чат-бот MAX для общего корпоративного автопарка: сотрудник выбирает машину, фиксирует состояние до и после поездки, возвращает её с точкой парковки; администратор управляет доступом и разбирает замечания.

**Статус на 30.09.2026:** BE-12 и обязательный UI-01 закрыты как Go/mock gates; Actions [#292](https://github.com/Seferaki/max-fleet/actions/runs/36635818102) прошёл пять jobs. INT-01 опубликована в `codex/integration`: full Compose запускает Go, React-карту, Python, PostgreSQL и S3. Python v1.13 прошёл 43 локальных PostgreSQL-теста и live smoke с 8+8 фото и ручной парковкой. INT-02 закрыта как синтетический integration gate: опубликованный Go inbox/dialog → Python/PostgreSQL/S3 тест `84ad9b51d732787a8e6057577ed7048f209a4015` проверяет 15-минутный hold, 8+8 фото, ручную карту с идемпотентным повтором, возврат/историю/S3-фото, issue ACL и admin assignment/resolution; Go test/vet/build прошли. В INT-03 опубликован recovery-подшаг `e6888015088d4e4feff391efaf3af17d3d7e4452` (checkpoint `6b8f033255fd8229020987883d09e71fd3d70370`): после 4/8 фото Go worker пересоздаётся, Python возвращает сохранённый прогресс, повтор фото-события не создаёт новую inbox row и не меняет счёт. Response-loss code `94ff2fb74500b14aa4598d019e06a4cd8862363a` опубликован в checkpoint `1a51d5ad59ea87357e13345fcaf5d36524bf4658`: тест отбросил ответ после Python commit `return.complete`, Go повторил тот же request/idempotency key/body. Новый code `d5b6e7aeea6fb83ccc5809082af20efb9e500b51` проверяет live повтор завершённого `return.complete` с тем же ключом: сохранённый Return вернулся, версия Trip осталась прежней, автомобиль доступен; status checkpoint `a4e2bab9b950e7cc114eb6a12c048fdbb60ecc9f` опубликован. Полный Go test/vet/build прошёл. Тесты используют синтетический MAX transport, не реальный MAX. Worker recovery пересоздаёт структуры в том же процессе; фактический data-api restart и Compose down/up без -v с сохранением volumes проверены, а backup/restore остаётся. Gateway `/health/ready` сообщает `dialog flows incomplete`. QA пропускается по решению владельца и остаётся NOT RUN, не PASS; исходная `codex/data` сохранена на SHA `9eb2211b29e48fce8a6afc410bc986fa98a4988e`. Следующий шаг INT-03 — backup/restore на новых synthetic volumes и оставшиеся отказные проверки. Реальный MAX требует H-01.

## Начать работу

1. Backend-разработчик открывает [план](docs/IMPLEMENTATION_PLAN.md), [прогресс backend](docs/progress/BACKEND.md) и запускает [промпт продолжения](prompts/BACKEND.md).
2. При смене ноутбука действует [протокол передачи](docs/HANDOFF.md). Состояние работы хранится в Git; новый аккаунт не нуждается в старом чате.
3. Data engineer получает [отдельное задание](docs/DATA_ENGINEER.md), [схему таблиц](docs/DATABASE.md), [контракт API](docs/API_CONTRACT.md) и [свой прогресс](docs/progress/DATA.md).
4. QA получает один основной файл — [требования и рекомендации по тестированию](docs/QA_REQUIREMENTS.md), затем ведёт [прогресс QA](docs/progress/QA.md).
5. После готовности обеих частей запускается [промпт интеграции](prompts/INTEGRATION.md). Последний этап соединяет сервисы и проверяет продукт целиком.

## Стек и разделение работы

| Часть | Стек | Владелец |
|---|---|---|
| MAX, диалоги, внешний HTTP API, уведомления | Go, официальный MAX Go SDK | Два ноутбука по очереди |
| Правила, SQL, миграции, файлы, внутренний API | Python, FastAPI, SQLAlchemy 2, Alembic, psycopg | Data engineer независимо |
| Данные | PostgreSQL | Только Python обращается к БД |
| Фотографии | Приватное S3-совместимое хранилище | Python; Go получает файлы через API |
| Экран карты | React, TypeScript, Vite, MAX UI, MAX Bridge, Leaflet | Backend-направление, задача UI-01 |
| Развёртывание | Docker Compose, TLS reverse proxy | Backend, финальная интеграция |

Go сначала использует HTTP-заглушку того же контракта. Переключение на Python выполняется конфигурацией на последнем этапе; SQL в Go не появляется.

## Документы и диаграммы

| Документ | Что внутри |
|---|---|
| [PRODUCT_SPEC](PRODUCT_SPEC.md) | Пользовательские сценарии, правила BR и критерии AC |
| [Архитектура](docs/ARCHITECTURE.md) | Компоненты, границы ответственности, решения и диаграмма |
| [CJM](docs/CJM.md) | Путь сотрудника, затруднения, обратная связь и работа администратора |
| [База данных](docs/DATABASE.md) | ER-диаграмма, поля, связи, ограничения, индексы и транзакции |
| [Внутренний API](docs/API_CONTRACT.md) | Общий контракт Go, mock и Python |
| [План реализации](docs/IMPLEMENTATION_PLAN.md) | Задачи, зависимости, артефакты, проверки и участие человека |
| [Передача работы](docs/HANDOFF.md) | Очередь ноутбуков, checkpoint, восстановление после остановки |
| [Data engineer](docs/DATA_ENGINEER.md) | Изолированная работа, паттерны, нагрузка и критерии готовности |
| [QA](docs/QA_REQUIREMENTS.md) | Проверяемые требования, покрытие AC-01…AC-27, приёмка |
| [Настройка и эксплуатация](docs/OPERATIONS.md) | Бот MAX, токены, Docker, HTTPS, резервирование |

Mermaid-диаграммы встроены в документы и отображаются GitHub. SVG-копии находятся в [docs/diagrams](docs/diagrams/).

## Порядок реализации

`S-01…S-03: подготовка и контракт → backend с mock / Python и БД / QA отдельно → INT-01…INT-06: соединение, проверка, демонстрация`.

Текущая задача — INT-03: восстановление после ошибок, рестарты и конкурентные команды на Go → Python → PostgreSQL/S3. Go не подключается к PostgreSQL напрямую. Синтетические проверки не доказывают готовность реального MAX. Для локальной проверки используйте инструкции [OPERATIONS](docs/OPERATIONS.md); проверки Go/mock — `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/verify.ps1 -Direction all` при наличии Go 1.27.1, Python 3.12, Node 22.22.2 и Docker Engine.

## Секреты

Репозиторий публичный. Реальные токены, ФИО сотрудников, номера машин, фотографии, координаты и дампы сюда не добавляются. Используются синтетические fixtures, игнорируемые локальные `.env`/`secrets/` и приватные volumes. Токен вводит человек локально без публикации в чате; агент проверяет наличие и подключение, не выводя значение.

Источники платформы проверены 26.09.2026: [MAX API](https://dev.max.ru/docs-api), [официальный Go SDK](https://dev.max.ru/docs/chatbots/bots-coding/go), [мини-приложения](https://dev.max.ru/docs/webapps/introduction). Перед реальной интеграцией повторно проверить актуальность методов и доступа.
