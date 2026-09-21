#!/bin/sh
# Runs Continuwuity in the foreground and, once the startup command has
# created the admin, logs that admin in and writes its access token.
set -e
TOKEN=/var/lib/continuwuity/admin-token.txt
rm -f "$TOKEN"
(
  for i in $(seq 1 90); do
    if curl -fsS -X POST http://127.0.0.1:8008/_matrix/client/v3/login -H 'Content-Type: application/json' \
      -d '{"type":"m.login.password","identifier":{"type":"m.id.user","user":"admin"},"password":"admin-password-smoke","initial_device_display_name":"smoke"}' \
      | python3 -c 'import json, sys; print(json.load(sys.stdin)["access_token"])' > "$TOKEN.tmp" 2>/dev/null; then
      mv "$TOKEN.tmp" "$TOKEN"
      exit 0
    fi
    sleep 2
  done
) &
exec /usr/local/bin/continuwuity --config /etc/continuwuity/smoke.toml
