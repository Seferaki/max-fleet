# Прогресс backend и финальной интеграции

Единственный текущий статус backend. S-01…S-03 выполнены; бизнес-маршруты ещё не написаны. Контракт v1 опубликован в `codex/backend`, будущие ветки data/QA должны взять именно его commit. Описание задач — [план](../IMPLEMENTATION_PLAN.md); обновление — [протокол](../HANDOFF.md).

```yaml
status_schema: 1
track: backend
lock_state: ACTIVE
owner: B
session_id: "1e22c0db-a86b-44f0-b2d1-33c197bea3ee"
branch: codex/backend
heartbeat_utc: "2026-09-27T13:53:00Z"
current_task: BE-01
current_substep: "Подтверждён takeover от A; добавить типизированные команды BE-01"
last_verified_code_commit: "fffc54eb257eb29783a0d9d2d57981b6c66595e9"
checkpoint_state: WIP
contract_commit: "aa56f0e05b3c2458eee1fe88550d183ece9075af"
backend_ready_for_integration: false
full_stack_accepted: false
next_step: "BE-01: добавить типизированные команды и отдельный mock с auth/версиями; проверить контракты и восстановление"
human_required: [H-01]
```

## Реестр задач

| ID | Статус | Commit / доказательство | Следующий подшаг / блокер |
|---|---|---|---|
| S-01 | DONE | `76d2ac9b2b709e41734fa32d413c00695daa600b`; проверки ниже | H-01 ожидает владельца; S-02 продолжается независимо |
| S-02 | DONE | `aa56f0e05b3c2458eee1fe88550d183ece9075af`; OpenAPI/fixtures/linters | Общий contract commit для data/QA до разделения веток |
| S-03 | DONE | `efe28b30ee513cdbd3d9c16e799d3808d5f6ca52`; чистый clone и GitHub CI success | BE-01 |
| BE-01 | IN_PROGRESS | `fffc54eb257eb29783a0d9d2d57981b6c66595e9` — чтения DataAPI client; WIP | Команды, mock, scenarios |
| BE-02 | TODO | — | См. план |
| BE-03 | TODO | — | См. план |
| BE-04 | TODO | — | См. план |
| BE-05 | TODO | — | См. план |
| BE-06 | TODO | — | См. план |
| BE-07 | TODO | — | См. план |
| UI-01 | TODO | — | См. план |
| BE-08 | TODO | — | См. план |
| BE-09 | TODO | — | См. план |
| BE-10 | TODO | — | См. план |
| BE-11 | TODO | — | См. план |
| BE-12 | TODO | — | См. план |
| INT-01 | TODO | — | См. план |
| INT-02 | TODO | — | См. план |
| INT-03 | TODO | — | См. план |
| INT-04 | TODO | — | См. план |
| INT-05 | TODO | — | См. план |
| INT-06 | TODO | — | См. план |

## Готовность организационных входов

| ID | Статус | Что требуется |
|---|---|---|
| H-01 | HUMAN_REQUIRED | Создать/отправить на модерацию бот MAX; затем локально ввести токен через `scripts/enter-max-token.ps1 -Enter`. Токен сейчас отсутствует. |
| H-02 | LOCAL_READY | Docker CLI/Compose/Engine доступны локально; HTTPS сервер/домен и права доступа нужны к INT-04 |
| H-03 | UNKNOWN | Реальные машины, правила, ключи, admin ID приватно; создан только `demo_only` seed |
| H-04 | UNKNOWN | Политика реального пилота: фото/доступ/срок хранения/владелец backup |

Архитектура Go → Python API → PostgreSQL и последовательная работа двух ноутбуков подтверждены заказчиком. ADR-07…10 остаются рабочими defaults без изменения бизнес-правил.

## Последний checkpoint

- BE-01 code commit: `fffc54eb257eb29783a0d9d2d57981b6c66595e9`. `services/gateway/internal/dataapi` содержит типизированные DTO `/meta`, `/me`, `/vehicles`, `/vehicles/{id}`, проверку URL/actor/UUID/версии метаданных, обязательных служебных заголовков, ограничение ответа 2 MiB, строгий JSON, ошибку с кодом/версией и повтор 429/503/transport с тем же X-Request-ID. `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/verify.ps1 -Direction gateway` → Go test/vet/build exit 0; тесты неизвестного actor, 503 retry, 409 без retry, недопустимого ввода/лишнего поля и несовместимого контракта прошли. Команды, mock, сохранение состояния и проверка сценариев ещё не реализованы; BE-01 WIP.

### Предыдущий checkpoint

- S-03 закрыт по опубликованному [GitHub Actions run 36322065525](https://github.com/Seferaki/max-fleet/actions/runs/36322065525): contract, gateway, web, docker, secret-scan — все пять job success. Чистый checkout и локальные команды указаны ниже. Это проверка каркаса, не готовность MAX, Python или UI-01. Начат BE-01.

### Предыдущий S-03 checkpoint

- S-03 code commit: `efe28b30ee513cdbd3d9c16e799d3808d5f6ca52`. Второй чистый clone из опубликованного `5ab7e85` прошёл `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/verify.ps1 -Direction all`: контракт, Go test/vet/build, web typecheck/build и три Docker build; `py scripts/check-secrets.py --tracked` → без совпадений; git status clone чистый. GitHub Actions run [36321666745](https://github.com/Seferaki/max-fleet/actions/runs/36321666745) выявил конфликт pinned `jsonschema==4.22.0` с `openapi-spec-validator==0.9.0` (требует >=4.26.0): contract job failed, остальные четыре job success. `jsonschema` закреплён на `4.26.0`; `docker run ... python:3.12-slim ... pip install -r contracts/requirements-dev.txt && python contracts/validate.py` → exit 0 и 44 сценария. Новый удалённый CI ещё не проверен, поэтому S-03 WIP.

### Предыдущий S-03 checkpoint

- S-03 code commit: `da03904b06eea69138649dd98f57785f900aee24`. Первый чистый clone на `3c6606a` выявил platform-dependent CRLF в `build_openapi.py`: `py contracts/validate.py` упал на byte equality YAML после генерации. Генератор теперь пишет UTF-8/LF через `write_bytes`; повтор локально → `OK: 2 OpenAPI, 36 routes, 24 commands, 7 examples, 44 scenarios`. Новый чистый clone и CI ещё не проверены, S-03 остаётся WIP.

### Предыдущий S-03 checkpoint

- S-03 code commit: `64c9c2f92aa4cf16dcd4288c743e514a3b93edd7`. `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/bootstrap.ps1` → 8 service/DB/storage секретов созданы вне OneDrive; повтор → существующие не перезаписаны, ACL ограничен текущим пользователем, значения не выводились. `scripts/enter-max-token.ps1 -Prepare/-Check` → работает, MAX token отсутствует. `sh -n` для Unix-скриптов → exit 0; `bootstrap.sh` выполнен в одноразовом Python-контейнере → файлы созданы. `py scripts/check-secrets.py --staged` и `--tracked` → совпадений нет.
- `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/verify.ps1 -Direction all` → exit 0: контракт/Redocly, Go test/vet/build, npm typecheck/build, три Docker image build. Docker Engine стал доступен в ходе S-03. Runtime smoke для gateway и web: UID 10001/101, `/health/live`=200, `/health/ready`=503; последнее ожидаемо для каркаса.
- CI workflow добавлен, но удалённый запуск пока не проверен. Чистый checkout пока не проверен. Следующий подшаг — опубликовать checkpoint и выполнить эти две проверки, затем закрыть S-03 только по фактам.

### Предыдущий S-03 checkpoint

- S-03 code commit: `f3eb2511de18e5df637f42553831173203d62754`. Созданы Go module (Go 1.27.1, MAX SDK v2.4.1) и React/Vite shell с lock-файлами; `data-mock` пока только каркас. `go mod tidy`, `go test ./...` (пакеты без тестов), `go vet ./...`, `go build ./cmd/gateway ./cmd/data-mock` → exit 0. Go 1.27.1 Windows archive сверён по SHA-256 с официальным релизом. HTTP smoke gateway: `/health/live` → 200, `/health/ready` → 503. `npm install --no-audit --no-fund`, `npm run typecheck`, `npm run build` → exit 0. Карта ещё не реализована; UI-01 остаётся обязательной.
- Docker Engine недоступен; контейнерные сборки и readiness настоящих сервисов не проверялись. Go toolchain размещён в игнорируемой `.local/`, не передаётся через Git.
- Следующий подшаг: Dockerfile, bootstrap/verify scripts и CI; не ставить S-03 DONE до воспроизводимой проверки на чистом checkout.

### Предыдущий S-02 checkpoint

- S-02 code commit: `aa56f0e05b3c2458eee1fe88550d183ece9075af`; версии обоих OpenAPI — `1.0`. `py contracts/validate.py` → OK: 2 OpenAPI, 36 внутренних маршрутов, 24 command examples, 7 иных examples и 44 сценария. `npx --yes @redocly/cli@2.54.3 lint contracts/data-api.openapi.yaml contracts/map-api.openapi.yaml --config redocly.yaml --format=stylish` → обе схемы valid, предупреждений нет. Сопоставление исходной таблицы в `docs/API_CONTRACT.md` с OpenAPI → все 24 операции совпали.
- Проверен исходный SDK `github.com/max-messenger/max-bot-api-client-go/v2` на теге `v2.4.1`, commit `b3b7025d53ee2a81b896a0b73ae8b02c672900d6`: имена событий, message/callback ID, image/location и dialog type. В `go.mod` SDK требует Go 1.24; локальный `go` CLI отсутствует, сборка SDK не заявлена.
- Контракт опубликован в рабочей backend-ветке. Data/QA-ветки пока отсутствуют; им нужен тот же contract commit. Сценарии не запускались на mock/Python, которых пока нет. Docker Engine по-прежнему недоступен; Go build и контейнерный build не проводились.
- Следующее действие: S-03, затем BE-01. H-01 остаётся независимым human gate для реального MAX.

### Предыдущий S-02 checkpoint

- S-02 code commit: `db17a4fb9648de8704860c2c3a32c78affda3c78`. `py contracts/build_openapi.py` и `openapi-spec-validator 0.9.0` → valid: 36 маршрутов, 24 discriminated-команды, 106 схем. Сверены категории/статусы замечаний с `docs/DATABASE.md`. Описаны фото 8 слотов, версии, очередь inbox/polling/delivery, actor и service/worker tokens. Состояние WIP: fixtures, сценарии и проверка MAX SDK ещё не сделаны.
- Следующий подшаг: добавить проверяемые JSON-примеры и сценарии, закрепить внешний маршрут карты, проверить схемы и опубликовать contract gate.

### Предыдущий S-02 checkpoint

- S-02 code commit: `5df66029edf0072965de23e0abcf9300724c2090`. `py contracts/build_openapi.py` → OpenAPI 3.1.0, 18 маршрутов, 48 схем; `openapi-spec-validator 0.9.0` → valid. `.local/openapi-validator` не публикуется. Состояние WIP: команды, загрузки фото, очереди, сценарии и contract gate пока отсутствуют.
- Следующий проверяемый подшаг: описать операции/маршруты, затем повторить генерацию и валидацию; `DONE` для S-02 не ставить до полной проверки fixtures.

### Предыдущий S-01 checkpoint

- Code commit: `76d2ac9b2b709e41734fa32d413c00695daa600b` (S-01). Изменены `.dockerignore`, `contracts/examples/synthetic-seed.json`, `scripts/enter-max-token.ps1`.
- Проверено: JSON разбирается; `demo_only=true`, 10 уникальных машин и тестовые сотрудники; `git check-ignore` покрывает `.env`, `secrets/`, `data/`, `backups/`, `*.dump`, `*.sql.gz`; staged secret pattern scan без совпадений; `git diff --cached --check` прошёл.
- Проверено: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/enter-max-token.ps1 -Prepare` → приватный каталог создан вне OneDrive; `-Check` → `False` (токена нет); ACL каталога ограничен текущим пользователем.
- Диагностика: `docker --version` → 29.8.0; `docker compose version` → v5.5.1; `docker info --format '{{.ServerVersion}}'` → ошибка подключения к Docker Desktop Linux Engine. Сборка контейнеров не выполнялась.
- `git fetch origin` после code commit не удался из-за сети (GitHub: port 443). Перед push повторить fetch, сверить `owner/session_id` и remote SHA.
- H-01: в [MAX для партнёров](https://business.max.ru) открыть профиль, раздел «Чат-боты», создать бот «MAX Fleet» с описанием «Служебный автопарк: оформление поездок, осмотры автомобиля, возврат и сообщения ответственному» и логотипом 500×500 PNG/JPEG, отправить на модерацию. После одобрения выполнить `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/enter-max-token.ps1 -Enter` на этом устройстве; значение не отправлять в чат. Агент позже проверит файл и `/me` без вывода токена. До этого S-02/Go mock идут независимо.
- Следующее действие: опубликовать этот checkpoint, затем S-02 OpenAPI и fixtures. Реальной проверки MAX ещё нет.

## Журнал передачи

| UTC | От → кому | Задача / SHA | Результат |
|---|---|---|---|
| 2026-09-27 12:19 | FREE → A | S-01 / `3d53d5a` | Claim опубликован в `codex/backend` |
| 2026-09-27 13:53 | A → B | BE-01 / `f3e6f49` | Пользователь подтвердил остановку A; takeover через отдельный claim-коммит |
