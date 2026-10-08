# OmniBox AI

Панель и OpenAI-совместимый API для локальных ИИ-движков на домашнем ПК. Версия **0.21.0**.

> Первая версия на Go: один файл `omnibox` для Windows и Linux. Последняя PowerShell-версия — ветка `powershell-0.17.1`.

Документация: [чат, OpenRouter и модели](docs/chat.md), [установка и обновление](docs/install.md), [подключение клиентов и NAS](docs/api.md), [модели и скорость](docs/models.md), [команда `ai`](docs/ai.md), [сон и пробуждение](docs/sleep.md).

## Что делает

- Веб-панель из четырёх разделов — Чат, Модели, Сервер, Настройки: запуск моделей, настройки для каждой модели с оценкой памяти и откатом при неудачном запуске, мониторинг ресурсов только при открытой вкладке.
- Встроенный чат: локальные модели и модели OpenRouter, веб-поиск через SearXNG, файлы (изображения, видео, аудио, Word, Excel, текст), шкала глубины размышлений, ассистенты; чаты хранятся на ПК со сроком хранения и закреплением.
- OpenRouter: ключ хранится зашифрованным, список моделей, баланс и расходы подтягиваются сами; избранные модели можно открыть клиентам `/v1` (по умолчанию выключено).
- OpenAI API `/v1` на порту панели (8800): запросы идут на запущенный движок под настоящим именем модели; одна генерация одновременно, короткая очередь для параллельных запросов клиентов.
- Движки — отдельные службы ОС: **PrismML** (`llama-server`, Bonsai PQ2 и обычные GGUF) и **Ollama**; слушают только loopback (8801, 8802).
- Клиенты: Jan, Pi, Open WebUI. Доступ из LAN и WireGuard; HTTPS — reverse proxy на NAS (`https://llm.fnx.red/v1`).
- Управление всем проектом: `ai on` / `ai off`, автозапуск с системой и движок при старте (`ai autostart`), обновление (`ai update` или кнопка в панели), полное удаление без следов (`ai uninstall`); сон ПК кнопкой в панели.
- Пресеты запуска, оценка размещения модели в VRAM и RAM, тест модели со скоростью tok/s, несколько ключей API с учётом использования.
- Панель на телефоне (PWA через `https://llm.fnx.red`), поиск по сохранённому содержимому запросов и экспорт сеанса.

Принципы: никакого фонового опроса в простое; запрос к API не запускает сервер и не загружает модель; после «Остановить» сервер остаётся остановленным; пароль, ключ API и настройки не теряются при обновлении.

## Установка и обновление

Одна команда и для новой установки, и для обновления ([подробнее](docs/install.md)). Мастер задаёт несколько вопросов — Enter принимает ответ по умолчанию; пароль панели и API key показываются один раз. Повторный запуск обновляет программу, сохраняя все настройки.

Windows (PowerShell от имени администратора; подойдёт и встроенный Windows PowerShell):

```powershell
gh release download --repo aafnx/omnibox-ai --pattern install.ps1 --dir $env:TEMP --clobber
powershell -NoProfile -ExecutionPolicy Bypass -File $env:TEMP\install.ps1
```

Linux:

```sh
gh release download --repo aafnx/omnibox-ai --pattern install.sh --dir /tmp --clobber && sudo sh /tmp/install.sh
```

Без `gh` скрипт спросит GitHub token с правом чтения репозитория; токен держится только в памяти. Скрипт проверяет SHA256 выпуска до запуска. Выпуск собирает workflow **Release** (вкладка Actions) из `main`: архивы для Windows и Linux, `SHA256SUMS` и оба скрипта.

## Устройство

| Путь | Назначение |
|---|---|
| `cmd/omnibox` | Бинарник: `omnibox serve` (панель и `/v1`), `omnibox engine <prismml\|ollama>` (служба движка) |
| `internal/config` | Единый `config.json`: панель, API, хранение, движки с откатом настроек |
| `internal/control` | Управление движками и фоновые операции панели |
| `internal/gateway` | `/v1`: ключ, очередь, учёт токенов, журналы запросов |
| `internal/host` | Хост службы движка: дерево процессов, журналы, `runtime.json` |
| `internal/panel` | HTTP-панель: вход, сессии, CSRF, статика |
| `internal/chats`, `internal/chatrun` | Чаты и ассистенты на ПК; ответ на сообщение чата: история, файлы, веб-поиск, поток |
| `internal/openrouter`, `internal/reasoning`, `internal/websearch`, `internal/attach`, `internal/library` | OpenRouter (ключ, модели, баланс, расходы), профили размышлений моделей, SearXNG, текст из документов, единый список моделей |
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
