# Промпт финального соединения

Запускать после BE-12, DE-09 и QA-03. Это последний этап общего плана.

```text
Выполни INT-01…INT-06 для MAX Fleet.

Сначала прочитай AGENTS.md, docs/HANDOFF.md, docs/IMPLEMENTATION_PLAN.md,
docs/API_CONTRACT.md, docs/OPERATIONS.md и все три docs/progress/*.md.
Проверь по code/contract SHA и тестам оба gate BACKEND_READY_FOR_INTEGRATION
и DATA_READY_FOR_INTEGRATION, а также готовность QA. Если gate ложный,
не объявляй интеграцию начатой: закрой доступную недостающую задачу или
точно назови внешний блокер, продолжая независимую работу.

Захвати backend-очередь. Создай codex/integration от готового backend
и слей data/QA ветки без force push и потери изменений. Сверь контракт,
enums, миграции, имена конфигурации. Создай deploy/compose.full.yaml
с gateway, frontend/proxy, Python API/worker/migrate, PostgreSQL и private S3.

Переключи Go с mock на Python через DATA_API_BASE_URL; SQL в Go не добавляй.
Проверь meta.mode=real, readiness, отсутствие test auth и публичных DB/S3 портов.
Пройди сквозной сценарий и fault/concurrency/restart/backup проверки,
сначала с simulated MAX transport, затем с реальным MAX через HTTPS.
Не включай скрытый fallback на mock при ошибке Python.

Организационные действия запрашивай по HUMAN_REQUIRED: краткие шаги/ссылка
и причина, почему нужен человек. Secret values в чат не запрашивай.
Новые платные ресурсы создавай только после выбора/авторизации владельца.

QA должен проверить mobile/web MAX, ручную карту без GPS и весь P0.
Исправь блокирующие дефекты, повтори затронутые проверки, зафиксируй ограничения.
Подготовь повторяемый Docker запуск на чистом checkout, backup/restore,
демонстрацию и фактический README. Сохраняй checkpoint commits/push.

В конце укажи release SHA, реальные результаты, известные ограничения и
что осталось, если внешний доступ блокирует завершение. Не отмечай
full_stack_accepted и release_accepted, пока критерии INT-06 не выполнены.
```
