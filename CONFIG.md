# CONFIG.md

Runtime configuration is environment-variable driven.

The long-running server fails closed unless every required setting below is
explicitly supplied. Startup errors name missing or invalid variables but never
print their values.

Validate a prepared environment without opening the database or starting a
listener with `comstac check-config`. A successful check prints only
`configuration=valid`.

## Core Runtime
- `COMSTAC_SMTP_ADDR` (required): SMTP bind address, for example `:2525`.
- `COMSTAC_SMTP_DOMAIN` (required): explicit non-`localhost` SMTP banner hostname, announced in EHLO greeting (for example `mail.example.com`).
- `COMSTAC_HTTP_ADDR` (required): loopback-only HTTP/UI/API bind address, for example `127.0.0.1:8080`. Put a TLS reverse proxy in front of it for remote access.
- `COMSTAC_DB_PATH` (required): absolute SQLite database path.

## Auth
- `COMSTAC_ADMIN_USERNAME` (required): bootstrap admin username.
- `COMSTAC_ADMIN_PASSWORD` (required): bootstrap admin password, at least 12 characters. The example default is rejected.
- `COMSTAC_SESSION_TTL_HOURS` (default `24`): session lifetime in hours.
- `COMSTAC_CSRF_SECRET` (required): independent HMAC key for CSRF token generation, at least 32 characters.

## Outbound SMTP Relay
- `COMSTAC_RELAY_HOST` (default empty): Smart-host SMTP server hostname. Outbound sending is disabled if not set.
- `COMSTAC_RELAY_PORT` (default `587`): Smart-host SMTP port.
- `COMSTAC_RELAY_USERNAME`: SMTP auth username for the relay.
- `COMSTAC_RELAY_PASSWORD`: SMTP auth password for the relay.
- `COMSTAC_RELAY_FROM`: Approved sender address (e.g. `you@example.com`). Used as the `From` header and SMTP envelope sender.

## Gmail IMAP OAuth2
- `COMSTAC_IMAP_ADDR` (default `imap.gmail.com:993`): IMAP server address.
- `COMSTAC_IMAP_USERNAME`: Gmail address to fetch from.
- `COMSTAC_IMAP_MAILBOX` (default `INBOX`): Mailbox to poll.
- `COMSTAC_IMAP_POLL_INTERVAL` (default `60s`): How often to poll. Uses Go duration syntax.
- `COMSTAC_IMAP_CLIENT_ID`: Google OAuth2 client ID.
- `COMSTAC_IMAP_CLIENT_SECRET`: Google OAuth2 client secret.
- `COMSTAC_IMAP_REFRESH_TOKEN`: OAuth2 refresh token. Run `comstac authorize` to obtain.

## Web Push Notifications
Generate VAPID keys with `comstac vapid` and add the output to your environment.
- `COMSTAC_VAPID_PUBLIC`: VAPID public key (from `comstac vapid`).
- `COMSTAC_VAPID_PRIVATE`: VAPID private key (from `comstac vapid`).
- `COMSTAC_VAPID_SUBJECT`: Contact address for VAPID, e.g. `mailto:you@example.com`. Must be a `mailto:` or `https://` URL under your control.

## Gmail OAuth2 Re-authorization (Web)
If IMAP fetch breaks with `invalid_grant`, you can re-authorize from the web UI (`/ui/accounts`) instead of the CLI. Requires a separate "Web Application" OAuth credential set:
- `COMSTAC_BASE_URL`: Public HTTPS root, e.g. `https://mail.example.com`.
- `COMSTAC_OAUTH_CLIENT_ID`: OAuth2 client ID (Web Application type).
- `COMSTAC_OAUTH_CLIENT_SECRET`: OAuth2 client secret (Web Application type).

The callback URI to register in Google Cloud Console is: `{COMSTAC_BASE_URL}/ui/oauth/callback`

## Setup
Run `comstac authorize` (with CLIENT_ID and CLIENT_SECRET set) to complete the one-time OAuth2 flow and obtain a refresh token.

## Backup
The bundled backup command is retained only as a legacy local-snapshot helper.
It is not a production backup design and its former Makefile timer installation
is disabled. Prefer an independently reviewed service that creates a consistent
SQLite snapshot, encrypts before storage or transfer, pins remote identity and
has a proven isolated restore.

- `COMSTAC_BACKUP_DIR` (default `/var/lib/comstac/backups`): local directory for snapshot files.
- `COMSTAC_BACKUP_DEST` (legacy; not recommended): remote SCP destination.
- `COMSTAC_BACKUP_KEY` (legacy; not recommended): path to an SSH private key.
- `COMSTAC_BACKUP_RETAIN` (default `7`): number of local snapshots to keep before pruning oldest.

Never restore over a running database. Validate a decrypted snapshot in
disposable storage, then use a separate stopped-service restoration procedure
with a current checkpoint and rollback.

## SMTP Recipient Policy
- `COMSTAC_LOCAL_DOMAINS` (required): comma-separated accepted local domains.
  - Example: `example.com,mail.example.com`
- `COMSTAC_LOCAL_RECIPIENTS` (required): comma-separated accepted full recipient addresses. Every address must belong to an accepted local domain.
  - Example: `local@example.com,alerts@example.com`

## Agent Integration (ghost-mail SSE)
- `COMSTAC_AGENT_TOKEN`: Strong token for authenticating the OpenClaw agent to `GET /api/push/sse`. Generate with `openssl rand -hex 32`. If unset, the SSE endpoint is not registered (silently skipped). Token is sent as the `X-Agent-Token` header by `ghost-sse-client.py`.

## Notes
- `COMSTAC_LOCAL_RECIPIENTS` also bootstraps local accounts used for recipient routing.
- Values are normalized to lowercase where relevant.
- Empty/invalid CSV entries are ignored.
- Relay, IMAP OAuth, browser OAuth and VAPID settings are validated as complete groups when enabled.
- Comstac enforces bounded HTTP bodies, a shared limiter for `/login` and `/api/login`, at most 32 concurrent SMTP connections, at most four concurrent SMTP DATA handlers, 25 MiB per message, 100 recipients per transaction, and a bounded notification queue.

## Example
```bash
export COMSTAC_SMTP_ADDR=":2525"
export COMSTAC_SMTP_DOMAIN="mail.example.com"
export COMSTAC_HTTP_ADDR="127.0.0.1:8080"
export COMSTAC_DB_PATH="/var/lib/comstac/comstac.db"
export COMSTAC_ADMIN_USERNAME="operator"
export COMSTAC_ADMIN_PASSWORD="replace-with-a-long-random-password"
export COMSTAC_CSRF_SECRET="replace-with-at-least-32-random-characters"
export COMSTAC_SESSION_TTL_HOURS="24"
export COMSTAC_LOCAL_DOMAINS="example.com"
export COMSTAC_LOCAL_RECIPIENTS="local@example.com"
```
