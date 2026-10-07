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
   | HTTPS-адрес через NAS | без NAS (для NAS введите, например, `https://llm.fnx.red`, затем IP NAS) |
   | Установить PrismML и Ollama | да, последние версии |
   | Каталог моделей | `C:\ProgramData\OmniBox AI\models` |
   | Удалить службы PowerShell-версии (`omnibox-ai`, `bonsai`, `ollama`) | да, если найдены — после успешного запуска новой панели; их файлы остаются на диске |

4. В конце установщик покажет **пароль панели** и **API key**. Они показываются один раз и нигде не хранятся в открытом виде — сохраните их в менеджере паролей. Пароль можно сменить в панели (вкладка «Подключение») или командой `ai password reset`.

Неинтерактивная установка со всеми ответами по умолчанию: добавьте `-Yes` (`install.ps1 -Yes`). Определённая версия: `-Version 0.18.0`.

Скрипт проверяет SHA256 выпуска до запуска. Если на любом шаге установки происходит ошибка, всё сделанное отменяется.

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
