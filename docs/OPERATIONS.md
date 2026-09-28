# Настройка, Docker и минимальные действия человека

S-03 добавила локальные `scripts/bootstrap.ps1`, `scripts/bootstrap.sh`, `scripts/verify.ps1`, `scripts/verify.sh` и Dockerfile каркаса. Они не означают готовность приложения: Compose и доменные сервисы появятся в BE-01, DE-01 и INT. Агент выполняет технические шаги сам; человек нужен для своих аккаунтов, приватных данных, оплаты/решений владельца и устройств.

## 1. Human gates

| ID | Минимальное действие человека | Что делает агент | Что можно продолжить без этого |
|---|---|---|---|
| H-01 | Войти в MAX для партнёров, подтвердить профиль при необходимости, создать/модерировать бота; ввести token локально скрытым вводом | Подготовить название/описание, ссылки на нужные страницы, secret-файл, проверку /me, интеграцию | Весь backend mock, Python, QA |
| H-02 | Предоставить существующий сервер/домен или выбрать и оплатить хостинг; выдать нужный доступ, подтвердить DNS/первый запуск Docker при необходимости | Docker/Compose, HTTPS, конфигурация proxy, health, deploy и проверка | Локальные отдельные контуры и full-synthetic |
| H-03 | Передать приватно список машин/сотрудников/admin ID, процедуру ключей, правила и город/часовой пояс | Проверить формат, импортировать seed, создать администратора, проверить доступность карточек | Разработка и демо на синтетических данных |
| H-04 | Утвердить для реального пилота срок хранения фото/истории, круг доступа, владельца backup и правила сотрудников | Реализовать утверждённую конфигурацию, процедуру удаления/архивирования и эксплуатационные проверки | Демонстрация без реальных сотрудников и фото |

H-01/H-02 блокируют только INT-04 с живым MAX. H-03/H-04 нужны до эксплуатации на настоящих сотрудниках; не требуют останавливать проектирование или тестирование.

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

## 2. Создание бота MAX

По [официальной инструкции создания](https://dev.max.ru/docs/chatbots/bots-create/create), проверенной 26.09.2026, сервис доступен через верифицированный профиль организации, ИП или самозанятого. Бот проходит модерацию; документация указывает до 48 часов по рабочим дням. Срок проекта не должен предполагать мгновенную выдачу доступа.

1. Человек проверяет доступ в [MAX для партнёров](https://business.max.ru) или «MAX для бизнеса», создаёт/верифицирует профиль.
2. В разделе чат-ботов создаёт бота с подготовленными агентом названием, описанием и подходящим логотипом. Пример описания: «Служебный автопарк: оформление поездок, осмотры автомобиля, возврат и сообщения ответственному».
3. Дожидается модерации. Агент записывает внешний блокер, но продолжает изолированную разработку.
4. После одобрения токен копируется из настроек в локальный secret-файл; порядок описан в [управлении ботом](https://dev.max.ru/docs/chatbots/bots-create/manage).
5. Агент проверяет /me через настроенный SDK и выводит только успех и несекретную идентификацию нужного бота. Обновление токена выполняется в том же интерфейсе владельца, затем агент проверяет новый secret.
6. Первый admin и тестовые пользователи открывают личный диалог, чтобы бот мог получать их действия и отправлять предусмотренные уведомления. ID администратора вводится в приватный bootstrap; аккаунт GitHub не определяет роль MAX.

Не использовать инструкции Telegram/BotFather и не считать публично найденный token пригодным. При ограниченном доступе хакатона уточнить у организаторов предоставленный способ подключения; не придумывать обход регистрации.

## 3. MAX, webhook и mini-app

- Использовать официальный [Go SDK](https://dev.max.ru/docs/chatbots/bots-coding/go), pin release/commit и проверить API base URL по текущей документации. Токен передаётся заголовком Authorization, не URL.
- Для production — webhook; polling только для разработки. Они не работают одновременно. [Режимы получения событий](https://dev.max.ru/docs/chatbots/bots-coding/prepare).
- Webhook требует HTTPS с доверенным сертификатом. Зарегистрировать URL /webhooks/max и secret, проверять X-Max-Bot-Api-Secret. MAX ожидает HTTP 200 в течение 30 секунд; наш целевой ответ — после быстрой durable записи inbox, до тяжёлого скачивания фото. Неуспешная durable запись → 503. [Контракт webhook](https://dev.max.ru/docs-api/methods/POST/subscriptions).
- Перед сменой режима прочитать текущие subscriptions. Не удалять подписки неизвестного назначения. Идемпотентно привести только конфигурацию этого проекта к выбранному режиму.
- Polling marker переносит границу прочитанных событий; сохранить пачку в inbox до продвижения marker. При рестарте продолжить от сохранённого значения; один poller на token. [GET updates](https://dev.max.ru/docs-api/methods/GET/updates).
- Mini-app подключается к боту через HTTPS URL, например PUBLIC_BASE_URL/map. Агент подготавливает URL и показывает человеку, какое поле настроить, если UI аккаунта недоступен. [Подключение mini-app](https://dev.max.ru/docs/webapps/introduction).
- MAX Bridge raw initData проверяется сервером; подпись, время и actor, никаких доверенных initDataUnsafe. [Валидация](https://dev.max.ru/docs/webapps/validation), [MAX Bridge](https://dev.max.ru/docs/webapps/bridge).
- Настроить outbound rate limiter по актуальным ограничениям MAX; учитывать 429/Retry-After, backoff и bounded retries. Потеря отправки не откатывает trip.

## 4. Секреты и переменные

[.env.example](../.env.example) содержит только имена, публичные defaults и пустые поля. Runtime должен принимать SECRET или SECRET_FILE, но не оба сразу; предпочтительны файлы, смонтированные read-only в /run/secrets. App bootstrap поддерживает это явно, не рассчитывает на неизвестную поддержку стороннего image.

| Переменная / группа | Кому нужна | Кто создаёт |
|---|---|---|
| APP_ENV, LOG_LEVEL, COMPANY_TIMEZONE | Go/Python | Агент; timezone подтверждает владелец |
| DATA_API_BASE_URL, CONTRACT_VERSION | Go | Агент; mock до INT, data-api после |
| MAX_BOT_TOKEN_FILE, MAX_WEBHOOK_SECRET_FILE | Только Go | Bot token — человек локально; secret — агент криптографически |
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

На Windows агент запускает `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/bootstrap.ps1`: восемь случайных service/DB/storage секретов создаются в `%LOCALAPPDATA%\MAXFleet\secrets` с ACL текущего пользователя, существующие не перезаписываются. Токен MAX вводит владелец отдельно через `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/enter-max-token.ps1 -Enter`; `-Check` показывает только наличие. На Unix `sh scripts/bootstrap.sh` создаёт те же service/DB/storage секреты в `${XDG_DATA_HOME:-$HOME/.local/share}/max-fleet/secrets` с правами 700/600. Реальные DATABASE_URL и seed оформляются позже вместе с Python, значения не копируются в чат.

Проверки: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/verify.ps1 -Direction all` либо `sh scripts/verify.sh all`. Направления: `contract`, `gateway`, `web`, `docker`. Отсутствующий инструмент/Engine даёт ошибку, а не зелёный результат. Для pre-commit проверки выбранных файлов: `py scripts/check-secrets.py --staged`; CI сканирует tracked-файлы без печати содержимого.

Не отправлять настоящие фото/PII в CI artifacts, public Issues/PR или сторонние сервисы проверки. Локальные секреты этим пакетом документации не создавались.

## 5. Docker: три независимых контура

| Compose-файл (будет создан) | Сервисы | Для кого |
|---|---|---|
| deploy/compose.backend.yaml | gateway, data-mock, web/proxy | Backend A/B без БД |
| deploy/compose.data.yaml | data-api, worker, migrate, postgres, s3 | Data engineer без MAX |
| deploy/compose.full.yaml | gateway, web/proxy, data-api, worker, migrate, postgres, s3 | Только финальный INT |

Full и data конфигурации переиспользуют одинаковые pinned images/настройки сервисов; не поддерживать несовместимые копии миграций. Root .env.example — общий список имён. У каждого контура отдельное имя Compose project и volumes, чтобы тесты не затронули демо.

Целевые команды **после соответствующих задач**, одинаковые в PowerShell и Unix:

```text
docker compose --env-file .env -f deploy/compose.backend.yaml -p max-fleet-backend up -d --build --wait
docker compose --env-file .env -f deploy/compose.data.yaml -p max-fleet-data up -d --build --wait
docker compose --env-file .env -f deploy/compose.full.yaml -p max-fleet-full up -d --build --wait
```

Не запускать все три окружения на общих портах/токене. Backend-mock не подключается к production MAX по умолчанию. По необходимости локальные debug-порты bind только 127.0.0.1.

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
