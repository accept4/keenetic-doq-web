# keenetic-doq-web

Веб-интерфейс для управления **DoQ (DNS over QUIC)** на роутерах Keenetic с Entware.

Позволяет через браузер добавлять, удалять, тестировать и перезапускать DoQ-редиректы
на роутере — без ручной правки конфигов `dnsmasq` и перезагрузки сервисов по SSH.

## Что делает

- Показывает статус `doqd` и список редиректов в реальном времени
- **Добавляет** DoQ-редиректы: домен → `doqd` (через `dnsmasq` server-строку)
- **Тестирует** редиректы: проверка, что DNS-запрос уходит по DoQ, а не по UDP/53
- **Перезапускает** `dnsmasq`/`doqd` без перезагрузки роутера
- **Удаляет** ненужные редиректы
- Работает на Entware (KeeneticOS), не требует перепрошивки

## Установка на роутер

Требования: Keenetic с Entware (`/opt`), `curl`.

```sh
# SSH на роутер, затем:
curl -fsSL https://raw.githubusercontent.com/accept4/keenetic-doq-web/main/install.sh | sh
```

Скрипт сам определит архитектуру (mipsle / arm64 / arm), скачает нужный бинарник
из [Releases](https://github.com/accept4/keenetic-doq-web/releases/latest),
установит в `/opt/sbin/doq-web`, создаст службу автозапуска `S57doq-web` и запустит сервис.

Веб-интерфейс: `http://<IP-роутера>:8091`

### Удаление

```sh
curl -fsSL https://raw.githubusercontent.com/accept4/keenetic-doq-web/main/uninstall.sh | sh
```

## Сборка из исходников

Нужен Go 1.22+. Собирает статические бинарники для 4 платформ:

```sh
# Windows (PowerShell)
.\build.ps1

# Вручную (пример для Keenetic Ultra — mipsle little-endian):
CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat \
  go build -trimpath -ldflags="-s -w" -o doq-web-linux-mipsle .
```

Архитектуры Keenetic и таргеты сборки:

| Модель Keenetic | Архитектура | GOARCH | GOMIPS |
|---|---|---|---|
| Giga KN-1010, Ultra KN-1811 | aarch64 | `arm64` | — |
| Ultra KN-1810, Hero KN-1011 | mipsel | `mipsle` | `softfloat` |
| Старые модели (armv7) | armv7l | `arm` | — (GOARM=7) |

## Тесты

```sh
go test ./...
```

## Как это работает

Keenetic + Entware: `dnsmasq` слушает :53 и перенаправляет запросы для выбранных доменов
на `doqd` (DoQ-резолвер, слушает :5354). `doq-web` управляет списком этих редиректов
через конфиги `dnsmasq` и API `doqd`, а также проверяет, что запросы реально идут по DoQ.

## Лицензия

MIT
