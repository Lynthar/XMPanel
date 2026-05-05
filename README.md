# XMPanel

A secure web admin panel for XMPP servers (Prosody and ejabberd) with a unified Go backend and React + TypeScript frontend.

> **Looking for a real deployment guide?** See [`docs/DEPLOY_DEBIAN.md`](docs/DEPLOY_DEBIAN.md) for an end-to-end Debian 13 + Prosody 13 + PostgreSQL 17 walkthrough, and [`docs/TROUBLESHOOTING.md`](docs/TROUBLESHOOTING.md) for symptom-based fixes. This README is just an orientation.

## Features

- Manage multiple Prosody / ejabberd servers from one panel (adapter pattern in `internal/adapter/`)
- XMPP user CRUD + live c2s session listing & disconnect (Prosody requires the bundled `prosody/mod_admin_panel.lua`)
- MUC room management (ejabberd only — Prosody 13 `mod_http_admin_api` does not expose MUC, so the Rooms tab is hidden via the `/servers/{id}/capabilities` endpoint)
- JWT auth with short-lived access tokens, refresh-token rotation in an HttpOnly cookie, and double-submit CSRF
- TOTP-based MFA + recovery codes (one-time view at enrollment)
- Argon2id password hashing, configurable policy
- IP+username login rate limiting, optional global per-IP rate limit
- Tamper-evident audit log with SHA-256 chain (`/audit/verify` re-hashes and reports the first break)
- AES-256-GCM encryption at rest for stored XMPP API keys (KeyRing supports key rotation)
- RBAC: `superadmin`, `admin`, `operator`, `viewer`, `auditor`
- Server-side i18n for auth errors (`en`/`zh`); full client-side i18n via i18next

## Architecture

```
Browser ─┬─ Vite dev (:5173, /api → :8080) ─┐
         └─ nginx :443 (TLS) ───────────────┤
                                            ▼
                          Go server :8080 (cmd/server)
                                │
              middleware: Recovery → SecurityHeaders → RequestID
                          → LocaleMiddleware → CORS → [RateLimit]
                                │
                          handler.* (auth/user/server/xmpp/audit)
                                │
                          adapter.XMPPAdapter
                          ├─ prosody.Adapter (mod_http_admin_api + mod_admin_panel)
                          └─ ejabberd.Adapter (mod_http_api)
                                │
                          PostgreSQL (audit_logs / users / sessions / xmpp_servers / settings)
```

Backend is Go 1.24 with `net/http` + Go 1.22+ `ServeMux` patterns (no third-party router). Frontend is React 18 + Vite + Tailwind, served from `web/dist/` by the Go binary in production.

## Quick start

### Prerequisites

- Go **1.24+** (toolchain pinned to `1.24.7` in `go.mod`)
- Node **20+**
- PostgreSQL **14+** (the only supported database — see "Database" below)

### Build & run

```bash
# 1. Create a PostgreSQL user + database
sudo -u postgres psql <<SQL
CREATE USER xmpanel WITH PASSWORD 'change-me';
CREATE DATABASE xmpanel OWNER xmpanel;
SQL

# 2. Copy + edit config
cp config.example.yaml config.yaml
# Set database.dsn, security.jwt.secret (>=32 chars), database.encryption_key
make generate-key   # prints a base64 32-byte key for database.encryption_key

# 3. Build
make deps
make build          # produces ./xmpanel + web/dist/

# 4. Run — first start prints "INITIAL ADMIN ACCOUNT CREATED" with a random
# 16-char password (only logged once; save it).
./xmpanel
```

`./xmpanel` listens on the address from `server.address` (default `:8080`). For production behind nginx see `docs/DEPLOY_DEBIAN.md`.

### Development

```bash
make run            # backend on :8080 (no frontend build)
make dev-frontend   # vite on :5173, proxies /api → :8080
```

Override config path with `XMPANEL_CONFIG=/path/to/config.yaml ./xmpanel`.

### Useful CLI flags

- `--reset-admin` — recreate the `admin` account with a fresh random password, clear its MFA, revoke its sessions. Other accounts untouched. Use when the initial admin password is lost.

## Database

**PostgreSQL only.** Older versions of this README mentioned SQLite; that path was removed in commit `f79c231`. The `database.driver` config field is parsed but ignored — `internal/store/db.go` always opens a PG connection. Migrations are idempotent SQL run on startup (no migration framework).

DSN default if none configured:

```
host=localhost port=5432 user=xmpanel password=xmpanel dbname=xmpanel sslmode=disable
```

## Authentication / cookies

- **Access token**: 15-minute JWT, returned in the JSON login response, attached to API calls via `Authorization: Bearer <token>`.
- **Refresh token**: 7-day JWT delivered out-of-band in the `xmpanel_refresh` HttpOnly cookie (Path=`/api/v1/auth`, SameSite=Strict). Rotated on every `/auth/refresh`; reuse of an old refresh token kills the entire session.
- **CSRF token**: random 32-char value in the `csrf_token` cookie (Path=`/`, JS-readable). The SPA mirrors it into `X-CSRF-Token` on every non-safe request; `middleware.CSRFMiddleware.Protect` enforces double-submit on `/auth/refresh` and all authenticated mutations.
- `cookies.secure_override`: `auto` (default; mirrors `server.tls.enabled`), `always` (force `Secure`; correct when nginx terminates TLS and Go runs HTTP loopback), or `never` (local dev over plain HTTP).

## Configuration

See [`config.example.yaml`](config.example.yaml) for the full schema. Notable fields:

| Path | Description | Default |
|---|---|---|
| `server.address` | HTTP listen address | `:8080` |
| `server.tls.enabled` | Direct TLS termination by Go (skip if nginx fronts) | `false` |
| `database.dsn` | Postgres DSN (libpq style) | local xmpanel/xmpanel |
| `database.encryption_key` | Base64 32-byte key for AES-GCM at-rest encryption — **set this in production** | auto-generated, ephemeral |
| `security.jwt.secret` | HS256 signing secret, **≥32 chars enforced** | auto-generated, ephemeral |
| `security.cookies.secure_override` | `auto` / `always` / `never` | `auto` |
| `security.rate_limit.trust_x_forwarded_for` | Read client IP from `X-Forwarded-For` (only enable behind a trusted proxy listed in `trusted_proxies`) | `false` |
| `security.cors.allowed_origins` | List of origins; cannot combine `*` with `allow_credentials: true` | none |
| `security.mfa.required` | **Currently parsed but not enforced** — admins must enroll voluntarily | `false` |

> **Set both `jwt.secret` and `database.encryption_key`** before going to production. If either is empty, `config.Validate` generates a random one with a warning — sessions and encrypted columns become unreadable across restarts.

## XMPP server setup

### Prosody 13

Prosody 13's `mod_http_admin_api` does **not** support a static API key, MUC management, or live session listing. To make XMPanel work end-to-end you need:

1. `mod_http_admin_api` + `mod_tokenauth` + `mod_http_oauth2` enabled on the VirtualHost (community modules)
2. A patch to `mod_tokenauth.lua`'s `select_role` (silent 401 bug — see `docs/TROUBLESHOOTING.md` deep dive)
3. A short-lived helper module to mint a Bearer admin token via `mod_tokenauth` (no static key exists)
4. **`prosody/mod_admin_panel.lua` from this repo** copied into `/etc/prosody/modules/` and enabled on the VirtualHost — it provides real user CRUD (the upstream `PUT /admin_api/users/{u}` returns 200 but does not actually create an account in 13.0.5) and the `/admin_panel/sessions/*` endpoints used by the Sessions tab

Full step-by-step in `docs/DEPLOY_DEBIAN.md` §2. Use the **token** as the API key field when adding the server in the panel.

### ejabberd

Enable `mod_http_api` and configure `api_permissions` to allow your admin user. ejabberd's admin API is more complete than Prosody's; the adapter has not been validated against a real ejabberd server, so expect to verify endpoint paths against your version. See `docs/DEPLOY_DEBIAN.md` Appendix B for a starter config.

## Selected API endpoints

Authenticated routes live under `/api/v1` and require `Authorization: Bearer <access>` plus `X-CSRF-Token` on non-safe methods.

```
POST   /api/v1/auth/login                      Login (no CSRF; sets cookies)
POST   /api/v1/auth/refresh                    Rotate refresh token (cookie + CSRF)
POST   /api/v1/auth/logout                     Revoke session
GET    /api/v1/auth/me                         Current user
POST   /api/v1/auth/mfa/{setup,verify,disable} TOTP enrollment / disable
POST   /api/v1/auth/password                   Change own password

GET    /api/v1/users                           Admin/SuperAdmin only
POST   /api/v1/users
GET    /api/v1/users/{id}
PUT    /api/v1/users/{id}
DELETE /api/v1/users/{id}

GET    /api/v1/servers
POST   /api/v1/servers                         servers:write
GET    /api/v1/servers/{id}
PUT    /api/v1/servers/{id}                    servers:write
DELETE /api/v1/servers/{id}                    servers:write
GET    /api/v1/servers/{id}/stats
GET    /api/v1/servers/{id}/capabilities       Hint set the UI uses to hide tabs
POST   /api/v1/servers/{id}/test               Test connection

GET    /api/v1/servers/{serverId}/users        XMPP-side ops
POST   /api/v1/servers/{serverId}/users                                xmpp:write
DELETE /api/v1/servers/{serverId}/users/{username}                     xmpp:write
POST   /api/v1/servers/{serverId}/users/{username}/kick                xmpp:write
GET    /api/v1/servers/{serverId}/sessions
DELETE /api/v1/servers/{serverId}/sessions/{jid}                       xmpp:write
GET    /api/v1/servers/{serverId}/rooms        ejabberd only
POST   /api/v1/servers/{serverId}/rooms                                xmpp:write
DELETE /api/v1/servers/{serverId}/rooms/{room}                         xmpp:write

GET    /api/v1/audit                           audit:read
GET    /api/v1/audit/verify                    audit:read
GET    /api/v1/audit/export                    audit:read (CSV)
```

## Roles and permissions

| Role | Default permissions |
|---|---|
| `superadmin` | `*` (all) |
| `admin` | `users:*`, `servers:*`, `xmpp:*`, `audit:read` |
| `operator` | `servers:read`, `xmpp:read`, `xmpp:write` |
| `viewer` | `servers:read`, `xmpp:read` |
| `auditor` | `audit:read`, `servers:read` |

Source of truth: `internal/store/models/user.go` `Permissions` map.

## Security checklist for production

1. Terminate TLS in front (nginx) **and** set `cookies.secure_override: always`
2. Set `security.jwt.secret` to a ≥32-char random value (persisted)
3. Set `database.encryption_key` to a base64 32-byte key (persisted; `make generate-key`)
4. Enable MFA on every privileged account (admin / superadmin / auditor) — `mfa.required: true` is **not yet enforced**, you must enroll manually
5. If behind a proxy, set `rate_limit.trust_x_forwarded_for: true` and list the proxy in `rate_limit.trusted_proxies`
6. Watch the audit log for `auth.login_failed` clusters; consider fail2ban (see `docs/DEPLOY_DEBIAN.md` §6.1 — note: the example filter currently does not match log lines as-is and is documented as a starting point)
7. Rotate the `database.encryption_key` periodically by adding a second key to the KeyRing — re-encryption is supported but no admin UI exposes it yet

## License

MIT — see [LICENSE](LICENSE).
