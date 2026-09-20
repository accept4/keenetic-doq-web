#!/bin/sh
set -e

REPO="accept4/keenetic-doq-web"
BIN_DIR="/opt/sbin"
INIT_DIR="/opt/etc/init.d"
NAME="doq-web"

echo "=== keenetic-doq-web installer ==="

# 1. Проверяем наличие Entware
if [ ! -d "/opt/bin" ] && [ ! -d "/opt/sbin" ]; then
    echo "Ошибка: Среда Entware не обнаружена в /opt!"
    echo "Установите Entware на роутер перед запуском скрипта."
    exit 1
fi

# 2. Проверяем утилиту curl
if ! command -v curl >/dev/null 2>&1; then
    echo "Ошибка: curl не установлен!"
    echo "Установите его командой: opkg update && opkg install curl"
    exit 1
fi

# 3. Определяем архитектуру системы
ARCH=$(uname -m)
case "$ARCH" in
  aarch64|arm64)  BINARY="doq-web-linux-arm64" ;;
  mipsel|mips)    BINARY="doq-web-linux-mipsle" ;;
  mips64)         BINARY="doq-web-linux-mips64" ;;
  x86_64)         BINARY="doq-web-linux-amd64" ;;
  *)
    echo "Ошибка: Неподдерживаемая архитектура: $ARCH"
    exit 1
    ;;
esac

# 4. Скачиваем бинарник из последней версии GitHub Releases
TMP="/tmp/$BINARY"
URL="https://github.com/$REPO/releases/latest/download/$BINARY"

echo "Скачиваю $BINARY ..."
curl -fsSL -o "$TMP" "$URL" || {
  echo "Ошибка: Не удалось скачать $BINARY."
  echo "Проверьте интернет-соединение и наличие скомпилированного файла в GitHub Releases."
  exit 1
}

# 5. Устанавливаем бинарный файл
mkdir -p "$BIN_DIR"
chmod +x "$TMP"
mv "$TMP" "$BIN_DIR/$NAME"
echo "Установлен бинарник: $BIN_DIR/$NAME"

# 6. Создаем службу автозапуска для Entware (rc.unslung)
mkdir -p "$INIT_DIR"
cat > "$INIT_DIR/S57doq-web" << 'EOF'
#!/bin/sh

ENABLED=yes
PROCS=doq-web
ARGS=""
PREARGS=""
DESC="keenetic-doq web service"
PATH=/opt/sbin:/opt/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

. /opt/etc/init.d/rc.func
EOF

chmod +x "$INIT_DIR/S57doq-web"

# 7. Запускаем сервис
echo "Запускаю службу..."
"$INIT_DIR/S57doq-web" start || true

# 8. Определяем локальный IP-адрес роутера
IP=$(ip -4 addr show br0 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP=$(ip -4 addr show br1 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP="<IP-роутера>"

echo ""
echo "=== Установка успешно завершена! ==="
echo "Веб-интерфейс доступен по адресу: http://$IP:8088"
echo ""
