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
- `COMSTAC_SMTP_TLS_CERT` (required): absolute path to the dedicated inbound SMTP full certificate chain.
- `COMSTAC_SMTP_TLS_KEY` (required): absolute path to the matching dedicated inbound SMTP private key. Do not point Comstac at a shared web-server private key.
- `COMSTAC_ACME_CHALLENGE_DIR` (required): absolute path to the root-managed HTTP-01 token directory. Comstac needs read/traverse access but must not be able to write it.
- `COMSTAC_HTTP_ADDR` (required): loopback-only HTTP/UI/API bind address, for example `127.0.0.1:8080`. Put a TLS reverse proxy in front of it for remote access.
- `COMSTAC_DB_PATH` (required): absolute SQLite database path.
- `COMSTAC_STORAGE_MAX_BYTES` (required): hard ceiling for the SQLite database and total-state ingestion checks, in bytes. A 10 GiB ceiling is `10737418240`.
- `COMSTAC_STORAGE_MIN_FREE_BYTES` (required): filesystem space reserved for other services; ingestion is temporarily rejected before crossing it. A 50 GiB reserve is `53687091200`.
- `COMSTAC_STORAGE_WARN_FREE_BYTES` (required): higher free-space warning watermark, in bytes. It must exceed the hard reserve. A 75 GiB warning level is `80530636800`.

The storage guard counts regular files beneath the database directory, checks
filesystem availability before SMTP and IMAP persistence, and applies SQLite's
page ceiling to every pooled database connection. `/healthz` returns `503` at
either the free-space warning level or 80% of the state ceiling, allowing the
existing monitor to alert before ingestion begins returning temporary SMTP
`452` failures. Authenticated `/metrics` exposes byte counts, watermarks and
the rejection counter without exposing paths or configuration secrets. The
database pool is bounded to eight open connections and four idle connections;
each connection has a five-second SQLite busy timeout.

The inbound listener advertises opportunistic STARTTLS and accepts TLS 1.2 or
newer. `comstac check-config` verifies the pair is readable, currently valid
and covers `COMSTAC_SMTP_DOMAIN` without printing either path. Plaintext MX
delivery remains available for senders that do not negotiate STARTTLS;
`REQUIRETLS` is not advertised. After atomically installing a renewed pair,
send `SIGHUP` to validate and activate it for new handshakes without restarting
the service. A failed reload leaves the last good in-memory certificate active.
For webroot renewal, the public challenge endpoint serves only regular files
whose names contain ASCII letters, digits, `_` or `-`, with a 4 KiB maximum.
It rejects subdirectories, traversal, symlinks and other methods. Keep the
configured directory root-managed and read-only to the service.

## Auth
- `COMSTAC_ADMIN_USERNAME` (required): bootstrap admin username.
- `COMSTAC_ADMIN_PASSWORD` (required): bootstrap admin password, at least 12 characters. The example default is rejected.
- `COMSTAC_SESSION_TTL_HOURS` (default `24`): session lifetime in hours.
- `COMSTAC_CSRF_SECRET` (required): independent HMAC key for CSRF token generation, at least 32 characters.

To rotate an existing password, run the deployed binary as the database owner
from an interactive terminal:

```bash
sudo -u comstac /path/to/comstac rotate-password --database /var/lib/comstac/comstac.db --username operator
```

The new password is read twice without echo and is never accepted in an
argument or environment variable. A successful update atomically revokes every
session for that user. The bootstrap environment password does not overwrite an
existing user's rotated database credential.

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
Comstac has no built-in backup command, backup environment variables or timer.
Use an independently reviewed service that creates a consistent SQLite
snapshot, encrypts before storage or transfer, pins remote identity and has a
proven isolated restore. Keeping this outside Comstac avoids granting the mail
process remote backup credentials or general command-execution responsibilities.

Never restore over a running database. Validate a decrypted snapshot in
disposable storage, then use a separate stopped-service restoration procedure
with a current checkpoint and rollback.

## SMTP Recipient Policy
- `COMSTAC_LOCAL_DOMAINS` (required): comma-separated accepted local domains.
  - Example: `example.com,mail.example.com`
- `COMSTAC_LOCAL_RECIPIENTS` (required): comma-separated accepted full recipient addresses. Every address must belong to an accepted local domain.
  - Example: `local@example.com,alerts@example.com`

## Agent Integration (ghost-mail SSE)
- `COMSTAC_AGENT_TOKEN`: Optional token of at least 32 characters for
  authenticating `GET /api/push/sse` through the `X-Agent-Token` header. If
  unset, the endpoint is not registered.

The former ghost-mail consumer is no longer used. In the current implementation
new-mail SSE dispatch shares the Web Push notifier, so setting the token alone
registers the stream but does not deliver new-mail events unless the complete
VAPID configuration is also enabled. Treat this as a dormant integration
surface unless a new consumer and HTTPS route are deliberately reviewed.

## Notes
- `COMSTAC_LOCAL_RECIPIENTS` also bootstraps local accounts used for recipient routing.
- Values are normalized to lowercase where relevant.
- Empty CSV entries are ignored; non-empty invalid domains or addresses make
  startup and `check-config` fail.
- Relay, IMAP OAuth, browser OAuth and VAPID settings are validated as complete groups when enabled.
- Comstac enforces bounded HTTP bodies, a shared limiter for `/login` and `/api/login`, at most 32 concurrent SMTP connections, at most four concurrent SMTP DATA handlers, 25 MiB per message, 100 recipients per transaction, and a bounded notification queue.
- Storage limits are integer byte counts rather than percentages so behavior is explicit and reproducible. Reassess the watermarks if Comstac state moves to another filesystem or the host's storage allocation changes.

## Example
```bash
export COMSTAC_SMTP_ADDR=":2525"
export COMSTAC_SMTP_DOMAIN="mail.example.com"
export COMSTAC_SMTP_TLS_CERT="/etc/comstac/tls/fullchain.pem"
export COMSTAC_SMTP_TLS_KEY="/etc/comstac/tls/privkey.pem"
export COMSTAC_ACME_CHALLENGE_DIR="/etc/comstac/acme-webroot/.well-known/acme-challenge"
export COMSTAC_HTTP_ADDR="127.0.0.1:8080"
export COMSTAC_DB_PATH="/var/lib/comstac/comstac.db"
export COMSTAC_ADMIN_USERNAME="operator"
export COMSTAC_ADMIN_PASSWORD="replace-with-a-long-random-password"
export COMSTAC_CSRF_SECRET="replace-with-at-least-32-random-characters"
export COMSTAC_STORAGE_MAX_BYTES="10737418240"
export COMSTAC_STORAGE_MIN_FREE_BYTES="53687091200"
export COMSTAC_STORAGE_WARN_FREE_BYTES="80530636800"
export COMSTAC_SESSION_TTL_HOURS="24"
export COMSTAC_LOCAL_DOMAINS="example.com"
export COMSTAC_LOCAL_RECIPIENTS="local@example.com"
```
