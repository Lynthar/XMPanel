#!/bin/sh
# Runs Synapse in the foreground as the package's user, registers the admin
# through the shared secret once the server answers, and writes the admin's
# access token for the smoke test to read.
set -e
TOKEN=/var/lib/matrix-synapse/admin-token.txt
VENV=/opt/venvs/matrix-synapse
CONFIG="--config-path /etc/matrix-synapse/homeserver.yaml --config-path /etc/matrix-synapse/conf.d/"
rm -f "$TOKEN"
chown -R matrix-synapse /var/lib/matrix-synapse /etc/matrix-synapse
runuser -u matrix-synapse -- $VENV/bin/python -m synapse.app.homeserver $CONFIG --generate-keys
(
  for i in $(seq 1 90); do
    if curl -fsS http://127.0.0.1:8008/_matrix/client/versions >/dev/null 2>&1; then
      $VENV/bin/register_new_matrix_user -k smoke-registration-secret -u admin -p admin-password-smoke -a http://127.0.0.1:8008 || true
      curl -fsS -X POST http://127.0.0.1:8008/_matrix/client/v3/login -H 'Content-Type: application/json' \
        -d '{"type":"m.login.password","identifier":{"type":"m.id.user","user":"admin"},"password":"admin-password-smoke","initial_device_display_name":"smoke"}' \
        | python3 -c 'import json, sys; print(json.load(sys.stdin)["access_token"])' > "$TOKEN.tmp"
      mv "$TOKEN.tmp" "$TOKEN"
      exit 0
    fi
    sleep 2
  done
) &
exec runuser -u matrix-synapse -- $VENV/bin/python -m synapse.app.homeserver $CONFIG
