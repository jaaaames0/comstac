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
go build -o comstac ./cmd/comstac
```

Or install directly to `/usr/local/bin`:

```bash
sudo make install   # builds, copies env file, installs and starts systemd service
```

### 2. Configure

Copy the example env file and fill in your values:

```bash
cp .env.example comstac.env
$EDITOR comstac.env
```

At minimum you need:

```bash
COMSTAC_SMTP_DOMAIN=mail.example.com
COMSTAC_LOCAL_DOMAINS=example.com
COMSTAC_LOCAL_RECIPIENTS=you@example.com
COMSTAC_ADMIN_USERNAME=you
COMSTAC_ADMIN_PASSWORD=a-strong-password
COMSTAC_CSRF_SECRET=$(openssl rand -hex 32)
```

See [Configuration](#configuration) below for all options.

### 3. Run

```bash
source comstac.env && ./comstac
# or for development:
make run
```

The web UI is available at `http://localhost:8080` (or whatever `COMSTAC_HTTP_ADDR` is set to). Log in with your configured admin credentials.

### 4. nginx + TLS (production)

Point nginx at `COMSTAC_HTTP_ADDR` (default `127.0.0.1:8080`) and terminate TLS with Let's Encrypt. A minimal server block:

```nginx
server {
    listen 443 ssl;
    server_name mail.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }

    # Rate-limit login attempts
    location /login {
        limit_req zone=login burst=3 nodelay;
        proxy_pass http://127.0.0.1:8080;
    }
}
```

### 5. Systemd service

A `comstac.service` unit file is included. Install it with:

```bash
sudo make install
```

This builds the binary, copies your env file to `/etc/comstac/comstac.env` (mode 600), installs the service unit, and starts it. To check status:

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

```bash
# Manual snapshot (creates a hot SQLite copy in COMSTAC_BACKUP_DIR)
sudo comstac backup

# Install a daily 02:00 systemd timer
sudo make install-timer

# Check next scheduled run
systemctl list-timers comstac-backup.timer
```

Set `COMSTAC_BACKUP_DEST=user@host:/path/` to also SCP snapshots to a remote host after each run. `COMSTAC_BACKUP_RETAIN` (default `7`) controls how many local snapshots are kept.

**Restore:**

```bash
sudo systemctl stop comstac
sudo cp /var/lib/comstac/backups/comstac_20260411_020000.sqlite /var/lib/comstac/comstac.db
sudo systemctl start comstac
```

---

## Configuration

All configuration is via environment variables. Copy `.env.example` as a starting point.

### Core

| Variable | Default | Description |
|---|---|---|
| `COMSTAC_SMTP_ADDR` | `:2525` | SMTP bind address (use `:25` in production as root or with `CAP_NET_BIND_SERVICE`) |
| `COMSTAC_SMTP_DOMAIN` | `localhost` | SMTP banner hostname (your server's FQDN) |
| `COMSTAC_HTTP_ADDR` | `:8080` | HTTP/UI/API bind address |
| `COMSTAC_DB_PATH` | `./comstac.db` | SQLite database path |
| `COMSTAC_LOCAL_DOMAINS` | — | Comma-separated accepted domains, e.g. `example.com` |
| `COMSTAC_LOCAL_RECIPIENTS` | — | Comma-separated accepted recipients, e.g. `you@example.com` |

### Auth

| Variable | Default | Description |
|---|---|---|
| `COMSTAC_ADMIN_USERNAME` | `admin` | Bootstrap admin username |
| `COMSTAC_ADMIN_PASSWORD` | `changeme123` | Bootstrap admin password — **change this** |
| `COMSTAC_SESSION_TTL_HOURS` | `24` | Session lifetime |
| `COMSTAC_CSRF_SECRET` | — | HMAC key for CSRF tokens — set explicitly in production (`openssl rand -hex 32`) |

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
| `COMSTAC_BACKUP_DEST` | — | Remote SCP destination, e.g. `user@host:/path/` |
| `COMSTAC_BACKUP_KEY` | — | SSH private key for remote transfer |
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
- nginx rate-limiting on `/login` is recommended in production
