# Настройка, Docker и минимальные действия человека

Backend Compose запускает Go gateway, contract mock и React map web; он не подключается к PostgreSQL/Python и не означает готовность приложения к реальному MAX. Агент выполняет технические шаги сам; человек нужен для своих аккаунтов, приватных данных, оплаты/решений владельца и устройств.

## 1. Human gates

| ID | Минимальное действие человека | Что делает агент | Что можно продолжить без этого |
|---|---|---|---|
| H-01 | Войти в MAX для партнёров, подтвердить профиль при необходимости, создать/модерировать бота; ввести token локально скрытым вводом | Подготовить название/описание, ссылки на нужные страницы, secret-файл, проверку /me, интеграцию | Весь backend mock, Python, QA |
| H-02 | Сейчас не требуется: проверенный hostname VDS — `efimok051.fvds.ru`, DNS A указывает на VDS | Установить Nginx/TLS через `scripts/deploy-vds-nginx.sh`, проксировать только на production web `127.0.0.1:8081`, проверить public readiness и MAX | Локальные отдельные контуры и full-synthetic |
| H-03 | Передать приватно реальные машины/сотрудников/admin ID, процедуру ключей, правила и город/часовой пояс. Для demo реальные данные не нужны | Проверить формат, импортировать приватный seed, создать администратора, проверить карточки | Подготовленный synthetic seed и demo |
| H-04 | Для пилота утвердить предложенные сроки, круг доступа, владельца backup и правила, заполнив реквизиты оператора | Реализовать автоматическое удаление/архивирование и расписание backup; проверить эксплуатацию | Demo без реальных сотрудников и фото; проект политик — `docs/policies/` |

H-01/H-02 блокируют только INT-04 с живым MAX. H-03/H-04 нужны до эксплуатации на настоящих сотрудниках; не требуют останавливать проектирование или тестирование. Synthetic seed (10 машин, 4 тестовых пользователя) уже подготовлен и отдельно проверен. Политики H-04 сгенерированы, но автоматическое удаление по срокам и расписание backup не заявляются как настроенные.

### Формат короткого запроса человеку

```text
HUMAN_REQUIRED: H-01
Нужно: завершить модерацию бота и ввести токен в локальный скрытый ввод.
Почему: действие требует вашего профиля MAX; у агента нет подтверждённого доступа к нему.
Где: https://business.max.ru → Чат-боты → выбранный бот → Настройки.
Сделайте: 1) скопируйте токен; 2) вставьте в подготовленный локальный prompt.
Не отправляйте значение в чат. Достаточно ответить «готово».
Проверка агента: секрет существует, /me успешен; значение не выводится.
Пока ожидаем: продолжаю BE-05 / DE-04.
```

Указывать реальные затронутые задачи и максимум несколько действий, а не просить человека писать SQL/Docker/код. Если ввод на этом устройстве недоступен, дать путь приватного файла и попросить создать его локально; не подменять это просьбой прислать secret в чат.

#### H-02 — публичный адрес приложения (hostname найден; настройка агента)

30.09.2026 в проверенной конфигурации VDS найден `efimok051.fvds.ru`; A-запись указывает на `185.146.157.147`. Ранее проверенный apex `fvds.ru` — неправильный адрес приложения. Текущий Nginx обслуживает ACME challenge, а остальные пути возвращают 404; TLS-сертификата ещё нет.

Для установки маршрута используйте на VDS `sudo bash /opt/max-fleet/scripts/deploy-vds-nginx.sh --hostname efimok051.fvds.ru --web-port 8081`. Скрипт сначала убеждается, что loopback-upstream — React web с `static_ready`, затем проверяет ACME webroot, получает Let's Encrypt сертификат и атомарно устанавливает HTTPS proxy. Конфигурация ведёт только на production web `8081`; synthetic demo остаётся отдельно на `8082`. При ошибке проверки или reload восстанавливается прежний конфиг. После установки требуется внешний HTTPS `/health/ready` probe.
## 2. Создание бота MAX

По [официальной инструкции создания](https://dev.max.ru/docs/chatbots/bots-create/create), проверенной 26.09.2026, сервис доступен через верифицированный профиль организации, ИП или самозанятого. Бот проходит модерацию; документация указывает до 48 часов по рабочим дням. Срок проекта не должен предполагать мгновенную выдачу доступа.

1. Человек проверяет доступ в [MAX для партнёров](https://business.max.ru) или «MAX для бизнеса», создаёт/верифицирует профиль.
2. В разделе чат-ботов создаёт бота с подготовленными агентом названием, описанием и подходящим логотипом. Пример описания: «Служебный автопарк: оформление поездок, осмотры автомобиля, возврат и сообщения ответственному».
3. Дожидается модерации. Агент записывает внешний блокер, но продолжает изолированную разработку.
4. После одобрения токен копируется из настроек в локальный secret-файл; порядок описан в [управлении ботом](https://dev.max.ru/docs/chatbots/bots-create/manage).
5. Агент проверяет `/me` через настроенный Go SDK и выводит только факт успешной проверки, не имя/username или сведения из ответа. Обновление токена выполняется в том же интерфейсе владельца, затем агент проверяет новый secret.
6. Первый admin и тестовые пользователи открывают личный диалог, чтобы бот мог получать их действия и отправлять предусмотренные уведомления. ID администратора вводится в приватный bootstrap; аккаунт GitHub не определяет роль MAX.

Не использовать инструкции Telegram/BotFather и не считать публично найденный token пригодным. При ограниченном доступе хакатона уточнить у организаторов предоставленный способ подключения; не придумывать обход регистрации.

## 3. MAX, webhook и mini-app

- Использовать официальный [Go SDK](https://dev.max.ru/docs/chatbots/bots-coding/go), pin release/commit и проверить API base URL по текущей документации. Токен передаётся заголовком Authorization, не URL.
- С 19.07.2026 API MAX использует `platform-api2.max.ru` и требует Russian Trusted Root CA. SDK уже направляет запросы на API v2; Go-клиент добавляет закреплённый по SHA-1 корневой сертификат только в собственный trust pool, не меняя системное хранилище. Источники: [изменения API MAX](https://dev.max.ru/docs-api/changelog-api), [корневой сертификат](http://reestr-pki.ru/cdp/rootca_ssl_rsa2022.crt); SHA-1 `8FF915CCAB7BC16F8C5C8099D53E0E115B3AEC2F`.
- Для production — webhook; polling только для разработки. Они не работают одновременно. [Режимы получения событий](https://dev.max.ru/docs/chatbots/bots-coding/prepare).
- Webhook требует HTTPS с доверенным сертификатом. Зарегистрировать `PUBLIC_BASE_URL/max/webhook` и secret, проверять `X-Max-Bot-Api-Secret`. Nginx web проксирует этот путь в Go gateway без записи заголовков/тела в access log; MAX ожидает HTTP 200 в течение 30 секунд, gateway ограничивает durable intake быстрым сохранением inbox. Неуспешная durable запись → 503. [Контракт webhook](https://dev.max.ru/docs-api/methods/POST/subscriptions).
- Перед сменой режима прочитать текущие subscriptions. Не удалять подписки неизвестного назначения. Идемпотентно привести только конфигурацию этого проекта к выбранному режиму.
- Безопасная read-only проверка списка подписок выполняется opt-in Go тестом; он печатает только количество и не печатает URL, типы событий или токен. Официальный [GET /subscriptions](https://dev.max.ru/docs-api/methods/GET/subscriptions) требует заголовок `Authorization` и использует MAX API с корневым сертификатом Минцифры:

```powershell
$env:MAX_FLEET_SECRETS_DIR = 'C:\MAXFleet\secrets'
$env:MAX_BOT_TOKEN_FILE = Join-Path $env:MAX_FLEET_SECRETS_DIR 'max_bot_token'
$env:MAX_FLEET_LIVE_MAX_SUBSCRIPTIONS = '1'
Push-Location services/gateway
& '..\..\.local\go-dist\go\bin\go.exe' test ./internal/maxsdk -run '^TestLiveMAXSubscriptions$' -count=1 -v
Pop-Location
Remove-Item Env:\MAX_BOT_TOKEN_FILE, Env:\MAX_FLEET_LIVE_MAX_SUBSCRIPTIONS -ErrorAction SilentlyContinue
```

- Для регистрации webhook после успешного HTTPS preflight используйте `/usr/local/bin/max-setup` внутри gateway-контейнера full stack. В удалённом `.env` заранее должны быть заданы `MAX_UPDATE_MODE=webhook` и `PUBLIC_BASE_URL=https://<домен>`; файлы `max_bot_token` и `max_webhook_secret` должны быть доступны Compose. Команда требует эти смонтированные token/secret-файлы и явный флаг `MAX_FLEET_CONFIGURE_MAX_WEBHOOK=1`. Она проверяет публичный HTTPS `/health/ready`, отказывается менять любую уже существующую подписку, регистрирует только `message_created`, `message_callback`, `bot_started`, затем перечитывает и сверяет результат. Пример после подготовки MAX overlay:

```powershell
docker compose --env-file .env -f deploy/compose.full.yaml -f deploy/compose.full.max.yaml -p max-fleet-prod up -d --build --wait
docker compose --env-file .env -f deploy/compose.full.yaml -f deploy/compose.full.max.yaml -p max-fleet-prod exec -e MAX_FLEET_CONFIGURE_MAX_WEBHOOK=1 gateway /usr/local/bin/max-setup
```

Не запускайте этот setup до успешной TLS-проверки или если в MAX уже есть подписка: CLI безопасно откажется от изменения. Он не меняет URL Mini App; этот адрес настраивается отдельно в MAX для бизнеса.

- Polling marker переносит границу прочитанных событий; сохранить пачку в inbox до продвижения marker. При рестарте продолжить от сохранённого значения; один poller на token. [GET updates](https://dev.max.ru/docs-api/methods/GET/updates).
- Кнопка карты использует `https://max.ru/<MAX_BOT_NAME>?startapp=<return_id>` для текущего возврата. После регистрации mini-app человек локально указывает имя одобренного бота без `@`; ID возврата в ссылке не даёт доступа без подписанного initData. Открытие deep link внутри настоящего MAX проверяется на INT-04. [Диплинки mini-app](https://dev.max.ru/docs/webapps/introduction).
- Mini-app подключается к боту через HTTPS URL, например PUBLIC_BASE_URL/map. Агент подготавливает URL и показывает человеку, какое поле настроить, если UI аккаунта недоступен. [Подключение mini-app](https://dev.max.ru/docs/webapps/introduction).
- MAX Bridge raw initData проверяется сервером; подпись, время и actor, никаких доверенных initDataUnsafe. [Валидация](https://dev.max.ru/docs/webapps/validation), [MAX Bridge](https://dev.max.ru/docs/webapps/bridge).
- Настроить outbound rate limiter по актуальным ограничениям MAX; учитывать 429/Retry-After, backoff и bounded retries. Потеря отправки не откатывает trip.

## 4. Секреты и переменные

[.env.example](../.env.example) содержит только имена, публичные defaults и пустые поля. Runtime должен принимать SECRET или SECRET_FILE, но не оба сразу; предпочтительны файлы, смонтированные read-only в /run/secrets. App bootstrap поддерживает это явно, не рассчитывает на неизвестную поддержку стороннего image.

| Переменная / группа | Кому нужна | Кто создаёт |
|---|---|---|
| APP_ENV, LOG_LEVEL, COMPANY_TIMEZONE | Go/Python | Агент; timezone подтверждает владелец |
| DATA_API_BASE_URL, CONTRACT_VERSION | Go | Агент; mock до INT, data-api после |
| MAX_BOT_TOKEN_FILE, MAX_WEBHOOK_SECRET_FILE, MAX_BOT_NAME | Только Go | Bot token и имя одобренного бота — человек локально; secret — агент криптографически. Имя без `@` используется только для deep link карты |
| MAX_PHOTO_HOSTS | Только Go | Агент после проверки доменов CDN MAX; точные HTTPS hostname через запятую, без URL, query и токенов |
| DATA_API_TOKEN_FILE, WORKER_API_TOKEN_FILE | Go/Python | Агент; отдельные значения для ролей |
| DATABASE_URL_FILE / MIGRATION_DATABASE_URL_FILE / POSTGRES_PASSWORD_FILE | Только Python/миграции/PostgreSQL | Агент; разные runtime/migration роли |
| S3_ENDPOINT, S3_BUCKET, S3_REGION | Python | Агент |
| S3_ACCESS_KEY_FILE, S3_SECRET_KEY_FILE | Python/S3 | Агент; приватный bucket и минимальные права |
| PUBLIC_BASE_URL, MAX_UPDATE_MODE | Go/proxy | Агент после H-02; mode=webhook/polling |
| MAP_TILE_URL, MAP_ATTRIBUTION, COMPANY_MAP_LAT/LON | Frontend public config | Агент/владелец города; это не место возврата |
| BOOTSTRAP_ADMIN_MAX_ID_FILE, PRIVATE_SEED_FILE | Одноразовый Python bootstrap | Человек вводит реальные значения приватно |

Безопасный порядок:

`MAX_PHOTO_HOSTS` по умолчанию пуст: фото-событие остаётся в durable inbox без ACK до настройки. Указывать только проверенные точные имена хостов из MAX image payload, например при приватном smoke на реальном боте; не добавлять wildcard, IP, подписанный URL или токен. Загрузчик не следует редиректам, отклоняет внутренние IP, ограничивает время/размер и не логирует source URL. До проверки реального MAX этот параметр не считается настроенным.

1. Агент создаёт приватный каталог и скрытый prompt; на Windows ограничивает ACL текущим пользователем, на Unix права 600/700.
2. Человек вводит только bot token и приватные исходные данные. Случайные service/DB/storage секреты агент генерирует сам, не печатая.
3. В .env записываются пути и несекретные параметры; secret contents не читаются в вывод инструментов. Если клон находится в OneDrive, каталог секретов размещается **вне синхронизируемой папки**.
4. Агент проверяет существование/доступность файла и безопасный API запрос. Не показывает cat .env, env, docker inspect или развёрнутый compose config с секретами.
5. Проверяет git check-ignore для secret paths внутри repo; staged files и secret scan. .dockerignore также исключает .env/secrets/backup.
6. При смене ноутбука человек один раз заполняет локальные secrets либо пользуется согласованным приватным менеджером; Git переносит только код.
7. При подозрении на попадание токена в Git/лог сначала отозвать/заменить у провайдера, затем убрать источник утечки. Простое удаление последнего файла не удаляет историю.

На Windows `scripts/bootstrap.ps1` создаёт восемь случайных service/DB/storage секретов с ACL текущего пользователя; существующие файлы не перезаписываются, значения не выводятся. Задайте `MAX_FLEET_SECRETS_DIR` абсолютным ASCII-путём вне репозитория, например `C:\MAXFleet\secrets`: Docker Desktop на этом устройстве смонтировал файлы из профиля с кириллицей как каталоги. Backend Compose получает пути только к двум локальным файлам `data_api_token` и `worker_api_token`; в PowerShell после bootstrap их можно задать без копирования значений:

```powershell
$env:MAX_FLEET_SECRETS_DIR = 'C:\MAXFleet\secrets'
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/bootstrap.ps1
$env:MAX_FLEET_DATA_API_TOKEN_FILE = Join-Path $env:MAX_FLEET_SECRETS_DIR 'data_api_token'
$env:MAX_FLEET_WORKER_API_TOKEN_FILE = Join-Path $env:MAX_FLEET_SECRETS_DIR 'worker_api_token'
docker compose -f deploy/compose.backend.yaml up --build -d --wait
```

Переменные путей и secret-файлы должны оставаться локальными; значения файлов не копируются в `.env`, Git или чат. Если Docker Desktop не может читать директорию с пользовательским ACL, `scripts/prepare-compose-token.ps1` создаёт защищённые копии рядом с каталогом секретов и печатает только путь. Токен MAX владелец вводит отдельно локальной командой `scripts/enter-max-token.ps1 -Enter`; `-Check` показывает только наличие, а скрипт использует `MAX_FLEET_SECRETS_DIR`, если он задан, иначе `%LOCALAPPDATA%\MAXFleet\secrets`. На Unix `scripts/bootstrap.sh` создаёт каталог secret-файлов с правами `700`; все смонтированные Compose secrets получают режим `444`, чтобы Go и Python контейнеры с UID `10001` могли их прочитать. Это нужно потому, что file-backed Compose secrets монтируются как bind mounts ([Docker Compose docs](https://docs.docker.com/compose/how-tos/use-secrets/)). Закрытый родительский каталог не позволяет другим пользователям хоста открыть файлы. Файл `max_bot_token` появляется только после безопасной передачи токена владельцем и хранится в этом же каталоге. CI использует временный каталог с правами `700` и не печатает значения секретов. Реальные DATABASE_URL и seed оформляются позже вместе с Python; значения не копируются в чат.

Проверки: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/verify.ps1 -Direction all` либо `sh scripts/verify.sh all`. Направления: `contract`, `gateway`, `web`, `docker`. Отсутствующий инструмент/Engine даёт ошибку, а не зелёный результат. Для pre-commit проверки выбранных файлов: `py scripts/check-secrets.py --staged`; CI сканирует tracked-файлы без печати содержимого.

Не отправлять настоящие фото/PII в CI artifacts, public Issues/PR или сторонние сервисы проверки. Локальные секреты этим пакетом документации не создавались.

## 5. Docker: три независимых контура

| Compose-файл | Сервисы | Для кого |
|---|---|---|
| deploy/compose.backend.yaml | gateway, data-mock, web/proxy | Backend A/B без БД |
| deploy/compose.data.yaml | data-api, worker, migrate, postgres, s3 | Data engineer без MAX |
| deploy/compose.full.yaml | gateway, web/proxy, data-api, worker, migrate, postgres, s3 | Только финальный INT |

Full и data конфигурации переиспользуют одинаковые pinned images/настройки сервисов; не поддерживать несовместимые копии миграций. Root .env.example — общий список имён. У каждого контура отдельное имя Compose project и volumes, чтобы тесты не затронули демо.

`data-worker` имеет Compose healthcheck процесса для поддержки `up --wait`; healthy подтверждает наличие дочернего процесса worker, но не проверяет обработку очереди. Для readiness API используйте `/health/ready`, а для поведения очереди — отдельный synthetic smoke.

Целевые команды:

```text
docker compose --env-file .env -f deploy/compose.backend.yaml -p max-fleet-backend up -d --build --wait
docker compose --env-file .env -f deploy/compose.data.yaml -p max-fleet-data up -d --build --wait
docker compose --env-file .env -f deploy/compose.full.yaml -p max-fleet-full up -d --build --wait
```

Не запускать все три окружения на общих портах/токене. Backend-mock не подключается к production MAX по умолчанию. По необходимости локальные debug-порты bind только 127.0.0.1.

### Локальный полный synthetic stack

В `compose.full.yaml` сервисы Data API/worker/migrate/PostgreSQL/S3 подключаются через `include` из `compose.data.yaml`, поэтому миграции и pinned images заданы в одном месте. На Windows:

```powershell
$env:MAX_FLEET_SECRETS_DIR = 'C:\MAXFleet\secrets'
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/bootstrap.ps1
$env:APP_ENV = 'development'
$env:MAX_UPDATE_MODE = 'webhook'
$env:MAX_INTEGRATION_KEY = 'demo-bot'
$env:SEED_SYNTHETIC = '1'
docker compose -f deploy/compose.full.yaml -p max-fleet-full up -d --build --wait
```

Карта доступна на `http://127.0.0.1:8081`. Если порт занят, задайте `WEB_PORT` (и `DATA_API_PORT` для loopback debug API). Postgres/S3 не публикуются. Без MAX bot token gateway принимает только synthetic входящие события без ответов; `/health/ready` сообщает `503 dialog flows incomplete`, а Compose проверяет `/health/live`. Этот synthetic запуск не подтверждает готовность реального MAX.

### Полный stack с настоящим MAX

Для настоящего MAX оставьте полный стек и подключите отдельный overlay с файлом токена бота. Базовый Compose останется пригоден для синтетических проверок и не подключает токен. Введите его локально скрытой командой `scripts/enter-max-token.ps1 -Enter`; заранее задайте `MAX_FLEET_SECRETS_DIR`, чтобы скрипт и Compose использовали один закрытый каталог. Не копируйте токен в `.env`, Git или чат. На Linux каталог создаётся `scripts/bootstrap.sh`; файлы получают права чтения, нужные UID `10001` контейнеров, а сам каталог закрыт для других пользователей правами `0700`.

```powershell
$env:MAX_FLEET_SECRETS_DIR = 'C:\MAXFleet\secrets'
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/bootstrap.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/enter-max-token.ps1 -Enter
$env:APP_ENV = 'production'
$env:MAX_UPDATE_MODE = 'webhook'
$env:SEED_SYNTHETIC = '0'
docker compose -f deploy/compose.full.yaml -f deploy/compose.full.max.yaml -p max-fleet up -d --build --wait
```

`compose.full.max.yaml` только монтирует `max_bot_token` для Go. Публичный HTTPS proxy отдельно направляет `/max/webhook` и карту на loopback-порт web; webhook регистрируется только после выдачи доверенного TLS-сертификата домену. Синтетическая готовность или HTTP без TLS не доказывают подключение настоящего MAX.

Smoke полного Data API через живые PostgreSQL и S3: задайте `MAX_FLEET_SECRETS_DIR`, `DATA_API_URL=http://127.0.0.1:18000`, затем выполните из корня:

```powershell
Push-Location services/data
uv run python scripts/smoke.py
Pop-Location
```

Опциональная Go-клиентская проверка Python API использует только синтетического сотрудника, создаёт идемпотентный 15-минутный hold и отменяет его:

```powershell
$env:MAX_FLEET_LIVE_DATA_API = '1'
$env:MAX_FLEET_LIVE_DATA_API_URL = 'http://127.0.0.1:18000/internal/v1'
$env:MAX_FLEET_LIVE_DATA_API_TOKEN_FILE = Join-Path $env:MAX_FLEET_SECRETS_DIR 'data_api_token'
$env:MAX_FLEET_LIVE_DATA_API_ACTOR = '8000000000000000001'
Push-Location services/gateway
go test ./internal/dataapi -run '^TestLivePythonDataAPI$' -count=1 -v
Pop-Location
```

Тест пропускается, если не включён `MAX_FLEET_LIVE_DATA_API=1`; токен читается из файла и не печатается. Реальный MAX/Bridge, gateway readiness, сервисные отказы и backup/restore остаются отдельными проверками.

Сквозной Go inbox/dialog → Python → PostgreSQL/S3 smoke на синтетических данных проходит возврат через диалог Go, ручной выбор точки картой, историю и S3-фото, а также подачу и разбор замечания администратором. Он проверяет 15-минутный hold, 8+8 фото, безопасное освобождение автомобиля, повтор сохранения `manual_map` с тем же ключом и запрет сотруднику на admin-команду. На отметке 4/8 тест пересоздаёт Go inbox worker, подтверждает восстановление прогресса из Python и проверяет, что повтор фото-события ссылается на ту же inbox row и не обрабатывается повторно. При `return.complete` тест пропускает реальный ответ Python после commit, возвращает Go синтетический 503 и сверяет, что retry повторяет тот же body/request/idempotency key, а поездка завершается один раз; затем отдельный вызов `return.complete` с тем же ключом возвращает сохранённый результат без увеличения версии завершённой поездки. Пересоздание worker-структур выполняется в тестовом процессе и пока не является проверкой перезапуска контейнера/ОС; сервисные рестарты/down/restore остаются отдельными INT-03 проверками. MAX transport, сотрудник/администратор и фото синтетические; это не проверка реального MAX. Используйте отдельный disposable Compose project и сотрудника без активной поездки. Значения токенов нигде не задаются: тест читает локальные `data_api_token`/`worker_api_token` из файлов.

```powershell
$env:MAX_FLEET_SECRETS_DIR = 'C:\MAXFleet\secrets'
$env:APP_ENV = 'development'
$env:MAX_UPDATE_MODE = 'webhook'
$env:MAX_INTEGRATION_KEY = 'demo-bot'
$env:SEED_SYNTHETIC = '1'
$env:WEB_PORT = '8084'
$env:DATA_API_PORT = '18003'
docker compose -f deploy/compose.full.yaml -p max-fleet-int-recovery up -d --build --wait --wait-timeout 360

$env:MAX_FLEET_LIVE_DIALOG = '1'
$env:MAX_FLEET_LIVE_DATA_API_URL = 'http://127.0.0.1:18003/internal/v1'
$env:MAX_FLEET_LIVE_DATA_API_ACTOR = '8000000000000000002'
$env:MAX_FLEET_LIVE_DATA_API_TOKEN_FILE = Join-Path $env:MAX_FLEET_SECRETS_DIR 'data_api_token'
$env:MAX_FLEET_LIVE_DATA_API_WORKER_TOKEN_FILE = Join-Path $env:MAX_FLEET_SECRETS_DIR 'worker_api_token'
Push-Location services/gateway
& '..\..\.local\go-dist\go\bin\go.exe' test ./internal/dialog -run '^TestLivePythonReturnDialog$' -count=1 -v
Pop-Location
```

Для проверки фактического restart Python API и сохранения синтетических PostgreSQL/S3 данных в том же изолированном проекте:

```powershell
docker compose -f deploy/compose.full.yaml -p max-fleet-int-recovery restart data-api
docker compose -f deploy/compose.full.yaml -p max-fleet-int-recovery up -d --wait --wait-timeout 90
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:18003/health/ready
docker compose -f deploy/compose.full.yaml -p max-fleet-int-recovery down
docker compose -f deploy/compose.full.yaml -p max-fleet-int-recovery up -d --build --wait --wait-timeout 360
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:18003/health/ready
```

`down` здесь намеренно запускается без `-v`: named volumes `pg_data`/`s3_data` должны остаться. Затем повторите live Go dialog test из блока выше. Это проверяет рестарт сервиса и сохранность volumes, но не заменяет backup/restore или restart Go-процесса посреди незавершённого возврата.

Отдельно можно проверить readiness при отказе зависимости на том же disposable проекте. Ожидается 503 при остановленной базе или S3, затем 200 после восстановления. Это проверка обнаружения недоступности и восстановления, а не доказательство атомарности команды, прерванной посреди операции:

```powershell
$compose = @('-p', 'max-fleet-int-recovery', '-f', 'deploy/compose.full.yaml')
docker compose @compose stop postgres
curl.exe -sS -o NUL -w '%{http_code}\n' http://127.0.0.1:18003/health/ready # 503
docker compose @compose up -d --wait --wait-timeout 180
curl.exe -sS -o NUL -w '%{http_code}\n' http://127.0.0.1:18003/health/ready # 200
docker compose @compose stop s3
curl.exe -sS -o NUL -w '%{http_code}\n' http://127.0.0.1:18003/health/ready # 503
docker compose @compose up -d --wait --wait-timeout 180
curl.exe -sS -o NUL -w '%{http_code}\n' http://127.0.0.1:18003/health/ready # 200
```

Осторожно с `services/data/scripts/backup-restore.sh`: в текущем виде он использует фиксированные Compose project names `max-fleet-data` и `max-fleet-restore`, а в конце удаляет target volumes через `down -v`. Перед запуском проверьте, что target содержит только disposable данные; не используйте существующий проект с нужными данными. В INT-03 backup/restore v1.13 выполнялся с уникальным временным target project, оригинальный DE-скрипт не менялся.

Используйте только синтетический seed/учётные записи. Если тест прервался и оставил поездку, создайте новый Compose project с отдельными свободными `WEB_PORT` и `DATA_API_PORT` (например, имя `max-fleet-int-recovery`, порты `8084` и `18003`); он получит собственные volumes и чистый seed, а прежнее состояние останется нетронутым. Секреты приложения MAX для этого smoke не нужны. Без MAX token `/health/ready` остаётся `503 dialog flows incomplete`.

Для локальной проверки входа webhook используется дополнительный `deploy/compose.backend.webhook.yaml`: он включает `MAX_UPDATE_MODE=webhook` поверх базового Compose, оставляя mock DataAPI и bind на `127.0.0.1`. Файл `MAX_FLEET_MAX_WEBHOOK_SECRET_FILE` должен быть приватным локальным файлом; для синтетического smoke допустим отдельный тестовый secret без настоящего MAX token. На Windows подготовить копии `data_api_token` и `worker_api_token` для Docker Desktop через `scripts/prepare-compose-token.ps1 -Name data_api_token` и `-Name worker_api_token`, затем передать их пути в `MAX_FLEET_DATA_API_TOKEN_FILE` и `MAX_FLEET_WORKER_API_TOKEN_FILE`. Запуск: `docker compose -f deploy/compose.backend.yaml -f deploy/compose.backend.webhook.yaml -p max-fleet-backend up -d --build --wait`. Без `MAX_BOT_TOKEN_FILE` webhook только сохраняет inbox; `/health/ready` остаётся 503. Не включать этот overlay на публичном сервере и не считать такой smoke проверкой реального MAX.

### Требования к контейнерам

- Multi-stage builds для Go/frontend, non-root, pinned versions/digests, lock files.
- PostgreSQL и S3 с named volumes; data-api/Go state не держат авторитетные данные в контейнерном filesystem.
- У data-api depends_on postgres healthy + migration job successful; proxy/gateway ready только после готовности dependencies. Порядка запуска без healthcheck недостаточно. [Docker startup order](https://docs.docker.com/compose/how-tos/startup-order/).
- Migration job имеет lock, не запускается одновременно в каждом worker.
- Worker имеет heartbeat/readiness; API liveness не проверяет внешнюю сеть, readiness проверяет нужные локальные зависимости.
- Только proxy публикует нужные HTTP(S) порты; PostgreSQL, Python и S3/admin UI доступны внутри сети.
- Ограничить body size, CPU/RAM, pool, retries и graceful shutdown. Не очищать volumes при обычном restart/update.
- Frontend сборка не содержит service/bot/DB keys; публичный runtime config отделён от secret-файлов.

## 6. Карты, данные и хранение

Для небольшого прототипа можно использовать OSM стандартные тайлы с атрибуцией, кешированием и без массового скачивания; это не сервис с гарантированной доступностью. MAP_TILE_URL задаётся конфигурацией. [Политика OSM tiles](https://operations.osmfoundation.org/policies/tiles/). Геокодинг адреса — P1, отказ геокодера не блокирует P0.

Локальное S3 — [SeaweedFS](https://github.com/seaweedfs/seaweedfs), один контейнер с persistent volume и явной аутентификацией; точный поддерживаемый digest и конфиг фиксируются в DE-01. Публичный бакет и режим без auth не использовать в full. При переходе на внешнее S3 изменяется adapter config и процедура резервирования, не доменные DTO.

Машины доступны только после заполнения координат и инструкции ключей. Если реальные параметры неизвестны, использовать явно синтетический seed, не придумывать рабочий регламент компании.

## 7. Резервирование, обновление и сбои

Для малого MVP на время согласованной копии остановить writers/обработку очередей, завершить транзакции, сохранить PostgreSQL и S3 с manifest/hashes, затем возобновить. Копии хранить приватно отдельно от основного диска. В DE-08/INT-03 восстановить на новые volumes и сверить строки, ссылки и выборку фото. Срок/частоту/место копий для пилота утвердить в H-04.

Обновление: backup → миграция совместимого типа → новые images → health/smoke. Откат image возможен только при совместимой схеме; иначе восстановление согласованной копии с явно указанным риском потери изменений после неё. Не запускать автоматически destructive downgrade.

| Сбой | Действие агента / системы |
|---|---|
| Docker не запущен / права отсутствуют | Показать одну конкретную локальную инструкцию; продолжить статические проверки |
| Token 401 / bot moderation pending | Не перебирать токены, не логировать; H-01; остальная работа продолжается |
| HTTPS/DNS неверен | Диагностика сертификата/маршрута; не отключать TLS-проверку |
| Webhook отписался / входящие прекратились | Сверить subscriptions и health, восстановить нужную подписку, отметить возможный пробел событий |
| DB/S3 down или диск заполнен | No false success; readiness=false; сохранить draft/очередь; восстановить сервис/место, повторить безопасно |
| MAX 429/5xx | Rate limiter/backoff, очередь и диагностика возраста; не откатывать trip |
| Невозможно доставить адресату | Delivery dead с причиной; данные поездки доступны администратору |
| Сбой карты и geo | Сохранить draft, дать контакт ответственного, не подставлять координаты |
| Другой ноутбук запустил тот же consumer | Остановить дублирующий dev consumer, сверить leases/режим; Git-очередь не заменяет runtime lease |
| Secrets отсутствуют на ноутбуке B | Один скрытый ввод; не восстанавливать значения из публичной истории |
| Нет внешнего доступа/сети | Локальные проверки и checkpoint; не заявлять успешный push/deploy |

## 8. Протокол успешной установки

В конце INT-06 сохранить в отчёте: code/contract SHA, migrations head, image digests, ОС/ресурсы, команды запуска/verify/backup/restore, режим webhook, фактические клиенты MAX, результаты smoke и известные ограничения. Вместо secret значений — только имена и безопасные пути.

Минимизация ручных действий — цель процесса. Регистрация, модерация, подтверждение приватных данных и действия на физическом телефоне могут потребовать человека; автоматизировать их результат можно только в пределах выданного доступа.
