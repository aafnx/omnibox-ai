# OmniBox AI

Панель и OpenAI-совместимый API для локальных ИИ-движков на домашнем ПК. Версия **0.17.1**.

> Идёт переход на Go: один бинарник `omnibox` для Windows и Linux. PowerShell-версия удалена из `main`; последняя рабочая PowerShell-версия — ветка `powershell-0.17.1`. Установщика Go-версии пока нет — он появится в выпуске, который заменит 0.17.1 (этап G в [плане](docs/roadmap.md)).

## Что делает

- Веб-панель: запуск и остановка движков, выбор модели, настройки с откатом при неудачном запуске, мониторинг ресурсов только при открытой вкладке.
- OpenAI API `/v1` на порту панели (8800): запросы идут на запущенный движок под настоящим именем модели; одна генерация одновременно, короткая очередь для параллельных запросов клиентов.
- Движки — отдельные службы ОС: **PrismML** (`llama-server`, Bonsai PQ2 и обычные GGUF) и **Ollama**; слушают только loopback (8801, 8802).
- Клиенты: Jan, Pi, Open WebUI. Доступ из LAN и WireGuard; HTTPS — reverse proxy на NAS (`https://llm.fnx.red/v1`).

Принципы: никакого фонового опроса в простое; запрос к API не запускает сервер и не загружает модель; после «Остановить» сервер остаётся остановленным; пароль, ключ API и настройки не теряются при обновлении.

## Устройство

| Путь | Назначение |
|---|---|
| `cmd/omnibox` | Бинарник: `omnibox serve` (панель и `/v1`), `omnibox engine <prismml\|ollama>` (служба движка) |
| `internal/config` | Единый `config.json`: панель, API, хранение, движки с откатом настроек |
| `internal/control` | Управление движками и фоновые операции панели |
| `internal/gateway` | `/v1`: ключ, очередь, учёт токенов, журналы запросов |
| `internal/host` | Хост службы движка: дерево процессов, журналы, `runtime.json` |
| `internal/panel` | HTTP-панель: вход, сессии, CSRF, статика |
| `internal/engine`, `internal/status`, `internal/telemetry`, `internal/platform` | Адаптеры движков, статус, ресурсы, службы и файлы ОС |
| `web/` | Интерфейс панели (встраивается в бинарник) |

Подробности — [`docs/architecture-go.md`](docs/architecture-go.md), план — [`docs/roadmap.md`](docs/roadmap.md), правила для агентов — [`AGENTS.md`](AGENTS.md).

## Сборка и проверка

Нужен Go из `go.mod`.

```sh
gofmt -l . ; go vet ./... ; GOOS=windows go vet ./... ; go test ./...
CGO_ENABLED=0 GOOS=windows go build -o omnibox.exe ./cmd/omnibox
node tests/web_test.cjs
```
