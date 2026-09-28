# Изменения контракта

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
