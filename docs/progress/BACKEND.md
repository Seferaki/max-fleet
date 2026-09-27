# Прогресс backend и финальной интеграции

Единственный текущий статус backend. S-01 подготовила безопасный локальный ввод и демонстрационный seed; код приложения ещё не написан. Описание задач — [план](../IMPLEMENTATION_PLAN.md); обновление — [протокол](../HANDOFF.md).

```yaml
status_schema: 1
track: backend
lock_state: ACTIVE
owner: A
session_id: "b78b0297-a9fa-4023-a429-3c2df2f65cfe"
branch: codex/backend
heartbeat_utc: "2026-09-27T12:29:31Z"
current_task: S-02
current_substep: "Добавить команды, фото и технические очереди к уже проверенным чтениям OpenAPI"
last_verified_code_commit: "5df66029edf0072965de23e0abcf9300724c2090"
checkpoint_state: WIP
contract_commit: null
backend_ready_for_integration: false
full_stack_accepted: false
next_step: "S-02: добавить discriminated union команд, маршруты фото и очередей, version bump; затем примеры и сценарии"
human_required: [H-01]
```

## Реестр задач

| ID | Статус | Commit / доказательство | Следующий подшаг / блокер |
|---|---|---|---|
| S-01 | DONE | `76d2ac9b2b709e41734fa32d413c00695daa600b`; проверки ниже | H-01 ожидает владельца; S-02 продолжается независимо |
| S-02 | IN_PROGRESS | `5df66029edf0072965de23e0abcf9300724c2090` — 18 GET, общие DTO; WIP | Команды, фото, очереди, fixtures и contract gate |
| S-03 | TODO | — | См. план |
| BE-01 | TODO | — | См. план |
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
| H-02 | UNKNOWN | Docker CLI/Compose есть, Engine недоступен; HTTPS сервер/домен и права доступа нужны только к INT-04 |
| H-03 | UNKNOWN | Реальные машины, правила, ключи, admin ID приватно; создан только `demo_only` seed |
| H-04 | UNKNOWN | Политика реального пилота: фото/доступ/срок хранения/владелец backup |

Архитектура Go → Python API → PostgreSQL и последовательная работа двух ноутбуков подтверждены заказчиком. ADR-07…10 остаются рабочими defaults без изменения бизнес-правил.

## Последний checkpoint

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
