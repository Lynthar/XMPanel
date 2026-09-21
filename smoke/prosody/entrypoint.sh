#!/bin/sh
# Registers the admin and the seed account, then runs Prosody in the
# foreground as the prosody user so the token helper can issue the bearer
# token on start.
set -e
mkdir -p /var/run/prosody && chown prosody:prosody /var/run/prosody
rm -f /var/lib/prosody/admin-token.txt
runuser -u prosody -- prosodyctl register admin example.com admin-password-smoke || true
runuser -u prosody -- prosodyctl register seed example.com seed-password-smoke || true
exec runuser -u prosody -- prosody
