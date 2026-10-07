# Установка и обновление

OmniBox AI — один файл `omnibox` (`omnibox.exe` на Windows). Его ставит и обновляет одна и та же команда.

## Установка на Windows

1. Откройте PowerShell **от имени администратора** (подойдёт встроенный Windows PowerShell).
2. Выполните:

   ```powershell
   gh release download --repo aafnx/omnibox-ai --pattern install.ps1 --dir $env:TEMP --clobber
   powershell -NoProfile -ExecutionPolicy Bypass -File $env:TEMP\install.ps1
   ```

   Если `gh` не установлен, скачайте `install.ps1` со страницы выпуска в браузере и запустите вторую строку. Скрипт попросит GitHub token с правом чтения репозитория (Contents: Read); токен вводится скрытно и нигде не сохраняется.
3. Ответьте на вопросы. **Enter принимает ответ в скобках**:

   | Вопрос | По умолчанию |
   |---|---|
   | Порт панели и API | 8800 |
   | Адреса панели | IPv4 этого ПК, имя компьютера, `localhost` |
   | Адрес API для клиентов | `http://<IPv4>:8800/v1` |
   | HTTPS-адрес через NAS | `https://llm.fnx.red` (`-` — без NAS) |
   | IP NAS | `192.168.1.10` |
   | Установить PrismML и Ollama | да, последние версии |
   | Каталог моделей | `C:\ProgramData\OmniBox AI\models` |

4. Сразу после установки, до скачивания движков, установщик покажет **пароль панели** и **API key**. Они показываются один раз и нигде не хранятся в открытом виде — сохраните их в менеджере паролей. Пароль можно сменить в панели (вкладка «Подключение»); если он потерян — `ai password reset`, новый API key — `ai key new` (от имени администратора). Повторный запуск установщика — это обновление: пароль и ключ не меняются и не показываются.

Неинтерактивная установка со всеми ответами по умолчанию: добавьте `-Yes` (`install.ps1 -Yes`). Определённая версия: `-Version 0.18.0`.

Скрипт проверяет SHA256 выпуска до запуска. Если на любом шаге установки происходит ошибка, всё сделанное отменяется.

## Удаление PowerShell-версии (0.17.1 и раньше)

Установщик прежнюю версию не трогает: она продолжает работать рядом (панель на 18090), пока вы её не удалите. После того как новая панель открылась и работает, выполните один раз в PowerShell от имени администратора:

```powershell
gh release download v0.18.0 --repo aafnx/omnibox-ai --pattern remove-old-version.ps1 --dir $env:TEMP --clobber
powershell -NoProfile -ExecutionPolicy Bypass -File $env:TEMP\remove-old-version.ps1
```

Скрипт показывает, что найдено, и спрашивает подтверждение (`-Yes` — без вопроса). Затем:

1. останавливает и удаляет службы прежней версии (`omnibox-ai`, `bonsai`, `ollama` — по её `config.json`);
2. **переносит модели** в каталоги моделей новой версии: GGUF — в каталог PrismML, модели Ollama (`blobs`, `manifests`) — в каталог Ollama. Каталоги берутся из `C:\ProgramData\OmniBox AI\config.json`. Файл, который уже есть в новом каталоге, не перезаписывается;
3. удаляет правила Firewall `omnibox-ai-panel` и `omnibox-ai-api`, запись в пользовательском PATH и строку PATH в профиле PowerShell;
4. удаляет каталог прежней версии (обычно `C:\tools\ai`), каталоги движков, созданные для неё (с XML-профилем службы рядом с исполняемым файлом), и её журналы.

Каталог, в котором остались непереносимые модели, не удаляется — скрипт об этом сообщит. Нестандартный каталог прежней версии: `-Root D:\ai`. После этого перенастройте NAS: upstream `http://192.168.1.77:8800` вместо `:18090`.

Скрипт не входит в программу: он нужен один раз при переходе на Go-версию.

## Установка на Linux

```sh
gh release download --repo aafnx/omnibox-ai --pattern install.sh --dir /tmp --clobber && sudo sh /tmp/install.sh
```

Параметры: `--yes`, `--version 0.18.0`. Откройте в межсетевом экране TCP-порт панели (8800) для локальной сети.

## Обновление

Та же команда, что и установка. Настройки, пароль, API key, модели и журналы сохраняются; службы переключаются на новую версию, панель перезапускается. Если новая панель не отвечает, установщик возвращает прежнюю версию. Работающий движок переходит на новую версию при следующем запуске.

## Где что лежит

| | Windows | Linux |
|---|---|---|
| Программа | `C:\Program Files\OmniBox AI\bin\<версия>\omnibox.exe` (текущая и предыдущая) | `/opt/omnibox/bin/<версия>/omnibox` |
| Команда `ai` | `C:\Program Files\OmniBox AI\ai.cmd` (в PATH) | `/usr/local/bin/ai` |
| Данные | `C:\ProgramData\OmniBox AI` — `config.json`, `logs\`, `engines\`, `models\`, `run\` | `/var/lib/omnibox` |
| Службы | `omnibox` (панель, автозапуск), `omnibox-prismml`, `omnibox-ollama` (движки, запуск из панели) | те же unit-файлы systemd |

Каталог данных доступен только SYSTEM и администраторам (Linux — root): там настройки, журналы и, если вы включили запись, содержимое запросов.

## Удаление

Пока вручную, от имени администратора:

```powershell
sc.exe stop omnibox-prismml; sc.exe delete omnibox-prismml
sc.exe stop omnibox-ollama; sc.exe delete omnibox-ollama
sc.exe stop omnibox; sc.exe delete omnibox
netsh advfirewall firewall delete rule name="OmniBox AI"
Remove-Item -Recurse "C:\Program Files\OmniBox AI"
# Данные и модели: Remove-Item -Recurse "C:\ProgramData\OmniBox AI"
```

Уберите `C:\Program Files\OmniBox AI` из системной переменной PATH.
