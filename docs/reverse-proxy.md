# Веб-панель через reverse proxy на NAS

Начиная с 0.5.0 панель поддерживает TLS termination на доверенном NAS. Клиент открывает HTTPS-домен NAS, NAS передаёт запросы по HTTP на Windows-ПК. Установщик не изменяет настройки NAS, сертификаты или DNS.

## Когда настраивать

Если NAS ещё не готов, ответь `n` на вопрос **«Настроить HTTPS-доступ к панели через reverse proxy на NAS?»**. Сейчас останется доступ по LAN/VPN/SSH. Когда появятся домен и IP NAS, повтори установщик и ответь `y`.

Мастер спросит:

- Внешний URL панели: например `https://ai.example.com`, **без пути**.
- IP NAS, с которого он фактически подключается к Windows-ПК: например `192.168.1.2`. При Docker/сетевой трансляции это может быть другой адрес. Допускается несколько конкретных IP через запятую, без CIDR/wildcard.
- Разрешённые адреса Firewall: NAS должен входить в этот список. Для доступа только через NAS можно указать `192.168.1.2`; loopback/SSH-туннель не требует входящего LAN-доступа.

Существующий пароль сохраняется. Enter сохраняет настроенный proxy; `n` отключает proxy-режим. Ответы подтверждения: `yes/no` или `y/n`, регистр любой; `[y/N]` означает по умолчанию no, `[Y/n]` — yes.

Если установочный файл уже на ПК, пример неинтерактивного обновления (одна физическая строка):

```powershell
& .\install-omnibox-ai.ps1 -NonInteractive -PanelPublicUrl 'https://ai.example.com' -PanelTrustedProxyAddresses '192.168.1.2' -PanelRemoteAddresses '192.168.1.2'
```

Чтобы отключить proxy без вопросов, явно передай пустые `-PanelPublicUrl '' -PanelTrustedProxyAddresses ''`. Неуказанные параметры при обновлении сохраняются.

## Настройка NAS

Создай HTTPS virtual host `ai.example.com` с действующим сертификатом. Upstream панели: **`http://192.168.1.77:18090`** — подставь IP Windows-ПК и выбранный порт. Проксируй весь корень `/`, включая `/api/*`, `/app.js`, `/style.css`. Размещение под `/ai/` в этой версии не поддерживается.

NAS должен **перезаписывать**, а не дополнять клиентские заголовки:

| Заголовок на upstream | Значение |
|---|---|
| `Host` | Upstream адрес `192.168.1.77:18090` или внешний hostname |
| `X-Forwarded-Proto` | `https` |
| `X-Forwarded-Host` | Внешний hostname и нестандартный HTTPS-порт, если он есть |
| `X-Forwarded-For` | Один реальный IP клиента |
| `Origin` | Оставить исходный заголовок браузера без изменения |

Пример блока **внутри HTTPS server** Nginx. Сертификат/listen/server_name настраиваются на NAS отдельно:

```nginx
location / {
    proxy_pass http://192.168.1.77:18090;
    proxy_http_version 1.1;
    proxy_set_header Host $proxy_host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $http_host;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header Authorization $http_authorization;
    proxy_set_header Connection "";
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 1800s;
    proxy_send_timeout 1800s;
}
```

Здесь намеренно используется `$remote_addr`, а не цепочка `$proxy_add_x_forwarded_for`: панель принимает один IP для ограничения попыток входа. При дополнительном proxy перед NAS сначала настрой корректное получение адреса клиента на NAS. Не кешируй ответы панели и не переписывай Cookie/Origin.

Панель доверяет forwarded-заголовкам только от перечисленных IP NAS. HTTPS Origin сверяется с `public_origin`; cookie получает `Secure`, `HttpOnly` и `SameSite=Strict`. HTTPS-сессия не принимается при прямом HTTP-доступе. Вход с LAN/SSH выполняется отдельной сессией. TLS обеспечивает NAS; встроенного TLS-сервера на ПК нет.

## Проверка

Открой HTTPS-домен, войди в панель, обнови статус и выполни управление остановленным движком. В инструментах браузера запросы `/api/*` должны оставаться на том же HTTPS-домене. Если вход возвращает ошибку Origin/proxy, проверь внешнюю схему/порт, IP источника NAS и заголовки; не отключай CSRF/Origin.

Конфигурация на ПК: `C:\tools\ai\panel.json`, поля `public_origin` и `trusted_proxy_addresses`. При ручном изменении нужна перезагрузка **службы панели**, не всего ПК. Старые конфигурации без этих полей продолжают работать в прямом режиме.

## Адрес панели и API моделей

С версии 0.6.0 один домен и одно правило NAS обслуживают панель и OpenAI API: `https://llm.fnx.red/` → панель, `https://llm.fnx.red/v1` → API. NAS проксирует весь корень на **`http://192.168.1.77:18090`**; маршруты `/v1/*` сервер передаёт выбранному движку. Не добавляй `:11435` к домену.

Сохраняй `Authorization` для Bearer API key, отключи `proxy_buffering` и увеличь `proxy_read_timeout`/`proxy_send_timeout` до 1800 секунд для генерации. Панель использует собственный пароль/CSRF; API — отдельный ключ или режим без ключа. Отдельный API-порт (например, 11435) остаётся необязательным вариантом прямого доступа. [Настройки клиентов и пример NAS](model-api.md).
