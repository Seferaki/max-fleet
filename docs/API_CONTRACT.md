# Контракт Go ↔ mock ↔ Python

Проектный контракт v1.0, 26.09.2026. В S-02 этот документ превращается в `contracts/data-api.openapi.yaml` и JSON fixtures с зафиксированным commit. **OpenAPI и сервер ещё не реализованы.** Поведение определяется [PRODUCT_SPEC](../PRODUCT_SPEC.md), модель — [DATABASE](DATABASE.md).

## 1. Транспорт и доверие

- Внутренняя база URL: `http://data-api:8000/internal/v1`; mock: `http://data-mock:8000/internal/v1`. Go использует один HTTP-клиент; переключается только URL.
- Authorization: Bearer DATA_API_TOKEN подтверждает сервис Go. Python не публикуется наружу. Между разными хостами нужен TLS; plaintext допустим только в изолированной Docker-сети одного хоста.
- X-Actor-Max-ID — десятичная строка проверенного MAX ID. Go формирует её после валидации источника; не копирует клиентский заголовок. Python проверяет роль и владение по БД.
- X-Request-ID — UUID трассировки. X-Contract-Version: 1.0 — версия контракта. Несовместимую версию явно отклонять.
- Worker-маршруты требуют отдельный WORKER_API_TOKEN, не пользовательскую авторизацию.
- Каждая мутация принимает Idempotency-Key; существующий агрегат — expected_version. Тот же логический запрос после timeout получает тот же ключ; изменённый body — новый ключ.
- Команды из inbox дополнительно передают X-Inbox-Event-ID и X-Inbox-Lease. Python под блокировкой actor проверяет актуальный fencing token до изменения домена; просроченный worker не выполняет новую команду. Запрос карты проходит собственную авторизацию и version check, не притворяется inbox worker.
- Go: connect timeout 2 с, JSON deadline 5 с, streaming upload до 60 с. До трёх повторов с jitter для сетевых ошибок/429/503, тем же ключом и учётом Retry-After. Это стартовые настройки для проверки на INT.

UUID, MAX user ID и chat ID в JSON — строки. Время — RFC3339 UTC. Версии — положительные целые. Null и отсутствие поля различаются; неизвестные поля отклоняются. Все enums фиксируются в OpenAPI, не задаются произвольным текстом.

## 2. Ответы

Успех: {"data": {...}, "request_id": "uuid"}. Список: data.items и data.next_cursor (null в конце). limit по умолчанию 5, максимум 50; cursor непрозрачный и привязан к фильтрам. Изменяемые DTO содержат id, status, version, updated_at.

Ошибка: {"error":{"code":"HOLD_EXPIRED","message":"Время оформления истекло","retryable":false,"details":{"current_version":4}},"request_id":"uuid"}.

| HTTP | Коды | Действие Go |
|---|---|---|
| 400 | INVALID_REQUEST, UNSUPPORTED_EVENT | Попросить корректный ввод |
| 401 | INVALID_SERVICE_TOKEN; INVALID_INIT_DATA во внешнем Go API | Отказать, не раскрыть данные |
| 403 | ACCESS_DENIED, ADMIN_REQUIRED, CANNOT_START_TRIP | Показать отказ; заблокированному водителю оставить собственный возврат |
| 404 | NOT_FOUND | Одинаково для чужого и отсутствующего trip/asset |
| 409 | VEHICLE_UNAVAILABLE, USER_BUSY, STALE_VERSION, HOLD_EXPIRED, INVALID_STATE, IDEMPOTENCY_CONFLICT, COMMAND_IN_PROGRESS | Обновить состояние или подождать; не создавать новую команду автоматически |
| 413 / 415 | FILE_TOO_LARGE / UNSUPPORTED_MEDIA | Предложить допустимый файл, сохранить остальные фото |
| 422 | PHOTO_SET_INCOMPLETE, DUPLICATE_PHOTO, ODOMETER_ROLLBACK, LOCATION_REQUIRED, UNSAFE_RETURN, CHALLENGE_INVALID, CHALLENGE_EXPIRED, RULES_REQUIRED | Назвать недостающий шаг/поле; бизнес-состояние не продвигать |
| 429 | RATE_LIMITED | Retry-After |
| 503 | DATABASE_UNAVAILABLE, STORAGE_UNAVAILABLE, TEMPORARY_FAILURE | Повтор позже тем же ключом |

Не возвращать stack trace, SQL, secret, initData и приватные URL. Ошибка текущей команды не стирает ранее сохранённый черновик. При повторе idempotency key с другим body — 409. Сохранённый результат выдавать только после проверки текущей авторизации.

## 3. Чтение: GET относительно /internal/v1

| Путь | Результат / ограничения |
|---|---|
| /meta | contract_version, build_sha, mode=mock/real, capabilities; без секретов |
| /me | employee либо allowed=false + собственный MAX ID без данных автопарка |
| /state | Текущее checkout/trip/return, следующий шаг, conversation_version |
| /checkouts/{id} и /returns/{id} | Свой или разрешённый admin-контекст, актуальные version/step/inspection/location; основа восстановления сводки |
| /rules/current | id, version_label, body |
| /vehicles?available=true | Доступные машины; право повторно проверяется при подтверждении |
| /vehicles/{id} | Автомобиль, ключи, статус, snapshot с датами, known_nonblocking issues |
| /vehicles/{id}/previous-inspection | Последний finalized after-осмотр завершённой поездки; обезличенная проекция |
| /trips?scope=mine | Только свои состоявшиеся поездки; без отменённых попыток взятия |
| /trips/{id} | Владелец/admin: осмотры, issues, место, missing_data, отметки закрытия |
| /inspections/{id} | Разрешённый контекст, ответы, занятые/недостающие слоты |
| /assets/{id}/content | Авторизованный поток файла, не публичный URL |
| /vehicles/{id}/previous-inspection/photos/{slot} | Обезличенное фото; это право не открывает чужой trip/произвольный asset |
| /admin/summary | Количества доступных, trip, hold, ожидающих проверки; пересечения показателей явно определены |
| /admin/trips?state=&employee_id=&vehicle_id= | История всех поездок и P0-фильтры |
| /admin/employees | ФИО, MAX ID, права, текущая поездка |
| /admin/issues?status=&vehicle_id= | Описание, автор, этап, фото, решение |
| /issues/{id} и /admin/employees/{id} | Разрешённая карточка замечания / административная карточка сотрудника |
| /commands/{idempotency_key}?operation= | Результат своей команды; чужие ключи не раскрываются |

GET /health/live и /health/ready находятся вне /internal/v1. Публичная диагностика не показывает пароли, DSN и внутренние адреса.

## 4. Мутации: POST /commands

Envelope: {"operation":"checkout.create","target_id":"uuid","expected_version":1,"payload":{}}. Для создания без существующего target — target_id и expected_version=null. В S-02 каждая операция получает discriminated union schema с additionalProperties=false. Никакого произвольного CRUD/SQL по строке operation.

| Operation | Target / payload | Обязательная семантика |
|---|---|---|
| checkout.create | vehicle / {} | Hold 15 мин; actor и машина проверены атомарно |
| checkout.cancel | checkout / {} | Только своё holding; cancelled и release assignment |
| challenge.create | target или null / purpose, intent_payload | 4 options, operands, expires_at; без правильного ответа; bind к actor/объекту/версии |
| challenge.answer | challenge / selected_option | correct, attempts_remaining; новый пример после 3 ошибок/TTL; одноразовый proof |
| checkout.accept_rules | checkout / rules_version_id | После math; сохранить конкретную версию и время |
| inspection.update | inspection / разрешённые fuel_level, odometer_km, new_damage, cabin_clean, parking_allowed, keys_returned, car_locked | Проверить этап; изменить draft и сбросить итоговое подтверждение |
| inspection.confirm_photos | inspection / {} | Только при 8 сохранённых уникальных ракурсах |
| checkout.set_no_new_issues | checkout / value=true | Явный ответ; замечание сохраняется через issue.create |
| checkout.start | checkout / attestation=true | Проверить hold/math/правила/фото/данные/запреты; один trip после commit |
| trip.begin_return | trip / {} | returning, один draft return/inspection; далее math |
| return.cancel | return / {} | cancelled; trip.active; следующий возврат — новый пустой черновик |
| return.set_location | return / latitude, longitude, source, landmark?, confirmed=true | Сохранить только draft; source max_geo/manual_map из доверенного канала |
| return.complete | return / attestation=true | Атомарный trip.completed + snapshot; ключи/закрытие/парковка/8 фото/точка обязательны |
| issue.create | vehicle / category, description, trip_id?, inspection_id?, asset_ids[0..3] | Проверить один контекст; до выезда отменить hold, в поездке её сохранить; запретить новую выдачу |
| vehicle.block | vehicle / reason, challenge_id | manual_blocked; отменить hold, сохранить active trip |
| vehicle.unblock | vehicle / reason, review_completed, challenge_id | Нет нерешённых blocking issues; needs_review снимается явно |
| vehicle.edit | vehicle / description?, key_instructions?, confirmation=true | Только перечисленные поля и audit |
| vehicle.correct_snapshot | vehicle / reason, fuel_level?, odometer_km?, location?, confirmation=true | Только свободная машина; отдельная коррекция, включая обоснованное исправление odo; старые осмотры неизменны |
| vehicle.annotate | vehicle / reason, text, confirmation=true | Append-only уточнение в audit, применимо к активной машине |
| employee.grant | null / max_user_id, display_name, challenge_id | Новый employee; нельзя назначить admin; existing MAX ID — явный конфликт |
| employee.access | employee / can_start_trip, reason, challenge_id | Запрет новых поездок; holding отменить, active возврат сохранить |
| issue.resolve | issue / status, comment, confirmation=true | in_progress/resolved/known_nonblocking; исходное сообщение неизменно |
| trip.admin_close | trip / reason, challenge_id, available_data? | closed_by_admin, missing_data, needs_review=true; отсутствующие сведения не выдумывать |
| conversation.save | actor state / flow, step, context, pending_input_kind? | CAS conversation_version; навигация не меняет бизнес-права |

После math take/return Python одноразово записывает intent_confirmed_at в оформление; второй пример на итоговой кнопке не требуется. Для admin challenge.answer не выполняет административное действие: итоговая команда потребляет challenge_id в своей транзакции. Hash покрывает операцию, объект, версию и критический payload без самого challenge_id. Изменились причина/ID/права — новый proof.

У photo/inspection/return отдельные версии; сохранение фото не должно ломать оформление из-за собственной промежуточной записи. Перед итоговым start/complete получить актуальный агрегат и показать сводку. Семантику version bump по каждой операции закрепить в OpenAPI fixtures.

## 5. Фотографии: multipart

| Маршрут | Поля | Результат |
|---|---|---|
| POST /inspections/{id}/photos/{slot} | image, expected_version, source_event_key, Idempotency-Key | Ready asset и обновлённый inspection; замена только выбранного ракурса draft |
| POST /assets/stage | image, purpose=issue, scope_type, scope_id, source_event_key, Idempotency-Key | Stage asset_id данного actor/контекста; issue.create связывает до 3 ID |

Go получает фото от MAX, ограничивает доверенные источники/redirects и stream-передаёт Python. Произвольный клиентский URL для скачивания сервером не принимается; loopback/private/link-local запрещены. Реальные домены CDN проверяются по SDK/fixtures, не угадываются.

Python проверяет сигнатуру/MIME, декодирование, 10 MiB/25 MP, SHA-256; сохраняет приватный объект; затем привязывает к ракурсу. Asset для issue принадлежит actor и совпадает по scope, не используется другой сущностью. При ошибке создания issue stage-фото остаются до retry/TTL. Текст, видео, документ и сообщение с несколькими картинками не занимают очередной ракурс.

## 6. Технические маршруты очередей

| Метод / путь | Контракт |
|---|---|
| POST /inbox | integration_key, event_key, event_type, actor_max_user_id, нормализованный payload; duplicate → прежний ID |
| POST /inbox/claim | worker_id, max_items; lease_token, expiry; последовательность по actor |
| POST /inbox/{id}/ack | done только с текущим lease_token |
| POST /inbox/{id}/retry | error_code, next_attempt_at; просроченный lease не может менять запись |
| GET /integrations/{key} | mode/marker, без секретов |
| POST /integrations/{key}/lease | Получить/продлить single-poller lease |
| POST /integrations/{key}/checkpoint | CAS marker, только после durable записи всех событий пачки |
| POST /notifications/claim | delivery_id, event snapshot, recipient, lease_token |
| POST /notifications/{id}/ack | sent + provider_message_id, проверка lease |
| POST /notifications/{id}/retry | retry/dead, error_code, retry_after; домен не откатывается |

Технические мутации также имеют Idempotency-Key. Повтор claim возвращает прежнюю аренду, пока она действительна. После expiry другой worker может получить запись; устаревший token больше не валиден. Lease token не логируется.

У MAX не предполагается универсальный update_id: ключ message event составляется из message ID и типа, callback — callback ID и типа; другие события получают стабильный fingerprint нормализованных полей. Пакет polling не подтверждается новым marker до сохранения всех событий. Предел гарантий: доставка уведомлений at-least-once; бизнес-команда идемпотентна.

## 7. Внешний API карты — только Go

POST /api/v1/returns/{id}/location: проверенное raw initData, Idempotency-Key, expected_version, latitude, longitude, landmark?, confirmed=true.

GET /api/v1/returns/{id}/context с той же проверкой initData возвращает разрешённые return/version, последнюю парковку машины и центр города. Это только начальный контекст карты; поле selected=false до собственного действия пользователя. Go получает данные из внутреннего /returns/{id} и /vehicles/{id}.

В S-02 закрепить перенос initData через Authorization: MaxInitData либо отдельное поле тела при ограничении длины заголовка. Никогда не query-параметр. Go валидирует [подпись MAX](https://dev.max.ru/docs/webapps/validation), сравнивает constant-time, проверяет auth_date (до 1 ч, clock skew 60 с). initDataUnsafe и присланный user_id не используются для авторизации.

Go получает actor из подписанных данных; Python проверяет владение return, состояние draft и version. Подменённый/просроченный/чужой return отклоняется. CORS только PUBLIC_BASE_URL, CSP, отсутствие секретов в VITE_*. Выбор точки в UI сохраняется при временной ошибке; центр карты не отправляется без явного выбора.

## 8. Fixtures для независимой разработки

| Набор | Случаи |
|---|---|
| Identity | employee, admin, blocked driver с active trip, неизвестный actor |
| Vehicles | free, holding, active+blocked, без места/ключей, known_nonblocking issue |
| Checkout | happy path, busy, expired, победитель/проигравший гонки, cancelled, issue-before |
| Inspection | 0/7/8 фото, duplicate event/hash, replace slot, extra photo, multi-image, S3 error |
| Return | successful, damage allowed, unsafe denied, cancelled→new ID, map failure, wrong owner, admin close |
| Delivery | DB down before ack, timeout after commit, lease expiry, MAX 429/5xx/blocked recipient |
| Schema | malformed UUID/ID, unknown enum, null vs omitted, limit, stale version, same key/different body |

Контракт меняется сначала здесь/OpenAPI/examples с новой версией и причиной, затем в обеих реализациях. До INT проверки запускаются отдельно на mock и Python. На INT одинаковые сценарии сравнивают поля и ошибки; одного HTTP 200 недостаточно.
