#!/bin/sh

echo "Останавливаю doq-web..."
/opt/etc/init.d/S57doq-web stop 2>/dev/null || true

rm -f /opt/sbin/doq-web
rm -f /opt/etc/init.d/S57doq-web

echo "keenetic-doq-web удалён."