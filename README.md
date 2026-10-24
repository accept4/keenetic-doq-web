# keenetic-doq-web

Веб-панель для управления **[keenetic-doq](https://github.com/necronicle/keenetic-doq)** (DNS-over-QUIC форвардер) на роутерах Keenetic (с Entware) и OpenWRT (без Entware).

Позволяет через браузер управлять DoQ-апстримами, файлом hosts (подмена адресов) и перезапускать сервис — без ручной правки конфигов и SSH.

## Возможности

- Статус `doqd` и версия (установленная / последняя из GitHub)
- Управление апстримами: добавление (`upstream`, `fallback`, `--force`), удаление, тестирование
- Hosts (подмена адресов): просмотр источника и числа правил, `check`/`reload`, экспорт встроенного снимка в свой файл, встроенный редактор с валидацией перед применением
- Безопасность: whitelist для `type`, валидация доменов, POST-only для опасных команд
- Кроссплатформенная сборка: GitHub Actions собирает бинарники для 8 архитектур при каждом релизе

> **Примечание.** Web-панель адаптирована под keenetic-doq 0.6.x (SmartDNS). Команды `geo`, `geo reselect`, `add-domain`/`remove-domain` и флаг `--geo` в апстриме удалены — вместо них директива `hosts builtin` / `hosts /путь/к/файлу` и подкоманды `doqd hosts status|check|export|reload`.

## Установка на Keenetic

Требования: Entware (`/opt`), `curl` (пакет `opkg install curl`).

### Онлайн-установка

```sh
curl -fsSL https://raw.githubusercontent.com/accept4/keenetic-doq-web/main/install.sh | sh
```

Скрипт автоматически определит архитектуру через `opkg print-architecture`, скачает нужный бинарник, проверит SHA256 и ELF-заголовок, запустит сервис.

Веб-интерфейс: `http://<IP-роутера>:8091`

## Установка на OpenWRT

Работает на OpenWRT 23.05+ **без Entware** — нужен только сам `doqd`,
поставленный [штатным установщиком апстрима](https://github.com/necronicle/keenetic-doq):

```sh
wget -qO- https://raw.githubusercontent.com/necronicle/keenetic-doq/main/install-openwrt.sh | sh
```

Затем панель:

```sh
wget -qO- https://raw.githubusercontent.com/accept4/keenetic-doq-web/main/install.sh | sh
```

(если установлен `curl`, подойдёт и команда из раздела Keenetic — скрипт
сам определит платформу по `/etc/openwrt_release`).

Отличия от Keenetic:

- архитектура берётся из `DISTRIB_ARCH` в `/etc/openwrt_release` — тот же
  источник, что у установщика апстрима;
- бинарник ставится в `/usr/sbin/doq-web`, автозапуск — procd-скрипт
  `/etc/init.d/doq-web` (respawn поднимет демона после сбоя);
- бинарник и init-скрипт добавляются в `/etc/sysupgrade.conf` — панель
  переживает обновление прошивки с сохранением настроек;
- сама панель по тому же признаку работает с openwrt-раскладкой апстрима:
  конфиг `/etc/doqd.conf`, init демона `/etc/init.d/doqd`, свой hosts
  `/etc/doqd/hosts`, а кнопки Install/Update дёргают `install-openwrt.sh`.

### Офлайн-установка (--local)

```sh
# Скопируйте бинарник на роутер, затем (платформа определяется так же):
sh install.sh --local /tmp/doq-web-linux....
```

### Удаление

```sh
curl -fsSL https://raw.githubusercontent.com/accept4/keenetic-doq-web/main/uninstall.sh | sh
# на OpenWRT без curl:
wget -qO- https://raw.githubusercontent.com/accept4/keenetic-doq-web/main/uninstall.sh | sh
```

Скрипт определяет платформу и удаляет соответствующие файлы (на OpenWRT —
ещё и свои строки из `/etc/sysupgrade.conf`).

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

| Архитектура | GOARCH | GOARM/GOMIPS | Таргет Entware | DISTRIB_ARCH OpenWRT |
|---|---|---|---|---|
| aarch64 | `arm64` | — | `aarch64-k3.10` | `aarch64_*` |
| ARM v7 | `arm` | `GOARM=7` | `armv7sf-k3.2` | `arm_cortex-a*_vfp*`, `arm_cortex-a*_neon*` |
| ARM v5 | `arm` | `GOARM=5` | `armv5sf-k3.2` | прочие `arm_*` |
| MIPS little-endian | `mipsle` | `softfloat` | `mipselsf-k3.4` | `mipsel_*` |
| MIPS big-endian | `mips` | `softfloat` | `mipssf-k3.4` | `mips_*` |
| MIPS64 little-endian | `mips64le` | `softfloat` | `mips64elsf-k5.0` | `mips64el_*` |
| MIPS64 big-endian | `mips64` | `softfloat` | `mips64sf-k5.0` | `mips64_*` |
| x86_64 | `amd64` | — | `x64-k3.2` | `x86_64` |

Go не собирает панели под `riscv64`, `i386`, `loong64`, ARMv4 и armeb —
на этих платформах doqd (апстрим) работает, а панель — нет; поможет только
перенос бинарника вручную.

## Тесты

```sh
go test ./...
```

## Как это работает

`doq-web` — это Go-сервер, который слушает порт `:8091` и предоставляет:
- HTML-интерфейс с формами управления апстримами и hosts
- REST API (`/add`, `/remove`, `/hosts`, `/api/status` и др.)
- Выполнение команд `doqd` через `os/exec` с таймаутами

На роутере `doqd` слушает `:5354`, подставляет адреса из hosts для известных имён, а остальные резолвит через DoQ-апстримы. `doq-web` управляет апстримами (`doqd add/remove/list/test`) и файлом hosts (`doqd hosts ...`, директива `hosts` в конфиге). Платформа определяется по `/etc/openwrt_release`: на Keenetic конфиг doqd — `/opt/etc/doqd.conf`, на OpenWRT — `/etc/doqd.conf`.

## Лицензия

MIT
