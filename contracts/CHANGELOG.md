# Изменения контракта

## 1.9 — BE-09, 29.09.2026

- Уточнён поток из PRODUCT_SPEC §13.7: переход `open → in_progress` фиксирует actor в audit и назначает `assigned_to`, но принимает только `status`; комментарий и подтверждение не требуются. Терминальные статусы `resolved`/`known_nonblocking` по-прежнему требуют непустой комментарий и явное подтверждение.
- OpenAPI использует раздельные payload-схемы для взятия в работу и терминальных решений; добавлен пример команды и негативная проверка лишних/недостающих полей. Go client/mock отклоняют смешанный payload и сохраняют actor в audit.
- Это исправление уже опубликованного v1.8 до синхронизации с Python. Data engineer должен использовать v1.9; ветка `services/data/` этим изменением не затрагивается.

## 1.8 — BE-09, 29.09.2026

- Причина: среди нескольких администраторов не сохранялось, кто взял замечание в работу; Issue projection также не возвращала уже хранимые resolution_comment/resolved_by/resolved_at.
- Issue v1.8 возвращает nullable `assigned_to`, `resolution_comment`, `resolved_by`, `resolved_at`. При `issue.resolve` с переходом open→in_progress `assigned_to` назначает сервер из аутентифицированного admin actor; payload не принимает assignee. Терминальное решение сохраняет назначение и отдельно фиксирует resolver/comment/time.
- Go client/mock реализуют issue.resolve с admin ACL, issue.version CAS, строго заданным payload, one-use idempotency result, audit и восстановлением snapshot; терминальное решение создаёт одно уведомление `issue_resolved` каждому администратору. В Python требуется добавить nullable `assigned_to` FK, DTO-поля и назначение actor в транзакции до INT; `services/data/` этим checkpoint не менялся.

## 1.7 — BE-09, 29.09.2026

- Причина: одометр snapshot мог быть ошибочным, но прежний vehicle.correct_snapshot разрешал изменения только свободной машины. Проверка возврата при этом опиралась на immutable пробег before-inspection, поэтому правка snapshot не устраняла ODOMETER_ROLLBACK.
- vehicle.correct_snapshot теперь требует admin, reason, confirmation и CAS vehicle.version. При точной active holding/in_trip assignment допускается только odometer_km; операция не освобождает назначение, не переписывает checkout/trip/inspection и фото, а audit записывается атомарно с командой. Для свободной машины остаются прежние перечисленные поля.
- inspection.update и return.complete сравнивают ввод с текущим подтверждённым vehicle snapshot. Пока коррекции нет, он совпадает с показанием выезда; после коррекции сохраняется новое основание без изменения исторического inspection.
- Обновлены OpenAPI/meta/header, Go/mock и сценарии до 1.7. Python в codex/data не менялся; перед INT он должен синхронизировать active correction, транзакционные блокировки, audit, версии и проверки возврата с этим commit.

## 1.6 — BE-09, 29.09.2026

- Причина: v1.5 связывал admin close с поездкой, версией и причиной, но не связывал proof с `available_data`, хотя эти значения меняют возврат, состояние машины и её snapshot. Администратор мог подтвердить один набор пробега, топлива и координат, а сервер записал бы другой.
- `TripAdminCloseIntent` получает необязательный объект `available_data`; если он передан, JSON-команда `trip.admin_close` должна содержать тот же объект в canonical JSON. Его вложенные поля сортируются и входят в intent SHA-256. Отсутствие объекта отличается от объекта с данными; null запрещён. Несовпадение возвращает `422 CHALLENGE_INVALID`, не поглощая proof и не меняя доменные записи.
- Обновлены contract version, заголовок, meta, examples и сценарий tampered admin-close data до 1.6. Python в `codex/data` не изменён; перед INT он должен получить этот contract commit и включить `available_data` в challenge schema, intent hash и проверку итоговой команды.

## 1.5 — BE-09, 29.09.2026

- Причина: в v1.4 `ChallengeIntent` перечислял поля админских операций как необязательные. Python уже требует точный набор ключей, связывает решение с SHA-256 намерения, возвращает `challenge_proof_id=challenge.id` и допускает одно потребление proof; OpenAPI не гарантировал ту же границу.
- `ChallengeIntent` теперь `oneOf` operation-specific схем: block — `operation,target_id,expected_version,reason`; unblock добавляет только `review_completed=true`; grant требует `target_id=null`, `expected_version=null`, `max_user_id,display_name`; access требует `can_start_trip,reason`; admin close требует `reason`. Purpose и operation должны совпадать, лишние поля запрещены.
- Зафиксирована canonical JSON сериализация для SHA-256: UTF-8, сортировка ключей, компактные разделители, Unicode не экранируется. Успешный challenge answer возвращает его UUID как `challenge_proof_id`; итоговая административная команда несёт этот UUID в `challenge_id`, а сервер сверяет intent hash и поглощает proof однократно.
- Обновлены examples, сценарии, contract header/meta и Go client/mock version до 1.5. Это контрактный/mock шаг; Python в отдельной ветке не изменён и требует получить тот же contract SHA до INT.

## 1.4 — BE-08, 29.09.2026

- Причина: post-return диалог уже реализован на Go/mock, но восстановление после рестарта не было зафиксировано схемой; в выборе категорий отсутствовали случаи парковки и неисправного замка.
- Зафиксированы значения `Conversation.flow`, включая `issue_post_return`, и контекст отдельного разговора по собственной завершённой поездке: `target_id=trip_id`, vehicle snapshot version, категория, описание и до трёх trip-scoped фото. Отправка остаётся отдельным идемпотентным `issue.create` с `inspection_id=null`; завершённые Trip и after-inspection неизменны.
- Добавлены категории замечаний `parking` и `car_lock`. Mock и Go-клиент принимают их вместе с прежними категориями.
- HTTP/error semantics согласованы с реализацией Python: `RULES_REQUIRED` и `CHALLENGE_EXPIRED` — 422; read `/admin/*` для не-admin — `ACCESS_DENIED`, административные команды — `ADMIN_REQUIRED`; занятый MAX ID при `employee.grant` — `409 INVALID_STATE`. Возврат по-прежнему отдаёт Return, а фото/inspection update повышают родительскую версию по правилам v1.3.
- OpenAPI, fixtures, сценарии, contract version header/meta и Go mock переходят на 1.4. Python ветка требует получить этот же contract commit до INT; данный коммит не является доказательством её соответствия.

## 1.3 — BE-08, 29.09.2026

- Причина: сотрудник должен сообщить о замеченной после возврата проблеме, не меняя завершённый снимок поездки. У `issue.create` добавлен контекст собственной `completed` поездки: `trip_id` задан, `inspection_id=null`, отдельный Issue получает `stage=post_return`; машина блокируется для новой выдачи до разбора и admin получает уведомление. Версии Trip и finalized after-inspection не меняются. Чужой trip скрывается через 404. В сообщении не утверждается вина предыдущего водителя.
- Добавлены версия 1.3 OpenAPI и заголовка, пример `examples/post-return-issue.json`, сценарии успешного и чужого обращения. `return.complete` документирован с результатом Return, а фото — с повышением версии inspection и родительского checkout/return согласно фактическому mock и Python. Это уточнение прежних расхождений, не заявление об их интеграционной проверке.
- Go/mock переходят на 1.3 в BE-08. Python в отдельной ветке пока реализует 1.2; INT требует согласовать и проверить контракт на обеих сторонах до переключения Go на Python. Старые версии клиента явно отклоняются.

## 1.2 — BE-06, 28.09.2026

- Причина: `conversation.save` имел версию, но `/state` не возвращал сохранённый черновик. После рестарта Go не мог восстановить категорию, описание и до трёх staged asset IDs до `issue.create`.
- `/state` теперь требует `conversation` (`null` или сохранённый диалог). В `Conversation.context` добавлены `issue_category`, `asset_ids` (до 3 уникальных UUID) и `vehicle_version`. `target_id` содержит ID before-осмотра, `vehicle_id` и версия связывают черновик с исходным hold. `conversation.save` сохраняет эти поля по CAS `conversation_version`; каждый actor видит только собственный черновик. Повтор с тем же idempotency key возвращает тот же результат.
- Версии OpenAPI, `X-Contract-Version`, examples и сценариев обновлены до 1.2. Python-сервис должен реализовать расширение до INT; его код здесь не меняется. Старые клиенты 1.1 отклоняются явной ошибкой версии.

## 1.1 — BE-05, 28.09.2026

- Причина: v1.0 перечислял занятые ракурсы в `Trip`, но не позволял Go получить приватные байты конкретного фото без заранее известного `asset_id`. Это блокировало сравнение «До»/«После» администратором.
- Добавлен `GET /internal/v1/trips/{id}/inspection-photos/{phase}/{slot}` с `phase=before|after`, `slot=1..8`. Ответ — приватный JPEG/PNG/WebP поток; нет публичной ссылки или asset ID. Python проверяет actor из доверенного Go заголовка: только владелец поездки или admin. `before` доступно после finalized осмотра; `after` — после завершения/закрытия поездки и finalized after-осмотра. Чужая поездка, ещё недоступная фаза и пустой слот дают одинаковый `404 NOT_FOUND`; потерянный объект — `503 STORAGE_UNAVAILABLE`.
- Обновлены версия OpenAPI и `X-Contract-Version` до `1.1`, синтетический пример и сценарии ACL/фаз. Остальные поля и команды v1.0 не изменены. Go mock и Python должны реализовать один и тот же новый маршрут до INT; работа Python здесь не редактируется.

## 1.0 — S-02, 27.09.2026

- Внутренний контракт: [data-api.openapi.yaml](data-api.openapi.yaml), источник генерации [build_openapi.py](build_openapi.py). Отдельный внешний контракт карты Go: [map-api.openapi.yaml](map-api.openapi.yaml).
- Внутренний API содержит 36 путей и 24 discriminated-команды. UUID и MAX ID передаются строками, время — RFC3339 UTC; неизвестные JSON-поля отклоняются. `X-Contract-Version: 1.0` обязателен. DATA_API_TOKEN и WORKER_API_TOKEN различаются.
- Ключ идемпотентности команды относится к actor; повтор того же тела возвращает прежний результат, другой body/operation/target/version с тем же ключом даёт `IDEMPOTENCY_CONFLICT`. Worker-маршруты применяют route+key. Внутренний lease проверяется как fencing token. Результат чужой команды не раскрывается.
- `vehicle.version` увеличивается при изменении доступности/блокировки/замечания/снимка, `checkout.version` — при изменении оформления, `inspection.version` — при ответе/фото/подтверждении, `return.version` — при месте/отмене/завершении, `trip.version` — при переходах поездки и admin close, `conversation.version` — только при сохранении диалога. Фото не повышает версию checkout/return. При конфликте возвращается `STALE_VERSION` с актуальной версией; Go перечитывает агрегат перед итоговой кнопкой.
- Осмотры before и after требуют по 8 уникальных слотов. Лимиты: JPEG/PNG/WebP, 10 MiB и 25 MP на файл, описание до 1000 знаков. Ошибка файла не стирает остальные фото. Hold — 15 минут серверного времени. Admin close помечает недостающие сведения, не выдумывая фото или место.
- Экран карты передаёт raw `WebApp.initData` в заголовке `Authorization: MaxInitData <raw>` при каждом запросе. Go проверяет HMAC, `auth_date` до 1 часа и clock skew до 60 с, берёт actor из подписанных данных. Начальный центр карты имеет `selected=false`; только явный маркер может вызвать сохранение.
- Синтетические примеры находятся в `examples/`, 44 сценария — в `scenarios/v1.json`. `py contracts/validate.py` проверяет обе OpenAPI-схемы, 24 команды, семь остальных примеров, состав seed и сценарии. Сценарии пока являются спецификацией будущих mock/Python тестов; их выполнение на сервисах не заявлено.
- Источник типов MAX SDK: официальный `github.com/max-messenger/max-bot-api-client-go/v2`, тег `v2.4.1`, commit `b3b7025d53ee2a81b896a0b73ae8b02c672900d6`. В `go.mod` тега указан Go 1.24. Проверены `model.UpdateType` (`message_created`, `message_callback`, `bot_started`), `Message.Body.Mid`, `Callback.CallbackID`, `Attachment.Type` (`image`, `location`), координаты и `ChatTypeDialog`. Нормализация хранит message/callback ID, а для остальных событий — стабильный SHA-256 fingerprint; универсальный `update_id` не предполагается. Сборка SDK локально пока не выполнена: Go CLI отсутствует. [Документация SDK](https://dev.max.ru/docs/chatbots/bots-coding/go), [валидация initData](https://dev.max.ru/docs/webapps/validation), [официальный репозиторий](https://github.com/max-messenger/max-bot-api-client-go/tree/v2.4.1).
- Для math challenge добавлен `purpose=employee_grant`: выдача доступа в P0 требует проверки, хотя перечисление в проектном `docs/DATABASE.md` пока не содержит этого значения. Data engineer должен синхронизировать CHECK/enum миграции с контрактом до DE-04; его файлы здесь не менялись.

Изменение v1 после contract gate требует записи причины, обновления OpenAPI, examples, scenarios и версии при несовместимости. Python и mock должны получать один и тот же commit контракта.

Проверка на чистом checkout: `py -m pip install -r contracts/requirements-dev.txt`, затем `py contracts/validate.py` и `npx --yes @redocly/cli@2.54.3 lint contracts/data-api.openapi.yaml contracts/map-api.openapi.yaml --config redocly.yaml`. Эти команды проверяют описание API и fixtures; доменные сценарии запускаются позднее отдельно на mock и Python.
