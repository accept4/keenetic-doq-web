# keenetic-doq-web

Веб-панель для управления **keenetic-doq** (DNS-over-QUIC форвардер) на роутерах Keenetic/OpenWRT с Entware.

Позволяет через браузер управлять DoQ-апстримами, geo-серверами, геоблокированными доменами и перезапускать сервис — без ручной правки конфигов и SSH.

## Возможности

- Статус `doqd` и версия (установленная / последняя из GitHub)
- Управление апстримами: добавление (`upstream`, `geo`, `fallback`, `--force`), удаление, тестирование
- Geo-серверы: просмотр pinned-сервера, ранжирования, здоровья прокси; ручная переоценка (`geo reselect`)
- Геоблокированные домены: добавление/удаление доменов из списка геоблокированных
- Безопасность: whitelist для `type`, валидация доменов, POST-only для опасных команд
- Кроссплатформенная сборка: GitHub Actions собирает бинарники для 8 архитектур при каждом релизе

## Установка на роутер

Требования: Entware (`/opt`), `curl` (пакет `opkg install curl`).

### Онлайн-установка

```sh
curl -fsSL https://raw.githubusercontent.com/accept4/keenetic-doq-web/main/install.sh | sh
```

Скрипт автоматически определит архитектуру через `opkg print-architecture`, скачает нужный бинарник, проверит SHA256 и ELF-заголовок, запустит сервис.

Веб-интерфейс: `http://<IP-роутера>:8091`

### Офлайн-установка (--local)

```sh
# Скопируйте бинарник на роутер, затем:
sh install.sh --local /tmp/doq-web-linux-mipsle
```

### Удаление

```sh
curl -fsSL https://raw.githubusercontent.com/accept4/keenetic-doq-web/main/uninstall.sh | sh
```

## Сборка из исходников

Нужен Go 1.22+. Собирает статические бинарники для 8 платформ:

```sh
# Windows (PowerShell)
.\build.ps1

# Вручную (пример для Keenetic Ultra — mipsle little-endian):
CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat \
  go build -trimpath -ldflags="-s -w" -o doq-web-linux-mipsle .
```

### Поддерживаемые архитектуры

| Архитектура | GOARCH | GOARM/GOMIPS | Таргет Entware |
|---|---|---|---|
| aarch64 | `arm64` | — | `aarch64-k3.10` |
| ARM v7 | `arm` | `GOARM=7` | `armv7sf-k3.2` |
| ARM v5 | `arm` | `GOARM=5` | `armv5sf-k3.2` |
| MIPS little-endian | `mipsle` | `softfloat` | `mipselsf-k3.4` |
| MIPS big-endian | `mips` | `softfloat` | `mipssf-k3.4` |
| MIPS64 little-endian | `mips64le` | `softfloat` | `mips64elsf-k5.0` |
| MIPS64 big-endian | `mips64` | `softfloat` | `mips64sf-k5.0` |
| x86_64 | `amd64` | — | `x64-k3.2` |

## Тесты

```sh
go test ./...
```

## Как это работает

`doq-web` — это Go-сервер, который слушает порт `:8091` и предоставляет:
- HTML-интерфейс с формой управления апстримами и geo-серверами
- REST API (`/add`, `/remove`, `/geo`, `/status` и др.)
- Выполнение команд `doqd` через `os/exec` с таймаутами

На роутере `dnsmasq` слушает `:53` и перенаправляет запросы для выбранных доменов на `doqd` (DoQ-резолвер, слушает `:5354`). `doq-web` управляет списком этих редиректов через конфиги `dnsmasq` и API `doqd`.

## Лицензия

MIT
