# Каркас Go

`cmd/gateway` пока обслуживает только `/health/live` (200) и `/health/ready` (503). `cmd/data-mock` обслуживает `/meta`, `/me`, `/vehicles`, `/vehicles/{id}` из синтетического seed с проверкой service token, версии контракта и actor. Частично работают `checkout.create` и `checkout.cancel`: hold длится 15 минут, версии и успешные повторы Idempotency-Key проверяются в памяти. `/health/ready` остаётся 503: остальные команды, snapshot и восстановление ещё не реализованы. `APP_ENV=production` запрещает запуск mock.

Версия Go фиксируется в `go.mod`; официальный SDK MAX закреплён на `v2.4.1`. Локальные проверки после установки Go: из этого каталога `go test ./...`, `go vet ./...`, `go build ./cmd/gateway ./cmd/data-mock`. Для запуска mock задайте ровно один из `DATA_API_TOKEN` (только локальный dev) или `DATA_API_TOKEN_FILE` (предпочтительно, приватный файл). MAX и Python-сервис пока не подключены.
