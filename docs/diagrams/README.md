# Диаграммы

Исходники — Mermaid-блоки в Markdown; SVG сгенерированы из них 26.09.2026 с Mermaid CLI 12.0.0. При изменении источника обновлять соответствующую SVG-копию.

| Изображение | Источник |
|---|---|
| [CJM](cjm.svg) | [docs/CJM.md](../CJM.md) |
| [Архитектура](architecture.svg) | [docs/ARCHITECTURE.md](../ARCHITECTURE.md) |
| [Схема БД](database.svg) | [docs/DATABASE.md](../DATABASE.md) |
| [Состояния поездки](states.svg) | [PRODUCT_SPEC.md](../../PRODUCT_SPEC.md), раздел 16 |

GitHub показывает Mermaid прямо в исходных документах. SVG можно открыть отдельно, увеличить или вставить в презентацию. Это схемы целевого продукта, не отчёт о развёрнутой системе.

Для повторного экспорта скопировать содержимое нужного Mermaid-блока без ограждений в временный diagram.mmd и выполнить:

```text
npx --yes --package @mermaid-js/mermaid-cli@12.0.0 mmdc -i diagram.mmd -o diagram.svg --no-font-embed
```

При наличии локального Chrome можно указать Puppeteer config через -p; секретов в нём не должно быть. Документация инструмента: [Mermaid CLI](https://github.com/mermaid-js/mermaid-cli). Временные зависимости и PNG проверки в .local/ не являются частью приложения и не коммитятся.
