# OmniBox AI

Светлая универсальная панель управления локальными ИИ-серверами на Windows. Версия **0.5.0**. Открывай её с ноутбука или смартфона, запускай движки, меняй настройки и управляй загрузкой моделей. Для работы с нейросетью используется отдельный клиент через API.

## Возможности

- Запуск, остановка и перезапуск серверов; **Остановить все серверы**.
- Загрузка и выгрузка моделей, скачивание Ollama-моделей и GGUF на ПК.
- Контекст и время простоя; GPU layers, Flash Attention, KV cache и batch для PrismML.
- Сохранение параметров для следующего запуска или применение сейчас, с откатом при ошибке запуска.
- Состояние служб, моделей, памяти и журналы; команды подключения для внешних клиентов.
- Явные поля OpenAI Base URL, Model и API key для Jan/Pi; поддержка HTTPS reverse proxy на NAS.
- Отдельный пароль панели; CLI `ai` остаётся для диагностики и восстановления.

## Движки

| Движок | Служба в текущей конфигурации | API на ПК |
|---|---|---|
| PrismML `llama-server`, включая Bonsai PQ2 | `bonsai` | `http://127.0.0.1:18080/v1` |
| Ollama | `ollama` | `http://127.0.0.1:11434/v1` |

Панель управляет отдельными движками. Форматы моделей и их API остаются собственными; панель не является API-прокси. Имена служб и пути задаются в `config.json`. Параметры моделей читаются из реального XML WinSW.

Требуются Windows, PowerShell **7.4+** и уже настроенные службы движков через WinSW. Программа не устанавливает runtime, драйверы или WinSW. Python, Node.js, Docker и WSL для работы панели не нужны.

## Установка одним скриптом на ПК

Запусти строку в повышенной SSH-сессии **на серверном ПК**, в PowerShell 7. Репозиторий приватный: создай [GitHub token](https://github.com/settings/personal-access-tokens/new?name=omnibox-ai-install&target_name=aafnx&expires_in=1&contents=read) только для `omnibox-ai`, с **Contents: Read-only**. Токен вводится скрыто и не сохраняется.

```powershell
& { $ErrorActionPreference = 'Stop'; $token = Read-Host 'GitHub token (omnibox-ai: Contents Read)' -AsSecureString; $headers = @{}; $file = Join-Path $env:TEMP ('omnibox-ai-install-' + [Guid]::NewGuid().ToString('N') + '.ps1'); try {     $headers.Authorization = 'Bearer ' + ([pscredential]::new('github', $token)).GetNetworkCredential().Password; $headers.Accept = 'application/vnd.github+json'; $package = Invoke-RestMethod -Uri 'https://api.github.com/repos/aafnx/omnibox-ai/contents/dist/install-omnibox-ai.ps1?ref=main' -Headers $headers -MaximumRedirection 0 -TimeoutSec 60; if ($package.type -ne 'file' -or $package.encoding -ne 'base64') { throw 'GitHub не вернул установочный файл.' }; [IO.File]::WriteAllBytes($file, [Convert]::FromBase64String($package.content)); $headers.Clear(); & $file } finally {     $headers.Clear(); $token.Dispose(); if ([IO.File]::Exists($file)) { [IO.File]::Delete($file) } } }
```

Мастер спросит папку (`C:\tools\ai`), сервер по умолчанию, контекст, запуск сейчас, адрес/порт панели, разрешённые сети и пароль. Подтверждения: `yes/no` или `y/n`; Enter сохраняет предложенное значение. Proxy можно оставить выключенным до настройки NAS. По умолчанию ИИ-сервер не запускается. Панель устанавливается отдельной службой `omnibox-ai` с автоматическим запуском.

После установки открой адрес, показанный мастером. Для текущего OmniBox при выборе `192.168.1.77` и стандартного порта: **`http://192.168.1.77:18090`**. Извне используй существующий VPN или SSH-туннель. Подробности: [установка и обновление](docs/install.md), [веб-панель и подключение](docs/web-panel.md).

## Ресурсы и простой

Панель не загружает модель при открытии и не опрашивает GPU постоянно. Статус обновляется только в видимой вкладке; `nvidia-smi` вызывается при входе и ручном обновлении. Сама служба панели занимает CPU/RAM PowerShell/.NET.

Движки с ручным запуском остаются остановленными до команды пользователя. Таймаут простоя задаётся в настройках, для текущей установки — **30 минут**. Автоматическая выгрузка реализована самими движками; новый запрос к работающему серверу может загрузить модель снова. **Остановить все серверы** полностью останавливает настроенные движки и оставляет панель доступной.

`exclusive: true` последовательно останавливает другой движок перед запуском выбранного. Сторонние процессы и прямой запуск служб этим правилом не ограничиваются.

## CLI

```powershell
ai help
```

```powershell
ai status
```

```powershell
ai stop
```

CLI доступен в PATH после установки. Все команды и параметры: [справка CLI](docs/commands.md).

## Разработка и проверки

```powershell
pwsh -NoProfile -File .\tests\test.ps1
```

```powershell
python -m unittest discover -s tests -p http_test.py -v
```

Python нужен только для второго набора тестов. Он проверяет настоящий HTTP-сервер PowerShell, вход, CSRF/Origin, ограничения запросов, журналы и фоновые операции с тестовым адаптером движков. Тесты управления проверяют XML, откат, эксклюзивность и модели без GPU. Реальные Windows-службы, ACL и Firewall нужно проверить на серверном ПК после установки.

Сборка единого установочного файла:

```powershell
pwsh -NoProfile -File .\scripts\build-setup.ps1
```

[Архитектура](docs/architecture.md) · [Веб-панель](docs/web-panel.md) · [Установка](docs/install.md) · [NAS reverse proxy](docs/reverse-proxy.md) · [Изменения](CHANGELOG.md)
