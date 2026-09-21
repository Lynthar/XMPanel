#!/bin/sh
# Starts ejabberd in the foreground and registers the admin account once the
# node answers; ejabberdctl needs the running node. The Erlang cookie is
# written up front so the status poller and the server do not race to create it.
set -e
mkdir -p /run/ejabberd /var/lib/ejabberd /var/log/ejabberd
if [ ! -s /var/lib/ejabberd/.erlang.cookie ]; then
  echo "xmpanel-smoke-cookie" > /var/lib/ejabberd/.erlang.cookie
  chmod 400 /var/lib/ejabberd/.erlang.cookie
fi
chown -R ejabberd:ejabberd /run/ejabberd /var/lib/ejabberd /var/log/ejabberd
(
  sleep 3
  for i in $(seq 1 60); do
    if runuser -u ejabberd -- ejabberdctl status >/dev/null 2>&1; then
      runuser -u ejabberd -- ejabberdctl register admin example.com admin-password-smoke || true
      exit 0
    fi
    sleep 2
  done
) &
exec runuser -u ejabberd -- ejabberdctl foreground
