#!/bin/sh

echo "Останавливаю doq-web..."
if [ -x /opt/etc/init.d/S57doq-web ]; then
    /opt/etc/init.d/S57doq-web stop >/dev/null 2>&1 || true
fi

rm -f /opt/sbin/doq-web
rm -f /opt/etc/init.d/S57doq-web

echo "keenetic-doq-web удалён."
