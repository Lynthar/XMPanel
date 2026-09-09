<div align="center">

<img src="web/public/favicon.svg" alt="XMPanel" width="72">

# XMPanel

[![license](https://img.shields.io/github/license/Lynthar/XMPanel)](LICENSE)
[![go](https://img.shields.io/github/go-mod/go-version/Lynthar/XMPanel)](go.mod)

</div>

Self-hosted web admin panel for Prosody XMPP servers, with RBAC, MFA and a tamper-evident audit log. Go + React.

English | [简体中文](README.zh-CN.md)

> **Under construction.** No release yet — you build it from source. The Prosody
> side has been deployed and used against a real server; the ejabberd adapter is
> written but hasn't been verified against one. Watch the repository if that's
> interesting, but don't expect a packaged product.

It sits outside the XMPP servers, with each server registered separately and
driven through an adapter, so it can manage several at once. It handles
accounts, live sessions and MUC rooms.

It has its own user system instead of borrowing the server's: short-lived JWTs
with refresh rotation, TOTP with recovery codes, Argon2id password hashing, five
permission levels. The audit log is chained with SHA-256, so a modified or
removed record breaks the chain (records written by builds before September 2026
were hashed with a timestamp precision the database does not keep and fail
verification — the chain is verifiable from the first record written after
upgrading); stored XMPP API keys are encrypted at rest with AES-256-GCM.

## Install

No packages, no container images and no releases; you build it yourself. You'll
need Go 1.24.7+, Node 20+, and PostgreSQL 14+ (the only supported database).

```bash
git clone https://github.com/Lynthar/XMPanel.git
cd XMPanel
```

```bash
sudo -u postgres psql -c "CREATE USER xmpanel WITH PASSWORD 'change-me';"
sudo -u postgres psql -c "CREATE DATABASE xmpanel OWNER xmpanel;"
```

```bash
cp config.example.yaml config.yaml
make generate-key          # prints the encryption key to paste into config.yaml
make deps
make build
./xmpanel
```

First start prints an initial `admin` password once; remember to save it.

> **Start it from the repository root.** The frontend is served from
> `web/dist` by relative path, and the config file defaults to `./config.yaml`.
> A systemd unit needs `WorkingDirectory=` set accordingly.

Prosody needs preparation on its side: three community modules
(`mod_http_admin_api`, `mod_tokenauth`, `mod_http_oauth2`), a small patch to
`mod_tokenauth`, a Bearer token to authenticate with, and this repository's
`prosody/mod_admin_panel.lua` installed. Upstream's own admin API returns 200 for
account creation without creating anything, and doesn't expose sessions at all —
the extra module exists to solve both of those.

## Usage

Open `http://localhost:8080` and sign in as `admin`. If you've lost the password:

```bash
./xmpanel --reset-admin
```

That resets only the `admin` account — new password, MFA cleared, its sessions
revoked — then exits.

Add a server from the Servers page. Set type to `prosody`, and put the **XMPP
virtual host name** in the host field, not an IP: `mod_http_admin_api` routes by
HTTP Host header, so an IP address gets a 404 every time. On a single machine,
map the domain to loopback in `/etc/hosts`.

The API is reachable directly if you'd rather script it:

```bash
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<pw>"}'
```

Non-safe methods also need `X-CSRF-Token`, read from the `csrf_token` cookie.

## Configuration

`config.yaml` in the working directory, or wherever `XMPANEL_CONFIG` points.
`config.example.yaml` is the annotated reference.

| Key | Notes |
|---|---|
| `database.dsn` | PostgreSQL connection string |
| `database.encryption_key` | base64 32 bytes. **Leave it empty and one is generated per start**, making previously encrypted columns unreadable after a restart |
| `security.jwt.secret` | At least 32 characters, enforced. Same restart caveat — every session is invalidated |
| `security.cookies.secure_override` | `auto`, `always` or `never` — use `always` behind a TLS-terminating proxy |
| `security.rate_limit.trust_x_forwarded_for` | Only with a trusted proxy listed in `trusted_proxies`, or clients can forge their source IP. Governs every client address the panel records — rate limiting, login lockout, sessions and the audit log |
| `server.address` | Default `:8080` |

Set both of the secrets above before you put real data in.

## Limitations

- **The ejabberd adapter is unverified.** It's written against the documented
  API but hasn't been run against a real ejabberd server, so its stated
  capabilities are intent, not confirmed behaviour.
- **MUC room management only exists on the ejabberd side** — which is the
  unverified one. Prosody's upstream API doesn't expose rooms.
- **XMPP accounts can be created, listed and deleted, but not edited.** Password
  changes exist in the adapter layer with no route or UI reaching them.
- **PostgreSQL only.** No SQLite, no MySQL.
- **No Dockerfile and no compose file.** Source build and a systemd unit.
- **Refreshing in several browser tabs at once trips the token reuse detector**
  and signs that user out everywhere.

## Security

Sessions use short-lived access tokens with refresh rotation; a refresh token
presented twice invalidates the whole session. CSRF uses double-submit on every
authenticated mutation. Passwords are hashed with Argon2id. Stored XMPP API keys
are encrypted with AES-256-GCM.

Failed logins are recorded in the database audit log but not written to stderr,
so fail2ban has nothing to match on yet.

There's no private disclosure channel; please don't file sensitive findings as
public issues.

## License

Apache License 2.0 — see [LICENSE](LICENSE). Copyright (c) 2026 Lynthar.
