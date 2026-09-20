#!/bin/sh
set -e

REPO="accept4/keenetic-doq-web"   # ← поменяй на свой
VERSION="latest"
BIN_DIR="/opt/sbin"
INIT_DIR="/opt/etc/init.d"
NAME="doq-web"

echo "=== keenetic-doq-web installer ==="

# Определяем архитектуру
ARCH=$(uname -m)
case "$ARCH" in
  aarch64|arm64)  BINARY="doq-web-linux-arm64" ;;
  mipsel|mips)    BINARY="doq-web-linux-mipsle" ;;
  *)
    echo "Неподдерживаемая архитектура: $ARCH"
    exit 1
    ;;
esac

# Скачиваем бинарник из релизов
TMP="/tmp/$BINARY"
URL="https://github.com/$REPO/releases/latest/download/$BINARY"

echo "Скачиваю $BINARY ..."
curl -fsSL -o "$TMP" "$URL" || {
  echo "Ошибка скачивания. Проверь, что релиз существует."
  exit 1
}

chmod +x "$TMP"
mv "$TMP" "$BIN_DIR/$NAME"
echo "Установлен: $BIN_DIR/$NAME"

# Ставим init-скрипт
cat > "$INIT_DIR/S57doq-web" << 'EOF'
#!/bin/sh
ENABLED=yes
PROCS=doq-web
ARGS=""
PREARGS=""
DESC="keenetic-doq web"
PATH=/opt/sbin:/opt/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

. /opt/etc/init.d/rc.func
EOF

chmod +x "$INIT_DIR/S57doq-web"

# Запускаем
"$INIT_DIR/S57doq-web" start

IP=$(ip -4 addr show br0 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP=$(nvram get lan_ipaddr 2>/dev/null || echo "IP-роутера")

echo ""
echo "Готово!"
echo "Веб-интерфейс: http://$IP:8088"
echo ""