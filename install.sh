#!/bin/sh
set -e

REPO="accept4/keenetic-doq-web"
NAME="doq-web"

echo "=== keenetic-doq-web installer ==="

# 1. Платформа. /etc/openwrt_release существует только на OpenWRT: там нет
# Entware, бинарники лежат в /usr/sbin, конфиги в /etc, автозапуск — procd.
# На Keenetic наоборот, вся среда — Entware в /opt. Пути панели здесь;
# пути doqd панель сама различает в рантайме по тому же файлу.
if [ -f /etc/openwrt_release ]; then
    PLATFORM="openwrt"
    BIN_DIR="/usr/sbin"
    INIT_DIR="/etc/init.d"
    SVC="$INIT_DIR/doq-web"
else
    PLATFORM="keenetic"
    BIN_DIR="/opt/sbin"
    INIT_DIR="/opt/etc/init.d"
    SVC="$INIT_DIR/S57doq-web"
fi
echo "Платформа: $PLATFORM"

# 2. Entware обязателен только на Keenetic: там он и среда исполнения, и
# источник архитектуры (opkg). На OpenWRT всё нужное есть в прошивке.
if [ "$PLATFORM" = "keenetic" ] && [ ! -d "/opt/bin" ] && [ ! -d "/opt/sbin" ]; then
    echo "Ошибка: Среда Entware не обнаружена в /opt!"
    echo "Установите Entware на роутер перед запуском скрипта."
    exit 1
fi

# 3. Поддержка --local режима (установка из локального файла)
LOCAL_FILE=""
if [ "$1" = "--local" ]; then
    [ -n "$2" ] || { echo "Ошибка: укажите путь к бинарнику: sh install.sh --local ./doq-web-linux-<arch>"; exit 1; }
    [ -f "$2" ] || { echo "Ошибка: файл не найден: $2"; exit 1; }
    LOCAL_FILE="$2"
    echo "Режим --local: устанавливаем из $LOCAL_FILE"
fi

# 4. Чем качать. На Keenetic busybox-wget собран без TLS — там строго curl.
# На OpenWRT штатный uclient-fetch (алиас wget) умеет https, curl опционален.
DL="curl"
if [ -z "$LOCAL_FILE" ]; then
    if command -v curl >/dev/null 2>&1; then
        DL="curl"
    elif [ "$PLATFORM" = "openwrt" ] && command -v wget >/dev/null 2>&1; then
        DL="wget"
    else
        echo "Ошибка: curl не установлен!"
        echo "Установите его командой: opkg update && opkg install curl"
        echo "Или используйте локальный режим: sh install.sh --local ./doq-web-linux-<arch>"
        exit 1
    fi
fi

fetch() { # $1 = url, $2 = output file
    if [ "$DL" = "wget" ]; then
        wget -qO "$2" "$1"
    elif [ -n "$GITHUB_TOKEN" ]; then
        curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" -o "$2" "$1"
    else
        curl -fsSL -o "$2" "$1"
    fi
}

# 5. Определяем архитектуру.
# Keenetic: Entware точно знает свой таргет — opkg print-architecture
# возвращает aarch64-k3.10, mipselsf-k3.4, mipssf-k3.4 и т.д. uname -m на
# Keenetic отдаёт "mips" для обеих endian-ностей, а /proc/cpuinfo на части
# прошивок не содержит "little" — отсюда KN-1810 (mipselsf) ошибочно
# уезжал в mipssf.
# OpenWRT: DISTRIB_ARCH из /etc/openwrt_release — тот же источник, что у
# апстримового install-openwrt.sh для doqd. Имена таргетов совпадают с
# Entware, чтобы остальной скрипт не ветвился.
detect_arch() {
    if [ "$PLATFORM" = "openwrt" ]; then
        . /etc/openwrt_release
        RAW_ARCH="$DISTRIB_ARCH"
        case "$DISTRIB_ARCH" in
            aarch64*) echo "aarch64-k3.10" ;;
            mips64el*) echo "mips64elsf-k5.0" ;;
            mips64*)   echo "mips64sf-k5.0" ;;
            mipsel*)   echo "mipselsf-k3.4" ;;
            mips*)     echo "mipssf-k3.4" ;;
            arm_cortex-a*_neon*|arm_cortex-a*_vfp*) echo "armv7sf-k3.2" ;;
            arm*)      echo "armv5sf-k3.2" ;;
            x86_64*)   echo "x64-k3.2" ;;
            *)         echo "Ошибка: неизвестная архитектура OpenWRT: $DISTRIB_ARCH" >&2
                       echo "Go не собирает панели под эту платформу — используйте --local." >&2
                       exit 1 ;;
        esac
        return
    fi
    RAW_ARCH=$(opkg print-architecture 2>/dev/null | awk '!/all|noarch/ {print $2; exit}')
    case "$RAW_ARCH" in
        aarch64*|arm64*) echo "aarch64-k3.10" ;;
        mips64r6*|mips64*el*) echo "mips64elsf-k5.0" ;;
        mips64*)         echo "mips64sf-k5.0" ;;
        mipsel*)         echo "mipselsf-k3.4" ;;
        mips*)           echo "mipssf-k3.4" ;;
        armv7*)          echo "armv7sf-k3.2" ;;
        armv5*)          echo "armv5sf-k3.2" ;;
        x64*|x86_64*)    echo "x64-k3.2" ;;
        *)               echo "Ошибка: неизвестный таргет Entware: $RAW_ARCH" >&2; exit 1 ;;
    esac
}

TARGET=$(detect_arch)

# Пакетный менеджер знает таргет сборки, но не знает, какие ELF умеет ядро.
# На 64-битных SoC с 32-битным userspace (MT7622 в KeeneticOS) opkg печатает
# aarch64, а ядро отвечает armv7l и отказывает в exec aarch64-ELF: ENOEXEC,
# и busybox начинает читать бинарник как sh-скрипт. Для ARM-семейства вердикт
# ядра важнее — он и решает, запустится файл или нет.
kernel_m=$(uname -m 2>/dev/null)
case "$kernel_m" in
    armv7l|armv8l|armv6l)
        case "$TARGET" in
            aarch64*)
                echo "Ядро 32-битное ($kernel_m) — беру armv7 вместо $TARGET"
                TARGET="armv7sf-k3.2" ;;
        esac ;;
esac

echo "Таргет: $TARGET (arch: $RAW_ARCH, ядро: ${kernel_m:-неизвестно})"

# 6. Скачиваем бинарник.
# /tmp на роутерах — это tmpfs (RAM), поэтому скачиваем сразу в BIN_DIR,
# иначе на моделях с 32-64 МБ RAM скачивание вызывает OOM-kill.
mkdir -p "$BIN_DIR"
TMP="$BIN_DIR/$NAME.part"

# Определяем имя бинарника для таргета
case "$TARGET" in
    aarch64-k3.10)       BINARY="doq-web-linux-arm64" ;;
    armv7sf-k3.2)        BINARY="doq-web-linux-arm" ;;
    armv5sf-k3.2)        BINARY="doq-web-linux-armv5" ;;
    mips64elsf-k5.0)     BINARY="doq-web-linux-mips64le" ;;
    mips64sf-k5.0)       BINARY="doq-web-linux-mips64" ;;
    mipselsf-k3.4)       BINARY="doq-web-linux-mipsle" ;;
    mipssf-k3.4)         BINARY="doq-web-linux-mips" ;;
    x64-k3.2)            BINARY="doq-web-linux-amd64" ;;
    *)                   BINARY="doq-web-linux-mipsle" ;;
esac

# Бинарники лежат в ветке raw-bins и отдаются напрямую через
# raw.githubusercontent.com — без редиректов на objects.githubusercontent.com.

# Проверка ELF: неверно определённый таргет приводит к "syntax error:
# unexpected (": ядро отказывает в exec (ENOEXEC), а busybox sh читает
# бинарник как скрипт. Байт 5 (0-based) ELF-заголовка: 01 = little, 02 = big.
#
# Магию читаем через head -c, а не od -N: на busybox v1.37 с роутера
# `od -An -tx1 -N4` вернул пустоту для заведомо валидного ELF (файл, у
# которого SHA256 совпал с релизным!), и установка падала с
# "файл не является ELF-бинарником" на правильном бинарнике.
want=01
case "$TARGET" in
    mipssf-k3.4)   want=02 ;;
    mips64sf-k5.0) want=02 ;;
esac

elf_magic() { # $1 = файл, печатает 4 байта магии hex или пусто
    head -c 4 "$1" 2>/dev/null | od -An -tx1 2>/dev/null | tr -d ' \n\r'
}

elf_endian() { # $1 = файл, печатает байт EI_DATA (01/02) или пусто
    head -c 6 "$1" 2>/dev/null | tail -c 1 | od -An -tx1 2>/dev/null | tr -d ' \n\r'
}

if [ -n "$LOCAL_FILE" ]; then
    # Локальный режим: копируем файл, GitHub не трогаем.
    cp "$LOCAL_FILE" "$TMP"
    echo "Скопировано из $LOCAL_FILE"
    hash_ok=""
else
    raw_base="https://raw.githubusercontent.com/$REPO/raw-bins"
    echo "Скачиваю $BINARY (таргет $TARGET)"
    fetch "$raw_base/$BINARY" "$TMP" \
        || { rm -f "$TMP"; echo "Ошибка: download failed (нет сети? --local)"; exit 1; }

    # Проверяем размер: бинарник должен быть > 1 МБ. Если меньше — это
    # HTML-страница ошибки, а не ELF-бинарник.
    fsize=$(wc -c < "$TMP" 2>/dev/null || echo 0)
    if [ "$fsize" -lt 1000000 ]; then
        rm -f "$TMP"
        echo "Ошибка: скачался маленький файл ($fsize байт) — это не бинарник."
        echo "  Проверьте сеть: curl -fsSL https://raw.githubusercontent.com"
        echo "Офлайн-установка: скачайте файл на ПК и выполните"
        echo "  sh install.sh --local /tmp/$BINARY"
        exit 1
    fi

    # SHA256SUMS — эталонный хеш. Совпадение доказывает целость лучше любого
    # разбора заголовка, поэтому при hash_ok разбор ELF пропускаем.
    # Файл <1 КБ, его место в /tmp — на OpenWRT каталога /opt/tmp просто нет.
    hash_ok=""
    if command -v sha256sum >/dev/null 2>&1; then
        fetch "$raw_base/SHA256SUMS" /tmp/doq-web.sums \
            || { rm -f "$TMP"; echo "Ошибка: SHA256SUMS download failed"; exit 1; }
        want_hash=$(awk -v f="$BINARY" '$2==f{print $1}' /tmp/doq-web.sums)
        got_hash=$(sha256sum "$TMP" | awk '{print $1}')
        rm -f /tmp/doq-web.sums
        if [ -z "$want_hash" ]; then
            rm -f "$TMP"
            echo "Ошибка: $BINARY не найден в SHA256SUMS."
            exit 1
        fi
        [ "$got_hash" = "$want_hash" ] \
            || { rm -f "$TMP"; echo "Ошибка: SHA256 mismatch — corrupted download"; exit 1; }
        echo "SHA256 OK"
        hash_ok=1
    else
        echo "WARNING: sha256sum not found, skipping checksum"
    fi
fi

# 7. ELF и порядок байт — только если SHA256 не подтвердил файл.
if [ -z "$hash_ok" ]; then
    magic=$(elf_magic "$TMP")
    if [ "$magic" != "7f454c46" ]; then
        rm -f "$TMP"
        echo "Ошибка: файл не является ELF-бинарником (магия: '${magic:-пусто}', размер: $(wc -c < "$TMP" 2>/dev/null) байт)."
        echo "  - Нет доступа к raw.githubusercontent.com"
        echo "  - Файл не найден в ветке raw-bins"
        echo "Офлайн-установка: скачайте файл на ПК и выполните"
        echo "  sh install.sh --local /tmp/$BINARY"
        exit 1
    fi
    got=$(elf_endian "$TMP")
    if [ -n "$got" ] && [ "$got" != "$want" ]; then
        rm -f "$TMP"
        echo "Ошибка: порядок байт бинарника ($got) не совпадает с таргетом $TARGET (ожидался $want)."
        echo "Проверьте вывод: opkg print-architecture (Keenetic) или cat /etc/openwrt_release (OpenWRT)"
        exit 1
    fi
    echo "ELF OK (endianness ${got:-не проверен})"
fi

# 8. Бинарник должен запуститься: -version печатает версию и выходит.
# Именно эта проверка ловит "не та архитектура" надёжнее разбора заголовков.
#
# chmod +x ОБЯЗАТЕЛЕН до проверки: curl -o создаёт файл с правами 664/644 без
# бита исполнения, и запуск вернул бы "Permission denied", что иначе
# ошибочно читается как "wrong architecture".
chmod +x "$TMP"
if ver_out=$("$TMP" -version 2>&1); then
    echo "Binary OK: $ver_out"
else
    echo "Ошибка: бинарник не запускается (код $?): ${ver_out:-нет вывода}"
    echo "  размер: $(wc -c < "$TMP" 2>/dev/null) байт, магия: $(elf_magic "$TMP")"
    echo "  права: $(ls -l "$TMP" 2>/dev/null)"
    echo "  монтирование $BIN_DIR: $(mount 2>/dev/null | grep -F "$BIN_DIR" || echo 'не найдено')"
    echo "Если в строке монтирования есть noexec — раздел нельзя исполнять,"
    echo "скопируйте бинарник в другую partition или перемонтируйте её."
    rm -f "$TMP"
    exit 1
fi

# 9. Устанавливаем бинарный файл
mv "$TMP" "$BIN_DIR/$NAME"
sync
echo "Установлен бинарник: $BIN_DIR/$NAME"

# 10. Служба автозапуска.
# OpenWRT: procd-скрипт — respawn сам поднимет демона после сбоя, а
# enable создаст ссылки rc.d. Keenetic: rc.func из Entware (rc.unslung).
mkdir -p "$INIT_DIR"
if [ "$PLATFORM" = "openwrt" ]; then
    cat > "$SVC" << 'EOF'
#!/bin/sh /etc/rc.common

START=57
STOP=10
USE_PROCD=1
PROG=/usr/sbin/doq-web

start_service() {
    procd_open_instance
    procd_set_param command "$PROG"
    procd_set_param respawn
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}
EOF
    chmod +x "$SVC"
    "$SVC" enable
    echo "Служба procd создана и включена: $SVC"

    # doq-web должен переживать sysupgrade с сохранением настроек —
    # так же делает установщик апстрима для doqd (/lib/upgrade/keep.d/doqd).
    if [ -f /etc/sysupgrade.conf ] && ! grep -qs "/doq-web" /etc/sysupgrade.conf; then
        echo "$BIN_DIR/$NAME" >> /etc/sysupgrade.conf
        echo "$SVC" >> /etc/sysupgrade.conf
        echo "Добавлено в /etc/sysupgrade.conf (переживёт обновление прошивки)"
    fi
else
    cat > "$SVC" << 'EOF'
#!/bin/sh

ENABLED=yes
PROCS=doq-web
ARGS=""
PREARGS=""
DESC="keenetic-doq web service"
PATH=/opt/sbin:/opt/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

. /opt/etc/init.d/rc.func
EOF

    chmod +x "$SVC"

    # Попытка добавить автозапуск в rc.custom (есть не на всех прошивках).
    # На современных KeeneticOS /flash — read-only, и это не проблема: ndm при
    # монтировании раздела с /opt сам запускает скрипты /opt/etc/init.d/S*,
    # а S57doq-web создан выше. Неудачная запись не должна ронять
    # установку — на set -e перенаправление >> убило бы скрипт до запуска сервиса.
    RC_LINE='if [ -x /opt/etc/init.d/S57doq-web ]; then /opt/etc/init.d/S57doq-web start; fi'
    if grep -qs "S57doq-web" /flash/rc.custom 2>/dev/null; then
        echo "Автозапуск в /flash/rc.custom уже существует"
    elif echo "$RC_LINE" >> /flash/rc.custom 2>/dev/null; then
        chmod +x /flash/rc.custom
        echo "Добавлен автозапуск в /flash/rc.custom"
    else
        echo "rc.custom недоступен (read-only) — автозапуск через $SVC"
    fi
fi

# 11. Перезапускаем сервис.
#
# При обновлении старый doq-web уже запущен и держит порт 8091. Если просто
# вызвать start, rc.func увидит живой PID из pid-файла, решит "уже работает" и
# ничего не сделает — а проверка pidof ниже подтвердит успех, хотя в памяти
# останется старая версия. Поэтому сначала stop.
if pidof "$NAME" >/dev/null 2>&1; then
    echo "Останавливаю запущенную версию..."
    "$SVC" stop || true
    sleep 1
fi

if netstat -tln 2>/dev/null | grep -q ':8091 '; then
    echo "ВНИМАНИЕ: порт 8091 занят другим процессом:"
    netstat -tlnp 2>/dev/null | grep ':8091 '
fi

echo "Запускаю службу..."
"$SVC" start || true
sleep 2

# Проверка PID и диагностика при неудаче
if pidof "$NAME" >/dev/null 2>&1; then
    echo "Сервис работает, PID: $(pidof $NAME)"
else
    echo "СЕРВИС НЕ ЗАПУСТИЛСЯ. Диагностика окружения:"
    "$BIN_DIR/$NAME" --probe || echo "probe вернул код $?"
    echo "Пробую запустить в foreground (5 секунд):"
    "$BIN_DIR/$NAME" 2>&1 &
    FPID=$!
    sleep 5
    if kill -0 "$FPID" 2>/dev/null; then
        echo "В foreground процесс жив — проблема в init-скрипте/ENVIRONMENT."
        kill "$FPID" 2>/dev/null
    else
        wait "$FPID" 2>/dev/null
        echo "Процесс умер с кодом $? — выше должен быть текст ошибки."
    fi
    exit 1
fi

# 12. Определяем локальный IP-адрес роутера (Keenetic: br0/br1, OpenWRT: br-lan)
IP=$(ip -4 addr show br0 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP=$(ip -4 addr show br1 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP=$(ip -4 addr show br-lan 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP="<IP-роутера>"

echo ""
echo "=== Установка успешно завершена! ==="
echo "Веб-интерфейс доступен по адресу: http://$IP:8091"
echo ""
