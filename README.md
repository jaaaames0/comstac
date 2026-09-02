# comstac

A self-hosted, single-user mail client in a single Go binary. Receives local-domain SMTP mail, aggregates external accounts via IMAP, and serves a unified inbox through a terminal-style web UI.

---

## What it does

- **Receives inbound SMTP** for your own domain — acts as a minimal MTA for configured local recipients
- **Aggregates Gmail (or any IMAP)** via OAuth2, polling on a configurable interval
- **Unified inbox** — SMTP and IMAP messages appear in one stream, filterable by source
- **Full-text search** across subject, sender, and body text
- **Compose and reply** through a configured SMTP smart-host relay — with reply-all, forward, attachment uploads, and CC/BCC
- **SPF / DKIM / DMARC** validation on inbound mail with per-message auth badges
- **Spam auto-flagging** when both SPF and DKIM hard-fail
- **Agent real-time alerts** (SSE) — OpenClaw agent connects via HTTPS to receive instant `new_mail` events when messages arrive; no polling, no SSH tunnel
- **Web Push notifications** (VAPID) for new mail — tapping a notification opens the specific email directly
- **Attachment download** on demand, streamed directly from the database
- **Snooze, archive, spam-flag** actions with optional IMAP write-back
- **Keyboard shortcuts** — `j`/`k` navigation, `r` reply, `e` trash, `u` unread, `c` compose, `Escape` back
- **Mobile layout** — swipe-to-trash, push-pattern navigation, two-row topbar
- **Automated backup** — SQLite snapshot via `comstac backup`, with optional remote SCP and daily systemd timer

## Design

- **Single binary** — one `comstac` process runs everything (SMTP receiver, IMAP fetcher, HTTP server, background sync, push notifier)
- **SQLite** is the sole datastore — raw MIME, normalized messages, auth sessions, sync jobs, push subscriptions all in one file
- **No Docker, no Redis** — nginx as a TLS-terminating reverse proxy is recommended but not required
- **~100 MB idle RAM** target under typical single-user load
- **Embedded frontend** — HTML/CSS/JS templates compiled into the binary via `go:embed`; no build step needed for the UI

---

## Requirements

- **Go 1.21+** (the module uses `go 1.25.0` syntax; any recent toolchain works)
- A Linux host with outbound SMTP access via a smart-host relay (port 587)
- **nginx** (recommended) as a TLS-terminating reverse proxy with Let's Encrypt
- For Gmail IMAP: a Google Cloud project with an OAuth2 credential

---

## Installation

### 1. Build

```bash
git clone https://github.com/youruser/comstac.git
cd comstac
go test ./...
go build -trimpath -buildvcs=true -o comstac ./cmd/comstac
```

For production, keep the source checkout, protected configuration, mutable
state and root-owned versioned runtime separate. The deployment section below
describes the required shape; the Makefile intentionally refuses an in-place
install over a running binary.

### 2. Configure

Create a protected environment file outside the source checkout and fill in
your values:

```bash
install -m 0600 /dev/null /path/to/comstac.env
$EDITOR /path/to/comstac.env
```

At minimum you need:

```bash
COMSTAC_SMTP_ADDR=:2525
COMSTAC_SMTP_DOMAIN=mail.example.com
COMSTAC_HTTP_ADDR=127.0.0.1:8080
COMSTAC_DB_PATH=/absolute/path/to/comstac.db
COMSTAC_LOCAL_DOMAINS=example.com
COMSTAC_LOCAL_RECIPIENTS=you@example.com
COMSTAC_ADMIN_USERNAME=you
COMSTAC_ADMIN_PASSWORD=replace-with-a-long-random-password
COMSTAC_CSRF_SECRET=$(openssl rand -hex 32)
```

See [Configuration](#configuration) below for all options.

### 3. Run

```bash
set -a
source /path/to/comstac.env
set +a
./comstac check-config
./comstac
# or for development:
make run
```

The web UI is available at `http://localhost:8080` (or whatever `COMSTAC_HTTP_ADDR` is set to). Log in with your configured admin credentials.

### 4. nginx + TLS (production)

Point nginx at the explicitly configured loopback `COMSTAC_HTTP_ADDR` and terminate TLS with Let's Encrypt. A minimal server block:

```nginx
server {
    listen 443 ssl;
    server_name mail.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }

    # Defense in depth around Comstac's built-in shared login limiter.
    location = /login {
        limit_req zone=login burst=3 nodelay;
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }

    location = /api/login {
        limit_req zone=login burst=3 nodelay;
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```

### 5. Systemd service

Do not overwrite a running binary with `make install`. Build from a clean
commit with `-trimpath -buildvcs=true`, verify tests and embedded build metadata,
then place the candidate in a new root-owned, non-writable versioned directory
such as `/usr/local/lib/comstac/RELEASE/comstac`. Keep configuration under
`/etc/comstac` and mutable SQLite state under `/var/lib/comstac`. Point the unit
at the exact versioned path and restart Comstac only under an independently
timed rollback. Retain the previous release until real inbound/outbound mail,
login, web, database, backup and monitoring checks pass.

To inspect an already installed service:

```bash
systemctl status comstac
journalctl -u comstac -f
```

---

## Gmail IMAP setup

Comstac fetches Gmail via IMAP with OAuth2 — no app password needed.

### One-time authorization

1. Create an OAuth2 **Desktop app** credential in [Google Cloud Console](https://console.cloud.google.com/) with the `https://mail.google.com/` scope
2. Download the `client_secret_*.json` file to your server
3. Set `COMSTAC_IMAP_CLIENT_ID` and `COMSTAC_IMAP_CLIENT_SECRET` in your env file
4. Run the authorization flow:

```bash
source comstac.env && ./comstac authorize
# Opens a browser URL — paste it on your local machine, authorize, paste the code back
```

5. Copy the printed `COMSTAC_IMAP_REFRESH_TOKEN` value into your env file

### Web-based re-authorization

If the refresh token expires (`invalid_grant` error), you can re-authorize from the web UI without touching the server:

1. Create a separate **Web Application** OAuth2 credential with redirect URI `https://mail.example.com/ui/oauth/callback`
2. Set `COMSTAC_OAUTH_CLIENT_ID`, `COMSTAC_OAUTH_CLIENT_SECRET`, and `COMSTAC_BASE_URL`
3. A warning banner appears in the Accounts page when auth has failed — click **Re-authorize**

---

## Push notifications

Comstac supports Web Push (VAPID) for new-mail notifications on Android and desktop Chrome.

```bash
# Generate a VAPID key pair
./comstac vapid
# Add the printed COMSTAC_VAPID_PUBLIC and COMSTAC_VAPID_PRIVATE to your env file
# Also set COMSTAC_VAPID_SUBJECT=mailto:you@example.com
```

Once configured, a subscription toggle appears on the Accounts page in the UI.

---

## Backup

The bundled `comstac backup` command is a legacy local-snapshot helper, not a
complete production recovery design. It does not provide encryption or a
verified remote-host identity boundary. The Makefile therefore refuses to
install its former timer or invoke it against a live binary.

For production, use an independently reviewed backup service that takes a
consistent SQLite snapshot, encrypts before persistence or transfer, pins the
remote identity, has bounded retention and is restore-tested in isolation.
Never restore over a running database; make restoration a separate stopped-
service transaction with rollback.

---

## Configuration

All configuration is via environment variables. Required settings have no
server-startup fallback: Comstac refuses to start when they are absent or
unsafe.

### Core

| Variable | Default | Description |
|---|---|---|
| `COMSTAC_SMTP_ADDR` | Required | SMTP bind address (use `:25` with the narrow `CAP_NET_BIND_SERVICE`, not a root process) |
| `COMSTAC_SMTP_DOMAIN` | Required | Explicit non-localhost SMTP banner hostname |
| `COMSTAC_HTTP_ADDR` | Required | Loopback-only HTTP/UI/API bind address |
| `COMSTAC_DB_PATH` | Required | Absolute SQLite database path |
| `COMSTAC_LOCAL_DOMAINS` | Required | Comma-separated accepted domains, e.g. `example.com` |
| `COMSTAC_LOCAL_RECIPIENTS` | Required | Comma-separated accepted recipients belonging to the accepted domains |

### Auth

| Variable | Default | Description |
|---|---|---|
| `COMSTAC_ADMIN_USERNAME` | Required | Bootstrap admin username |
| `COMSTAC_ADMIN_PASSWORD` | Required | Bootstrap admin password, at least 12 characters; the example default is rejected |
| `COMSTAC_SESSION_TTL_HOURS` | `24` | Session lifetime |
| `COMSTAC_CSRF_SECRET` | Required | Independent HMAC key of at least 32 characters (`openssl rand -hex 32`) |

### Outbound relay

| Variable | Default | Description |
|---|---|---|
| `COMSTAC_RELAY_HOST` | — | Smart-host hostname. Outbound disabled if not set |
| `COMSTAC_RELAY_PORT` | `587` | Smart-host port |
| `COMSTAC_RELAY_USERNAME` | — | SMTP auth username |
| `COMSTAC_RELAY_PASSWORD` | — | SMTP auth password |
| `COMSTAC_RELAY_FROM` | — | Approved sender address |

### Gmail IMAP

| Variable | Default | Description |
|---|---|---|
| `COMSTAC_IMAP_ADDR` | `imap.gmail.com:993` | IMAP server |
| `COMSTAC_IMAP_USERNAME` | — | Gmail address |
| `COMSTAC_IMAP_POLL_INTERVAL` | `60s` | Poll interval (Go duration) |
| `COMSTAC_IMAP_CLIENT_ID` | — | OAuth2 client ID (Desktop app credential) |
| `COMSTAC_IMAP_CLIENT_SECRET` | — | OAuth2 client secret |
| `COMSTAC_IMAP_REFRESH_TOKEN` | — | Refresh token from `comstac authorize` |

| `COMSTAC_AGENT_TOKEN` | — | Strong token for agent SSE auth (`openssl rand -hex 32`). Enables `GET /api/push/sse` for OpenClaw agent integration |

### Web Push (optional)

| Variable | Description |
|---|---|
| `COMSTAC_VAPID_PUBLIC` | VAPID public key from `comstac vapid` |
| `COMSTAC_VAPID_PRIVATE` | VAPID private key from `comstac vapid` |
| `COMSTAC_VAPID_SUBJECT` | `mailto:` or `https://` contact address |

### Web re-auth (optional)

| Variable | Description |
|---|---|
| `COMSTAC_BASE_URL` | Public HTTPS root, e.g. `https://mail.example.com` |
| `COMSTAC_OAUTH_CLIENT_ID` | Web Application OAuth2 client ID |
| `COMSTAC_OAUTH_CLIENT_SECRET` | Web Application OAuth2 client secret |

### Backup (optional)

| Variable | Default | Description |
|---|---|---|
| `COMSTAC_BACKUP_DIR` | `/var/lib/comstac/backups` | Local snapshot directory |
| `COMSTAC_BACKUP_DEST` | — | Legacy SCP destination; not recommended for production |
| `COMSTAC_BACKUP_KEY` | — | Legacy SSH private key path; not recommended for production |
| `COMSTAC_BACKUP_RETAIN` | `7` | Number of local snapshots to keep |

---

## Development

```bash
# Run locally (reads env vars from shell)
source comstac.env && go run ./cmd/comstac

# Or via make (reads from shell env)
make run

# Full test suite
make test

# Focused tests
make test-api
make test-smtp
make test-store

# Format
make fmt
```

The binary embeds all frontend assets (templates, static files) at build time — there is no separate frontend build step.

---

## Security notes

- Passwords are stored with bcrypt
- Session cookies are `HttpOnly`, `Secure`, `SameSite=Lax`
- All state-mutating requests require an HMAC-SHA256 CSRF token derived per session
- Inbound HTML email renders in a sandboxed `<iframe>` — scripts, forms, and same-origin access blocked; only `allow-popups` and `allow-popups-to-escape-sandbox` are permitted so links open in a new tab
- SPF/DKIM/DMARC results are persisted and displayed as auth badges per message
- Security response headers (`X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Permissions-Policy`) are set on all responses
- Apply the same nginx rate limit to exact locations `/login` and `/api/login`; Comstac also shares an in-process limiter across both routes
