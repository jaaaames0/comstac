# CONFIG.md

Runtime configuration is environment-variable driven.

## Core Runtime
- `COMSTAC_SMTP_ADDR` (default `:2525`): SMTP bind address.
- `COMSTAC_SMTP_DOMAIN` (default `localhost`): SMTP banner hostname, announced in EHLO greeting. Set to your server's FQDN (e.g. `mail.example.com`).
- `COMSTAC_HTTP_ADDR` (default `:8080`): HTTP/UI/API bind address.
- `COMSTAC_DB_PATH` (default `./comstac.db`): SQLite database path.

## Auth
- `COMSTAC_ADMIN_USERNAME` (default `admin`): bootstrap admin username.
- `COMSTAC_ADMIN_PASSWORD` (default `changeme123`): bootstrap admin password.
- `COMSTAC_SESSION_TTL_HOURS` (default `24`): session lifetime in hours.
- `COMSTAC_CSRF_SECRET` (default: derived from admin password): HMAC key for CSRF token generation. Set an explicit secret in production for stability across restarts.

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
- `COMSTAC_BACKUP_DIR` (default `/var/lib/comstac/backups`): local directory for snapshot files.
- `COMSTAC_BACKUP_DEST` (default empty): remote SCP destination, e.g. `user@host:/path/to/backups/`. If unset, only local snapshots are kept.
- `COMSTAC_BACKUP_KEY` (default empty): path to SSH private key for remote transfer. If unset, SSH uses its default key (`~/.ssh/id_rsa`).
- `COMSTAC_BACKUP_RETAIN` (default `7`): number of local snapshots to keep before pruning oldest.

### Running a backup
```bash
# Manual on-demand
sudo make backup          # or: sudo comstac backup

# Install daily 02:00 systemd timer
sudo make install-timer

# Check timer status
systemctl list-timers comstac-backup.timer
```

### Restore
```bash
# Stop the service, replace the database file, restart
sudo systemctl stop comstac
sudo cp /var/lib/comstac/backups/comstac_20260411_020000.sqlite /var/lib/comstac/comstac.db
sudo systemctl start comstac
```

## SMTP Recipient Policy
- `COMSTAC_LOCAL_DOMAINS` (default empty): comma-separated accepted local domains.
  - Example: `example.com,mail.example.com`
- `COMSTAC_LOCAL_RECIPIENTS` (default empty): comma-separated accepted full recipient addresses.
  - Example: `local@example.com,alerts@example.com`

## Notes
- `COMSTAC_LOCAL_RECIPIENTS` also bootstraps local accounts used for recipient routing.
- Values are normalized to lowercase where relevant.
- Empty/invalid CSV entries are ignored.

## Example
```bash
export COMSTAC_SMTP_ADDR=":2525"
export COMSTAC_HTTP_ADDR=":8080"
export COMSTAC_DB_PATH="./comstac-dev.db"
export COMSTAC_ADMIN_USERNAME="admin"
export COMSTAC_ADMIN_PASSWORD="amsterdam"
export COMSTAC_SESSION_TTL_HOURS="24"
export COMSTAC_LOCAL_DOMAINS="example.com"
export COMSTAC_LOCAL_RECIPIENTS="local@example.com"
```
