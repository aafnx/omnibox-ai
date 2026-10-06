# Установка, обновление и удаление

## Установка прямо на ПК через SSH

Выполни установку в PowerShell 7 на OmniBox, в той же повышенной SSH-сессии, где работает `Start-Service`. Ноутбук используется только как терминал.

Требования: Windows, PowerShell 7.4+, уже установленные службы Bonsai/PrismML и Ollama. Установщик устанавливает веб-панель и CLI `ai` поверх текущей рабочей конфигурации: движки, модели и драйверы уже находятся на ПК.

Репозиторий приватный. Один раз создай [fine-grained GitHub token](https://github.com/settings/personal-access-tokens/new?name=omnibox-ai-install&target_name=aafnx&expires_in=1&contents=read): Resource owner `aafnx`, **Only select repositories → omnibox-ai**, **Contents: Read-only**, срок один день. Другие разрешения не нужны. Не вводи токен в команду; скрипт попросит его скрытым вводом. [GitHub: токены](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens), [Contents API](https://docs.github.com/en/rest/repos/contents).

Скопируй одну строку в SSH-сессию на ПК:

```powershell
& { $ErrorActionPreference = 'Stop'; $token = Read-Host 'GitHub token (omnibox-ai: Contents Read)' -AsSecureString; $headers = @{}; $file = Join-Path $env:TEMP ('omnibox-ai-install-' + [Guid]::NewGuid().ToString('N') + '.ps1'); try {     $headers.Authorization = 'Bearer ' + ([pscredential]::new('github', $token)).GetNetworkCredential().Password; $headers.Accept = 'application/vnd.github+json'; $package = Invoke-RestMethod -Uri 'https://api.github.com/repos/aafnx/omnibox-ai/contents/dist/install-omnibox-ai.ps1?ref=main' -Headers $headers -MaximumRedirection 0 -TimeoutSec 60; if ($package.type -ne 'file' -or $package.encoding -ne 'base64') { throw 'GitHub не вернул установочный файл.' }; [IO.File]::WriteAllBytes($file, [Convert]::FromBase64String($package.content)); $headers.Clear(); & $file } finally {     $headers.Clear(); $token.Dispose(); if ([IO.File]::Exists($file)) { [IO.File]::Delete($file) } } }
```

Исходник загрузчика: [scripts/get-install.ps1](../scripts/get-install.ps1). Он скачивает установочный файл через HTTPS GitHub Contents API с токеном только в памяти. Токен не сохраняется в файлы, настройки, переменные окружения или историю команды. Заголовок авторизации очищается перед запуском установщика; временный установочный файл удаляется после завершения или ошибки. Установщик содержит всю программу и проверяет SHA256 встроенного пакета; hash обнаруживает повреждение, а доверенный источник самого скрипта — твой репозиторий.

Если установочный файл уже есть на ПК, интернет и токен не нужны:

```powershell
& "$env:USERPROFILE\install-omnibox-ai.ps1"
```

## Вопросы мастера

| Вопрос | По умолчанию |
|---|---|
| Папка установки | `C:\tools\ai` |
| Сервер по умолчанию | Значение существующего JSON, либо `bonsai` |
| Контекст | Текущее значение XML выбранной службы |
| Запустить ИИ-сервер сейчас | `no` (`y/N`) |
| IP/DNS-имя панели | Адрес ПК или сохранённый адрес |
| Порт панели | `18090` или сохранённый порт |
| Разрешённые сети Firewall | `LocalSubnet` или сохранённое значение |
| Сетевой OpenAI API | `yes`, либо сохранённое значение |
| Порт / Base URL API | `18090` / адрес по IP ПК, либо сохранённые значения |
| Использовать API key | `yes`, либо сохранённый режим |
| HTTPS reverse proxy на NAS | `no`, либо сохранённый режим |
| Пароль панели (при первой установке) | Enter генерирует пароль; от 12 символов при ручном вводе |

Для подтверждений используй `yes/no` или `y/n` (любой регистр). `[y/N]` означает по умолчанию no. Enter сохраняет предложенное значение. Для Bonsai текущая настройка — 196608 (192k); для Ollama — 32768 (32k). Размер можно задать как `128k` или точное число токенов. Если выбран другой контекст, он сохраняется для следующего запуска. При ответе `no`/`n` работающие серверы не останавливаются: их активный контекст не меняется. При ответе `yes`/`y` выбранная служба запускается или перезапускается, применяется exclusive из JSON.

Мастер меняет только выбранный `default_backend` в существующем JSON; остальные локальные параметры сохраняются. Изменения контекста и запуск выполняются через тот же модуль, что и команды `ai`, включая откат. Уже выбранный контекст повторно не записывается.

## Что устанавливается

Известные файлы программы копируются в отдельную папку с маркером установки. В пользовательский PATH добавляется эта папка, а в PowerShell 7 CurrentUserAllHosts — собственный PATH-блок. Существующий промпт и остальные строки профиля сохраняются; исходный профиль архивируется. Машинный PATH не меняется.

Папка должна быть отдельной, с абсолютным ASCII-путём без `%` и кавычек. Чужая непустая папка и другая существующая команда `ai` вызывают отказ. При обновлении старые файлы программы и JSON копируются в `C:\tools\ai\backups\<дата-время-идентификатор>`.

Параметры движков, таймеры простоя и WoL сохраняются. Службы движков переводятся на встроенный service runtime с ручным запуском, панель `omnibox-ai` — с автоматическим. WinSW больше не требуется. Его прежние файлы и пути запуска сохраняются в backups; используемые wrapper EXE удаляются после успешного запуска панели. Совместимые XML-профили остаются данными настроек, их читает сам omnibox-ai. Установщик компилирует DLL средствами PowerShell — SDK .NET не нужен — и проверяет временную службу: запуск, остановку, завершение дочернего процесса. Планировщик не используется. Панель открывает выбранный HTTP-порт только для указанных адресов Firewall. Каталог панели защищается ACL для SYSTEM, Administrators и пользователя установки.

Отдельный сетевой OpenAI API настраивается в том же мастере; `gateway.json` сохраняет порт, Base URL, движок и hash ключа. На общем порту используется правило панели; отдельное `omnibox-ai-api` создаётся только для дополнительного API-порта. Новая служба не запускает ИИ-движки.

Веб-интерфейс: [управление, модели и доступ извне](web-panel.md). Пароль хранится как PBKDF2 hash; при обновлении он сохраняется. Панель требует доверенной LAN/VPN или SSH-туннеля.

## После установки

Открой адрес панели, напечатанный мастером, и войди с выбранным/сгенерированным паролем. На текущем ПК пример: `http://192.168.1.77:18090`.

При запуске скрипта через `&` из текущего PowerShell PATH процесса обновляется сразу:

```powershell
ai help
```

```powershell
ai status
```

```powershell
ai context bonsai
```

Если установку запускал дочерний `pwsh`, переподключись по SSH или добавь путь в текущий процесс:

```powershell
if (@($env:Path -split ';' | Where-Object { $_.TrimEnd('\') -ieq 'C:\tools\ai' }).Count -eq 0) { $env:Path += ';C:\tools\ai' }
```

Новые SSH-сессии получают путь из профиля. При `-NoProfile` профиль не загружается: используй полный путь. Перезапуск sshd или ПК не нужен.

## Исходники и автоматизация

Если исходники уже распакованы на ПК, запускается тот же мастер:

```powershell
& .\install.ps1
```

Без вопросов, сохраняя контекст и не запуская ИИ-движки (служба панели будет запущена):

```powershell
& .\install.ps1 -NonInteractive
```

Явный контекст и запуск:

```powershell
& .\install.ps1 -NonInteractive -DefaultBackend bonsai -Context 128k -StartServer
```

Параметры панели: `-PanelHost`, `-PanelPort`, `-PanelRemoteAddresses` (например `LocalSubnet,10.10.0.0/24`), `-PanelPublicUrl`, `-PanelTrustedProxyAddresses` (только конкретные IP NAS). При обновлении сохраняются значения, которые явно не переопределены. Для NAS-прокси внешний HTTPS origin и конкретные IP NAS указываются вместе: [инструкция reverse proxy](reverse-proxy.md). Внешний URL не содержит пути; HTTPS завершается на NAS.

Параметры API: `-ApiPort`, `-ApiBaseUrl`, `-NoApi`. [API и ключи](model-api.md). При обновлении через мастер можно включить сетевой API в уже установленной версии.

`-NoPanelSetup` оставляет только CLI при первой установке; с уже установленной службой панели этот режим запрещён.

Те же параметры доступны в `dist/install-omnibox-ai.ps1`. `-NoProfileUpdate` отключает обновление профиля. PowerShell 5.1 и прежний `-Remote` не поддерживаются: установка выполняется на самом ПК через PowerShell 7.

## Обновление и восстановление

Повтори ту же команду загрузки на ПК и ответь на вопросы. Службы панели и движков останавливаются на время обновления runtime. Панель запускается снова; движок запускается только при выборе запуска в мастере. Контекст, NAS, пароль, ключ API и журнал сохраняются. Для исходников с Git: `git pull --ff-only`, затем `install.ps1`.

Предыдущие настройки модели восстанавливаются без поиска файлов:

```powershell
ai restore bonsai
```

После неудачного запуска с изменённым контекстом это выполняется автоматически; подробности — [команды](commands.md). История контекста хранится рядом с XML службы и сохраняется при обновлении оболочки.

Откат версии самой программы — отдельная операция: выбери напечатанную папку backup и останови `omnibox-ai`, восстанови `ai.ps1`, `ai.cmd`, `panel.ps1`, `src`, `web` и документацию, затем запусти `omnibox-ai`. JSON восстанавливай только если требуется отменить его настройки. Модели и службы от версии оболочки не зависят.

## Удаление панели и CLI

При необходимости сначала `ai stop`. Удаление панели само ИИ-движки не останавливает. В повышенной PowerShell-сессии останови и удали только службу панели и её правило Firewall:

```powershell
Stop-Service -Name omnibox-ai -ErrorAction Stop; Remove-Service -Name omnibox-ai -ErrorAction Stop; Get-NetFirewallRule -Name omnibox-ai-panel,omnibox-ai-api -ErrorAction SilentlyContinue | Remove-NetFirewallRule
```

Для установки без панели этот шаг пропускается. Пока службы движков используют `service.ps1` и `Service.Host.dll`, папку установки удалять нельзя. Для полного удаления останови и удали также управляемые службы движков.

Удалить её путь из пользовательского и текущего PATH:

```powershell
$aiDirectory = 'C:\tools\ai'; $userEntries = @([Environment]::GetEnvironmentVariable('Path', 'User') -split ';' | Where-Object { $_ -and [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') -ine $aiDirectory }); [Environment]::SetEnvironmentVariable('Path', ($userEntries -join ';'), 'User'); $env:Path = (@($env:Path -split ';' | Where-Object { $_.TrimEnd('\') -ine $aiDirectory }) -join ';')
```

Убери только блок между `# >>> omnibox-ai PATH >>>` и `# <<< omnibox-ai PATH <<<` в профиле PowerShell 7. Папку `C:\tools\ai` можно переместить в архив или корзину. `C:\ai`, `C:\tools\bonsai` и `C:\tools\ollama` содержат модели и движки, не оболочку.


После обновления доступны `ai access` и `ai password reset`. Старые параметры NAS, пароль, ключ API и журнал запросов сохраняются; файлы logs не входят в установочный пакет. Ограничения одной модели применяются при следующем управляемом запуске движка. [Мониторинг и журнал](monitoring.md).
