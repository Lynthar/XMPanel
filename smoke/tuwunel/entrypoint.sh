#!/bin/sh
# Runs Tuwunel in the foreground and, once it answers, registers the admin
# through the shared secret and writes its access token.
set -e
TOKEN=/var/lib/tuwunel/admin-token.txt
rm -f "$TOKEN"
(
  for i in $(seq 1 90); do
    if curl -fsS http://127.0.0.1:8008/_matrix/client/versions >/dev/null 2>&1; then
      python3 /smoke-register.py > "$TOKEN.tmp"
      mv "$TOKEN.tmp" "$TOKEN"
      exit 0
    fi
    sleep 2
  done
) &
export TUWUNEL_CONFIG=/etc/tuwunel/smoke.toml
exec /usr/sbin/tuwunel
