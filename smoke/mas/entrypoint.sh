#!/bin/sh
# Runs MAS in the foreground (it migrates and syncs its config itself), then
# registers the admin once Synapse answers (MAS asks it whether the localpart
# is free) and mints the compatibility token carrying Synapse admin rights.
set -e
TOKEN=/var/lib/mas/admin-token.txt
CONFIG="--config /opt/mas/smoke.yaml --config /opt/mas/secrets.yaml"
rm -f "$TOKEN"
cd /opt/mas
for i in $(seq 1 60); do
  if ./mas-cli database migrate $CONFIG >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
./mas-cli server $CONFIG &
SERVER=$!
(
  for i in $(seq 1 120); do
    if curl -fsS http://127.0.0.1:8080/.well-known/openid-configuration >/dev/null 2>&1 \
       && curl -fsS http://synapse-mas:8008/_matrix/client/versions >/dev/null 2>&1; then
      ./mas-cli manage register-user $CONFIG --yes admin --password admin-password-smoke --admin --ignore-password-complexity || true
      ./mas-cli manage issue-compatibility-token $CONFIG admin --yes-i-want-to-grant-synapse-admin-privileges 2>&1 \
        | sed -n 's/.*[Cc]ompatibility token issued: *\([^ ]*\).*/\1/p' | head -n 1 > "$TOKEN.tmp" || true
      # Synapse rejects the token until MAS has synced the session's device
      # into it, which only a provisioning run does for a CLI-issued token.
      ./mas-cli manage provision-all-users $CONFIG >/dev/null 2>&1 || true
      for j in $(seq 1 60); do
        if curl -fsS -H "Authorization: Bearer $(cat "$TOKEN.tmp")" http://synapse-mas:8008/_matrix/client/v3/account/whoami >/dev/null 2>&1; then
          mv "$TOKEN.tmp" "$TOKEN"
          break
        fi
        sleep 2
      done
      exit 0
    fi
    sleep 2
  done
) &
wait $SERVER
