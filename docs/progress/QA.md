# Прогресс QA

Основной документ — [QA_REQUIREMENTS](../QA_REQUIREMENTS.md). По прямому решению владельца QA-01…QA-03 пропущены в текущем MVP-сеансе и остаются NOT RUN. Это не закрывает QA и не означает приёмку продукта; никакие QA проверки не считаются пройденными.

```yaml
status_schema: 1
track: qa
owner: null
branch: codex/qa
current_task: QA-01
requirements_version: "1.0"
tested_code_commit: null
tested_contract_commit: null
environment: none
qa_ready_for_integration: false
release_accepted: false
next_step: "В текущем MVP-сеансе не запускать QA по решению владельца; оставить QA-01…QA-03 NOT RUN, qa_ready_for_integration=false и release_accepted=false"
```

| ID | Статус | Commit / evidence | Следующий подшаг |
|---|---|---|---|
| QA-01 | TODO | — | Кейсы и матрица покрытия |
| QA-02 | TODO | — | Отдельная проверка mock backend и Python |
| QA-03 | TODO | — | Подготовка финальной приёмки |

## Покрытие и дефекты

Заполнять ссылки на cases/report по мере выполнения. Для результата обязательны режим (mock / data-only / full-synthetic / full-MAX), commit, дата и клиент. PASS на mock не переносится автоматически в full-MAX.

| Дефект | Требование / AC | Серьёзность | Commit / режим | Статус |
|---|---|---|---|---|
| — | — | — | — | Проверки ещё не начаты |

## Финальная приёмка INT-05 / INT-06

Не выполнялась. QA-01…QA-03 пропущены по решению владельца для ускорения MVP; оставить все результаты NOT RUN. При отдельной приёмке записать релизный SHA, матрицу AC-01…AC-27, реальные результаты безопасности/гонок/restart, проверенные клиенты MAX, известные ограничения и решение владельца.
