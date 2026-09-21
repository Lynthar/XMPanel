<div align="center">

<img src="web/public/favicon.svg" alt="XMPanel" width="72">

# XMPanel

[![license](https://img.shields.io/github/license/Lynthar/XMPanel)](LICENSE)
[![go](https://img.shields.io/github/go-mod/go-version/Lynthar/XMPanel)](go.mod)

</div>

Self-hosted web admin panel for XMPP servers (Prosody, ejabberd) and Matrix homeservers (Synapse, Tuwunel), with RBAC, MFA and a tamper-evident audit log. Go + React. The Matrix side is still being extended.

English | [简体中文](README.zh-CN.md)

> **Under construction.** No release yet — you build it from source. The Prosody
> side has been deployed and used against a real server; all three adapters are
> exercised against real servers in CI (`smoke/`). Watch the repository if
> that's interesting, but don't expect a packaged product.

It sits outside the servers, with each one registered separately and driven
through a protocol-neutral adapter, so it can manage several at once. It
handles accounts, live sessions and rooms, and only offers what each server
actually supports: the panel probes a server when it is registered and hides
the operations its adapter cannot perform.

It has its own user system instead of borrowing the server's: short-lived JWTs
with refresh rotation, TOTP with recovery codes, Argon2id password hashing, five
permission levels. The audit log is chained with SHA-256, so a modified or
removed record breaks the chain (records written by builds before September 2026
were hashed with a timestamp precision the database does not keep and fail
verification — the chain is verifiable from the first record written after
upgrading); stored server credentials are encrypted at rest with AES-256-GCM.

## Install

No packages, no container images and no releases; you build it yourself. You'll
need Go 1.24.7+, Node 20.19+ / 22.13+ / 24+, and PostgreSQL 14+ (the only supported database).

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

Synapse needs an admin account's access token (from `register_new_matrix_user -a`,
or an admin's login). The endpoint is the client-server listener, and both
`/_matrix` and `/_synapse/admin` must be reachable there. A deployment that
delegates authentication to Matrix Authentication Service is detected. Its admin
token then comes from MAS (`mas-cli manage issue-compatibility-token <admin>
--yes-i-want-to-grant-synapse-admin-privileges`, followed by a provisioning run so
that Synapse learns the session's device), and the account lifecycle goes through
MAS's admin API once the server entry carries a MAS client: an OAuth 2.0 client
declared with `client_secret_basic` and listed in `policy.data.admin_clients`.
Without that client the panel still lists accounts, devices and rooms, revokes
devices and purges rooms, but does not create, lock, deactivate or change passwords.

Tuwunel serves the Synapse admin API itself, so it needs the same kind of token:
one from a server admin, meaning a member of its admin room (the first account
registered, or one promoted with the admin room's `!admin users` commands). MAS is not
involved: Tuwunel does not accept MAS tokens, and its own next-generation auth
does not change what the panel can do.

## Usage

Open `http://localhost:8080` and sign in as `admin`. If you've lost the password:

```bash
./xmpanel --reset-admin
```

That resets only the `admin` account — new password, MFA cleared, its sessions
revoked — then exits.

Add a server from the Servers page. A server has two addresses: the **admin
API endpoint** the panel connects to (usually a loopback URL such as
`http://127.0.0.1:5280`) and the **domain** the accounts belong to (the XMPP
VirtualHost, or the Matrix `server_name`). For Prosody the domain is sent as the HTTP Host header, which is
how `mod_http_admin_api` picks the VirtualHost, so the endpoint may be an IP
address. "Test connection" probes the server and reports what it supports.

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
| `database.encryption_key` | base64 32 bytes, **required**: the panel refuses to start without it, since server credentials encrypted under a lost key cannot be recovered |
| `security.jwt.secret` | At least 32 characters, enforced. Left empty, one is generated per start and every session is invalidated on restart |
| `security.cookies.secure_override` | `auto`, `always` or `never` — use `always` behind a TLS-terminating proxy |
| `security.rate_limit.trust_x_forwarded_for` | Only with a trusted proxy listed in `trusted_proxies`, or clients can forge their source IP. Governs every client address the panel records — rate limiting, login lockout, sessions and the audit log |
| `server.address` | Default `:8080` |

Set the JWT secret before you put real data in; the encryption key is checked at startup.

## Limitations

- **Room management exists on ejabberd and Synapse.** Prosody's upstream API
  doesn't expose rooms.
- **XMPP listings are paged in the panel, not by the server.** Both XMPP adapters
  fetch the full account or session list and page it in memory, so very large
  servers are slow to list. Synapse pages on the server.
- **Matrix support covers Synapse and Tuwunel, and is partial.** On Synapse,
  password (legacy) authentication is fully supported; behind Matrix
  Authentication Service the account lifecycle needs the MAS client described above.
  Deleting an account deactivates it (Matrix has no deletion) and its id stays
  taken; deleting a room starts the server's background purge, so the room can
  linger in listings for a moment. No room creation and no global device list. The
  moderation tools (erase, suspend, shadow ban, media quarantine, registration
  tokens, reports, room block and purge, server notices, federation health) are
  in; erase, shadow ban, block and purge need the admin role and a typed-out id.
  Server notices need `server_notices` configured on Synapse, which the panel
  cannot detect up front: without it the button answers "not supported".
  Tuwunel has no shadow ban, no event reports and no media quarantine, and its
  registration tokens are managed by MAS once `mas_secret` is set; the panel
  hides those tools there.
- **PostgreSQL only.** No SQLite, no MySQL.
- **No container image for the panel itself.** Source build and a systemd
  unit; the compose file under `smoke/` only starts test servers.
- **Refreshing in several browser tabs at once trips the token reuse detector**
  and signs that user out everywhere.

## Security

Sessions use short-lived access tokens with refresh rotation; a refresh token
presented twice invalidates the whole session. CSRF uses double-submit on every
authenticated mutation. Passwords are hashed with Argon2id. Stored server
credentials are encrypted with AES-256-GCM.

Failed logins are recorded in the database audit log but not written to stderr,
so fail2ban has nothing to match on yet.

There's no private disclosure channel; please don't file sensitive findings as
public issues.

## License

Apache License 2.0 — see [LICENSE](LICENSE). Copyright (c) 2026 Lynthar.
