# CHANGELOG.md

All notable changes to this project will be documented in this file.

This project adheres to Semantic Versioning.

## [Unreleased]

### Removed
- **Web-based Gmail re-authorization** (`/ui/oauth/start`, `/ui/oauth/callback`)
  and its `COMSTAC_BASE_URL`, `COMSTAC_OAUTH_CLIENT_ID` and
  `COMSTAC_OAUTH_CLIENT_SECRET` settings, which are now ignored. The flow stored
  a refresh token issued to the Web Application client, but the IMAP fetcher
  always refreshes with the Desktop app client, so a completed web
  re-authorization could not have kept IMAP working. Recovery is
  `comstac authorize`. The IMAP auth-failure banner remains and is now shown
  whenever Gmail IMAP is configured rather than only when web OAuth was.

### Added
- **Optional agent SSE endpoint**: `GET /api/push/sse` uses the
  `X-Agent-Token` header, emits 30-second keepalives and supports up to four
  simultaneous clients. It is absent when `COMSTAC_AGENT_TOKEN` is unset. The
  former ghost-mail consumer was abandoned and the endpoint is dormant in the
  current production workflow. New-mail dispatch presently shares the Web Push
  notifier and therefore also requires the complete VAPID configuration.
- **Configuration preflight**: `comstac check-config` validates server settings without opening the database or starting listeners and prints no values.
- **Remote-image privacy preferences**: the operator can add or remove normalized
  exact mailbox addresses whose messages may load HTTPS images automatically.
  Preferences are stored in SQLite rather than configuration, and a compact
  per-message `load images` action remains available for every other sender.
- **Password rotation command**: `comstac rotate-password` reads a confirmed
  password from an interactive terminal, atomically replaces the selected
  user's bcrypt hash and revokes all of that user's sessions.
- **Sanitized security counters**: authenticated `/metrics` includes fixed,
  process-local totals for login failures, rate-limit rejections, temporary
  SMTP rejections, SMTP saturation and dropped notifications, without dynamic
  labels or message/user data.

### Security
- Inbound SMTP now advertises opportunistic STARTTLS using a required dedicated
  certificate/key pair. Startup and `check-config` fail closed on unreadable,
  invalid, expired, not-yet-valid or hostname-mismatched material; TLS 1.2 is
  the minimum. Plaintext MX delivery remains available and `REQUIRETLS` is not
  advertised. `SIGHUP` validates and atomically activates renewed material for
  new handshakes; a failed reload leaves the last good certificate active.
- A bounded public HTTP-01 handler serves only strictly named regular token
  files from a required root-managed read-only directory. This permits
  dedicated SMTP-certificate webroot renewal without exposing a shared nginx
  key or granting Comstac challenge-file write access. Traversal-like paths
  are rejected before HTTP mux canonicalization can redirect them.
- HTMX 1.9.12 is now pinned inside the embedded static filesystem and served
  with its independently verified Subresource Integrity digest; production no
  longer executes JavaScript from a CDN. HTMX evaluation and swapped-script
  execution are disabled.
- All responses now carry a restrictive Content Security Policy with a fresh
  per-response nonce for the fixed application script. Login and authenticated
  responses use `Cache-Control: no-store`, and referrers are suppressed.
- Sandboxed HTML-email documents receive their own deny-by-default CSP. Remote
  images are blocked unless explicitly loaded for that one message, while
  scripts, connections, forms, frames, objects, fonts and media remain blocked.
  Sender-supplied meta refresh navigation is removed.
- Automatic remote-image preferences match the parsed mailbox, never a display
  name, and do not expand the email iframe beyond HTTPS images. They are
  deliberately presented as privacy preferences rather than authentication:
  `From` is spoofable and the currently recorded DKIM/SPF data plus DMARC policy
  lookup do not establish aligned DMARC passage.
- Session-cookie deletion now retains the same `Secure`, `HttpOnly`, path and
  `SameSite=Lax` boundary as creation. Dynamic error fragments use
  `html/template`, and OAuth failures no longer echo provider/internal errors.
- Browser-facing error messages that can originate on variable failure paths
  now use a shared `html/template` renderer; account creation no longer reflects
  internal SQLite errors to the response.
- Explicit storage watermarks now protect shared-host capacity: every connection in the bounded SQLite pool receives a hard page ceiling and busy timeout, SMTP and IMAP ingestion check total Comstac state plus reserved free space, SMTP returns a retryable `452` when capacity is unavailable, `/healthz` warns before rejection, and authenticated metrics expose only non-secret capacity counters.
- Server startup now fails closed unless listener addresses, absolute database path, admin credentials, independent CSRF secret, SMTP hostname and local recipient allowlists are explicitly configured and valid. HTTP must bind to loopback; partially configured relay, IMAP OAuth, browser OAuth and VAPID groups are rejected.
- `/login` and `/api/login` share an in-process failed-attempt limiter with sanitized event logging, `429 Too Many Requests` and `Retry-After`; the API route can no longer bypass protection intended only for the HTML route.
- HTTP header/read/write/idle deadlines, a 1 MiB normal mutation-body cap, a 64 KiB login-body cap and a 26 MiB compose/reply cap bound slow or oversized requests.
- SMTP is limited to 32 concurrent connections and four concurrent DATA handlers. Saturation receives temporary `421`/`451` responses rather than creating unbounded work; existing message-size, recipient and socket-time limits remain enforced.
- Per-message notification goroutines were replaced by a 128-item queue with two fixed workers and per-delivery deadlines. Saturation drops only the notification, not already persisted mail.
- VAPID key mismatch is now a startup error. Push delivery respects cancellation and no longer logs partial subscription endpoint URLs.
- Agent SSE routing now reaches its token-authenticated handler when enabled, is capped at four concurrent clients, removes clients on disconnect, no longer depends on the deprecated unsafe `CloseNotifier` assertion, and explicitly opts out of the normal finite HTTP write deadline while retaining request-context cancellation.
- Legacy `make install`, timer-install and broad uninstall targets now refuse destructive in-place operations; documented deployment uses clean, root-owned immutable versioned releases and rollback.

### Changed
- `go mod tidy` now correctly records packages imported directly by Comstac as direct dependencies.
- The module minimum is Go 1.26. `x/net`, `x/crypto`, `x/sys` and the indirect
  JWT dependency were advanced to their mutually compatible fixed releases;
  `govulncheck` reports zero reachable and zero imported-package
  vulnerabilities.
- Public documentation now describes the implemented single-mailbox Gmail
  integration, local-only IMAP state actions, opportunistic inbound STARTTLS,
  external backup boundary, current commands and remaining `1.0` gates. The
  starter environment now includes every required server variable.

### Removed
- The superseded `comstac backup` command, its SCP/private-key configuration
  surface and its Makefile targets. Production recovery is owned by an
  independent encrypted, retention-bounded and restore-tested backup service.

## [0.9.3] - 2026-04-17
### Security
- **`.gitignore` hardened**: replaced stale `sovereign.*` entries with full coverage — `*.env`, `client_secret_*.json`, `*.db*`, `cookies.txt`, compiled binaries, systemd unit files, `AGENDA.md`. No secrets or email data can be accidentally committed.
- **`COMSTAC_CSRF_SECRET`** added to production env file; app no longer falls back to hardcoded default key.
- **`/metrics` moved behind auth**: was publicly accessible; now requires a valid session cookie like all other protected routes.
- **Security headers middleware** added to all responses: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: strict-origin-when-cross-origin`, `Permissions-Policy: camera=(), microphone=(), geolocation=()`.
- **MIME header injection fixed** (`relay.go`): `buildRawMIME` now strips `\r`/`\n` from all header values via `sanitizeHeaderValue`. A crafted Subject or To containing CRLF could have injected extra headers into outbound mail.
- **Attachment Content-Type clamped** (`handler.go`): `/ui/attachment` now passes the MIME part's Content-Type through a known-safe allow-list, falling back to `application/octet-stream`. Prevents a malicious email from causing the browser to treat a downloaded attachment as HTML.
- **Attachment filename sanitized** (`handler.go`): `sanitizeFilename` strips `"`, `\`, CRLF, and control characters from the `Content-Disposition` filename parameter.
- **SMTP connection timeouts** (`smtpserver`): `ReadTimeout` and `WriteTimeout` set to 5 minutes (was 0 — no timeout). Limits connection exhaustion from slow or hanging senders.

### Added
- **Attachment support in compose and reply**: file picker accepts multiple files; handler parses `multipart/form-data` and attaches each file as a base64-encoded MIME part. `relay.OutboundMessage` carries `[]Attachment`; `buildRawMIME` branches on `multipart/mixed` vs plain `text/plain`.
- **Reply-all**: `GET /ui/reply?id=X&all=1` pre-fills CC from the original message's `Cc:` and additional `To:` addresses. Shown conditionally in the reply dropdown when there are other recipients.
- **Forward**: `GET /ui/forward?id=X` opens compose pre-filled with `Fwd:` subject and quoted body. `relay.ForwardSubject()` helper applies the prefix idempotently.
- **Reply / reply-all / forward dropdown**: three actions consolidated into a single dropdown in the message detail action bar, replacing three separate buttons.
- **Snooze dropdown**: 1h / 24h / 3d / 7d options in a dropdown menu; "unsnooze" single-button shown when already snoozed.
- **Keyboard shortcuts**: `j`/`k` next/prev message, `r` reply, `e` trash, `u` mark unread, `c` compose, `Escape` close detail. Not active when focus is inside a text field.
- **Reading pane scroll-to-top**: pane resets scroll position on every content change (message switch, compose, accounts).
- **Auto-focus compose/reply body**: first `<textarea>` in the reading pane is focused automatically after load, putting the cursor at position 0.
- **Hover timestamps**: list row timestamps show the full `Mon, 2 Jan 2006 15:04:05` date as a native `title` tooltip.
- **Targeted push notification opens**: clicking a push notification now POSTs `action=read` for the specific message ID (marks read + loads detail), rather than just reloading the home page.

### Fixed
- **IMAP OAuth empty access token**: `refreshAccessToken` now returns an error if the token endpoint responds with an empty `access_token`, preventing silent IMAP auth failures.
- **Links in HTML emails open in new tab**: `<base target="_blank">` injected into iframe HTML body; sandbox updated to `allow-popups allow-popups-to-escape-sandbox`. Previously `sandbox=""` blocked all navigation silently.
- **Mobile dropdown visibility**: `.dropdown-menu` switched from `position: absolute` to `position: fixed` to escape the `overflow-x: auto` container (CSS forces `overflow-y: auto` too, clipping absolutely-positioned children).
- **Mobile dropdown position offset**: `transformAncestorOffset()` walks the DOM for a CSS transform ancestor (`.reader-col` has `translateX(0)` on mobile) and subtracts its viewport offset from `getBoundingClientRect()` coords, correcting ~50px vertical drift.

### Changed
- **`relay.OutboundMessage.To`** changed from `string` to `[]string` to support multi-recipient compose.
- **`store.MessageDetail.CcAddr`** added; populated from the `Cc:` header of the raw MIME during `GetMessageDetail`.
- **`ingest.MailNotifier.SendNewMail`** signature extended with `messageID int64` for targeted notification payloads.
- **CC/BCC inlined**: dedicated `cc_bcc.html` partial template removed; fields are now a `<details>` toggle directly inside `compose.html` and `reply.html`.
- **`parseTimestamp` helper** extracted from duplicated parsing loops in `fmtTime` and `fmtFullDate`.

## [0.9.2] - 2026-04-13 (production-validated)
### Added
- **Web Push notifications**: `POST /ui/push/subscribe` and `POST /ui/push/unsubscribe` endpoints save/remove Web Push subscriptions from the `push_subscriptions` table. Push delivered via `webpush-go` (VAPID). Notification on new mail (SMTP or IMAP ingest) fires to all registered subscriptions. Expired subscriptions (HTTP 410 Gone) are pruned on next send.
- **Service worker** (`/sw.js`): static asset serving from `StaticFS`; handles `push` event and shows a `Notification`. `notificationclick` focuses or opens the app window.
- **`comstac vapid` subcommand**: generates and prints a VAPID key pair (`COMSTAC_VAPID_PUBLIC`, `COMSTAC_VAPID_PRIVATE`) using `webpush.GenerateVAPIDKeys()`.
- **`push_subscriptions` table** (migration `0012_push_subscriptions.sql`): stores `endpoint`, `p256dh`, `auth` per subscriber. `ON CONFLICT DO UPDATE` for subscription renewal.
- **`internal/push` package**: `Notifier` wraps `webpush.Subscription` for each stored endpoint; fires in a goroutine after ingest commit so it does not block the request path.
- **`ingest.Service.SetNotifier`**: attaches the push notifier to the shared ingest pipeline. Notifier is called asynchronously via `go s.notifier.SendNewMail(...)` after each successful ingest.
- **Push subscription toggle on accounts page**: shown when `VAPIDPublicKey` is configured. Uses `navigator.serviceWorker.register('/sw.js')` + `pushManager.subscribe()` with the VAPID public key, then POSTs the subscription JSON to `/ui/push/subscribe`. Disable calls `/ui/push/unsubscribe` and `sub.unsubscribe()`.
- **`/sw.js` and `/manifest.json` served publicly**: added to `publicMux` and `isPublicPath` in `api.Server.Handler()`.
- **PWA meta tags** in `index.html`: `theme-color`, `apple-mobile-web-app-capable`, `apple-mobile-web-app-status-bar-style`, and `<link rel="manifest">` for iOS/Android home-screen add.
- **`manifest.json`** in `static/`: standalone display mode, dark theme color.

### Changed
- **`ingest.Service` interface**: `MailNotifier` interface introduced with `SendNewMail(ctx, subject, from)`. `SetNotifier` attaches the notifier. Call is fire-and-forget goroutine.

### Fixed
- `GenerateVAPIDKeys()` wrapper had `(public, private)` return order but passed through `webpush.GenerateVAPIDKeys()` unswapped, which returns `(private, public)` — keys were silently backwards, causing browser subscription rejection and FCM 403.
- Nil pointer dereference at startup when VAPID keys are not configured: `pushNotifier.HexPublicKey()` was called unconditionally before checking if the notifier was created.
- `X-CSRF-Token` header missing from subscribe/unsubscribe `fetch()` calls — all push subscription saves were rejected with 403 by the CSRF middleware.
- VAPID key pair consistency check added to `push.New()`: logs `ERROR` at startup if `COMSTAC_VAPID_PUBLIC` and `COMSTAC_VAPID_PRIVATE` do not correspond to the same key pair (e.g. copied from different `comstac vapid` runs).

## [0.9.1] - 2026-04-13
### Added
- **Mobile push-pattern layout**: portrait viewports (≤ 680px) show list only; tapping a message slides the reader overlay in full-width from the right. `body.detail-open` CSS class drives the transition.
- **Swipe to trash/restore on mobile**: left or right swipe ≥ 80px on a list row triggers the trash/restore action. Visual feedback (translate + fade) during the swipe; row animates off-screen on confirm, snaps back on cancel.
- **`← back` button in brand area**: replaces "comstac" wordmark when `body.detail-open`; shares exact same space so filter nav is never pushed or crowded.
- **Infinite-scroll pagination**: "load older" button replaced with `#msg-more-sentinel` div using `hx-trigger="revealed"` (intersection observer). Appended rows rendered by new `message_list_more` template (no wrapping `#message-list` div). `— end —` indicator shown when no more pages. `MoreURL` only set when `len(items) == opts.Limit` (previously set on any non-empty result).
- **`message_list_more.html`** template: renders bare rows + next sentinel for append-mode pagination.
- **`settings` table** (migration `0011_settings.sql`): generic key/value store for runtime-mutable config such as the IMAP refresh token.
- **Web-based Gmail re-authorization** (`/ui/oauth/start` → `/ui/oauth/callback`): server-side OAuth2 Authorization Code flow using dedicated Web Application credentials (`COMSTAC_OAUTH_CLIENT_ID`, `COMSTAC_OAUTH_CLIENT_SECRET`, `COMSTAC_BASE_URL`). New refresh token persisted to `settings` table and applied to the live `TokenSource` without a service restart.
- **IMAP auth failure detection**: `TokenSource` tracks `invalid_grant` errors with timestamp. "accounts" button in topbar turns red with `⚠` on auth failure. Accounts page shows a contextual error banner with a direct re-authorize link; banner is hidden when healthy.
- **Two-row topbar on mobile**: brand row (top) + scrollable filter nav row (bottom); both compose and search remain visible.
- Action buttons in reading pane wrap to a single scrollable row on mobile (no-scrollbar, `nowrap`).
- `SourceFilter` field on `messageListData`: "permanently deleted" link in trash view now preserves the current stream filter.

### Changed
- Message list ordering changed from `COALESCE(sent_at, created_at)` to `created_at` (received time). Timestamps in the list now show when the message arrived in comstac, not the `Date:` header value. Keyset cursor updated to match.
- Message detail pane date field now shows `sent_at` (email `Date:` header, parsed to local time) in full format `Mon, 2 Jan 2006 15:04:05` via new `fmtFullDate()` helper. Previously showed raw `date_hdr` string.
- Hover trash/restore button hidden on mobile (swipe gesture replaces it).
- Trash button hover highlight now fills edge-to-edge within its container (`overflow: hidden` on `.row-acts`, padding moved into `.ra` buttons).
- Active message highlight now reliably persists across filter/stream changes: uses `htmx.ajax()` Promise (resolves after swap settles) instead of `htmx:afterSwap` body listener, which silently failed for `outerHTML` swaps of detached elements.
- Mark-unread now re-bolds the list row immediately: reading pane `htmx:afterSwap` handler syncs `msg-row--unread` class from `data-read` attribute on `.msg-detail-wrap`. Auto-read timer suppressed when detail is re-rendered by an action (`AutoRead: false`).
- `htmx:afterSwap` bubbling bug fixed: back-button press no longer re-opens message 2.5 s later. Guard `e.detail.target !== pane` prevents child element swaps (auto-read `hx-swap="none"`) from triggering the pane open/close logic.
- `COMSTAC_IMAP_REFRESH_TOKEN` now read from `settings` DB table first (falls back to env var), so web-based re-authorization survives service restarts without editing `.env`.
- `OAuthConfig` uses dedicated Web Application OAuth credentials; IMAP fetcher continues using Desktop app credentials unchanged.
- `api.New()` signature updated to accept `*ui.OAuthConfig`; `ui.RegisterRoutes()` updated to accept `*ui.OAuthConfig`.

## [0.9.0] - 2026-04-13
### Changed
- Full UI overhaul (dark terminal design system):
  1. HTML templates extracted from Go string concatenation to `internal/ui/templates/*.html` via `//go:embed`.
  2. Dark design system: `#0d1117` background, jade-green `#34d399` accent, monospace font stack throughout.
  3. 2-pane workspace (`1fr 2fr` grid) replacing 3-pane sidebar layout.
  4. Command/filter bar in topbar replacing left sidebar.
  5. Bold sender/subject = unread; dimmed = read.
  6. Auto-mark-read after 2.5 s (HTMX delayed trigger + client-side CSS class strip for immediate feedback).
  7. Context-aware reading pane actions: Restore (if trashed), Mark Unread (only when read), Snooze/Clear Snooze, Spam/Not Spam toggle, Trash.
  8. Hover-reveal inline Trash/Restore on list rows; trash button red-accented.
  9. Login page reskinned to dark theme matching main app.
  10. Sender display name extracted from `From:` header via `fmtSender()`.
  11. Gmail-style expanding timestamps in the configured local timezone: `HH:MM` today, `Mon HH:MM` within 7 days, `D Mon` same year, `DD/MM/YY` prior years. IANA timezone data embedded in binary via `_ "time/tzdata"`.
- Filter system redesigned as two independent dimensions: **filter** (inbox / trash / spam) × **stream** (all / smtp / imap). Both dimensions stay active simultaneously; selecting either rebuilds the query from both current states via `htmx.ajax()`.
- Selected message highlighted in list with accent left-border; reading pane clears on any filter/stream change.
- Stacked 2-line list rows: sender + timestamp (top), subject + badges (bottom).
- Source badge (smtp/imap) grouped with timestamp in top-right of each row.

### Added
- Migration `0009_archived_at.sql`: `archived_at TEXT` column on messages — stamped on trash, cleared on restore.
- Migration `0010_sent_at.sql`: `sent_at TEXT` column on messages; index on `COALESCE(sent_at, created_at)`.
- Message ordering by `COALESCE(sent_at, created_at) DESC` with keyset cursor for correct pagination.
- `sent_at` populated during ingest from RFC 2822 `Date:` header (stored UTC ISO-8601).
- Trash scoped to last 14 days; "permanently deleted" link loads all archived messages.
- Trashing/restoring removes the row from the list and clears the reading pane via HTMX OOB swap (`hx-swap-oob="delete"`).

## [0.8.0] - 2026-04-11
## Added
- Attachment support:
  1. MIME walker (`ExtractAttachments`, `GetAttachmentPart`) recursively finds all non-body parts in multipart messages.
  2. Attachment list rendered in reading pane above message body — filename and content-type shown as download links.
  3. `/ui/attachment?id=&part=` streaming endpoint decodes and serves attachment bytes directly from the DB on demand. No disk writes.
  4. `GetRawMIMEByMessageID` store helper for the attachment endpoint.
- CC/BCC on compose and reply:
  1. `CC []string` and `BCC []string` added to `relay.OutboundMessage`.
  2. `Cc:` header added to outbound MIME when CC recipients present; BCC is envelope-only (not in headers, as per RFC).
  3. CC and BCC form fields added to compose and reply as a collapsed `<details>` toggle — hidden by default, expand to use.
  4. `parseAddressList` helper handles comma-separated address input.

## [0.7.0] - 2026-04-11
## Added
- Split inbox by stream: sidebar tabs `SMTP` and `Gmail` filter message list by source; all default views (`All Mail`, `Unread`, `Active`, `Archived`, `SMTP`, `Gmail`) now exclude spam automatically.
- Spam flagging:
  1. Migration `0008_spam.sql`: `spam INTEGER NOT NULL DEFAULT 0` column on messages.
  2. Auto-flag on ingest: messages where both SPF and DKIM hard-fail are automatically marked spam.
  3. `Mark Spam` / `Not Spam` action buttons in message detail pane.
  4. Red "Marked as spam" banner in reading pane when flagged.
  5. `[SPAM]` prefix and dimmed row in list view when spam filter is active.
  6. `Spam` sidebar folder showing only flagged messages.
- Backup/restore:
  1. `comstac backup` subcommand: creates hot SQLite snapshot via `VACUUM INTO`, optionally SCPs to remote host.
  2. `COMSTAC_BACKUP_DIR` (default `/var/lib/comstac/backups`), `COMSTAC_BACKUP_DEST`, `COMSTAC_BACKUP_KEY`, `COMSTAC_BACKUP_RETAIN` (default 7) config vars.
  3. Auto-loads `/etc/comstac/comstac.env` or `comstac.env` so the subcommand works standalone without sourcing the env file.
  4. Local snapshot rotation: keeps last N snapshots, prunes oldest.
  5. `comstac-backup.service` + `comstac-backup.timer` systemd units for daily 02:00 automated backup.
  6. `make install-timer` and `make backup` Makefile targets.
  7. Restore instructions documented in `CONFIG.md`.

## [0.6.0] - 2026-04-11
## Added
- SPF/DKIM/DMARC annotation on inbound messages:
  1. New `internal/validation` package with `CheckMessage(rawMIME, remoteIP, envelopeFrom)`.
  2. SPF checked via `blitiri.com.ar/go/spf` using connecting IP from SMTP session (skipped for IMAP-fetched messages).
  3. DKIM verified via `github.com/emersion/go-msgauth/dkim`; first passing signature wins.
  4. DMARC policy looked up via `github.com/emersion/go-msgauth/dmarc` from From: header domain.
  5. Results stored as JSON in new `auth_results` column (`0007_auth_results.sql`).
  6. Inline auth badges rendered in message detail pane (colored DKIM/SPF/DMARC labels).
- Metrics endpoint `GET /metrics` (initially public; moved behind session authentication in `0.9.3`):
  - `uptime_seconds`, `requests_total`, `messages_total`, `messages_unread`, `sync_jobs_pending`, `sync_jobs_failed`.
  - `requests_total` tracked via in-process atomic counter.
- Session cookie `Secure` flag enabled — cookie only sent over HTTPS.
- SMTP server now extracts connecting IP from `gosmtp.Conn` and passes it through `IngestInput.RemoteIP` for SPF evaluation.

## [0.5.0] - 2026-04-11
## Added
- CSRF protection:
  1. HMAC-SHA256 token derived per session from `COMSTAC_CSRF_SECRET` (defaults to a sha256 of admin password if unset).
  2. Token embedded in `<body hx-headers="...">` so all HTMX requests carry it as `X-CSRF-Token`.
  3. Logout form carries `_csrf` hidden field for non-HTMX POST.
  4. Middleware validates `X-CSRF-Token` header (or `_csrf` form value) on all protected mutating requests; returns 403 on mismatch.
- Structured logging via `log/slog` throughout:
  1. Text handler with `INFO` level set as default in `main()`.
  2. All `log.Printf`/`log.Fatalf` calls replaced with `slog.Info`/`slog.Warn`/`slog.Error` with `component` attribute (`smtp`, `imap`, `api`, `sync`, `ui`, `relay`).
- New config var: `COMSTAC_CSRF_SECRET`.

## [0.4.0] - 2026-04-10
## Added
- HTML email rendering:
  1. HTML-only and multipart/alternative emails now render in a sandboxed `<iframe srcdoc="...">` in the reading pane (`sandbox=""` blocks scripts, plugins, and forms).
  2. `htmlToText` stripper converts HTML to readable plain text for FTS indexing and plain-text fallback.
  3. `extractBodyHTML` extracts raw HTML part from MIME for iframe rendering.
  4. `extractBodyPart` updated to prefer `text/plain` in multipart/alternative, and strip HTML when only `text/html` is present.
- Sent mail view:
  1. "Sent" button in sidebar loads outbound message list from `outbound_messages` table.
  2. Failed sends highlighted in red.
  3. Pagination via `before_id` cursor consistent with inbox.
- Full-text search:
  1. Migration `0006_fts.sql`: `body_text TEXT` column added to `messages` table.
  2. FTS5 virtual table `messages_fts` indexes `subject`, `from_addr`, and `body_text` with insert/update/delete triggers.
  3. `body_text` populated during ingest — all new messages are fully searchable.
  4. Search box in sidebar POSTs to `/ui/search?q=...` and returns standard message list.
  5. Historical messages searchable by subject and from_addr; body search applies to messages ingested from this release onward.
- Renamed session cookie from `sovereign_session` to `comstac_session`.

## [0.3.0] - 2026-04-10
## Added
- Outbound SMTP relay:
  1. `internal/relay` package with `Relay.Send()` — constructs MIME message and delivers via STARTTLS/PLAIN auth.
  2. Compose UI: `/ui/compose` GET/POST — blank compose form in reading pane.
  3. Reply UI: `/ui/reply` GET/POST — pre-filled reply form with quoted original body.
  4. Reply button on every message detail view; Compose button in sidebar.
  5. `outbound_messages` table (`0005_outbound.sql`) tracking sent mail with status, error, and raw MIME.
  6. `SaveOutbound` store function — persists every send attempt regardless of success/failure.
  7. In-Reply-To and References headers threaded correctly on replies.
- New config vars: `COMSTAC_RELAY_HOST`, `COMSTAC_RELAY_PORT`, `COMSTAC_RELAY_USERNAME`, `COMSTAC_RELAY_PASSWORD`, `COMSTAC_RELAY_FROM`.
- Relay is opt-in: server starts normally with a log message if relay credentials are not set.
- Renamed project references from "Sovereign Mail" → "Comstac" throughout UI.

## [0.2.0] - 2026-04-10
## Added
- Gmail IMAP OAuth2 fetch loop:
1. `comstac authorize` subcommand for one-time OAuth2 loopback flow (no App Password needed).
2. `TokenSource` with cached access token and automatic refresh before expiry.
3. IMAP fetcher with configurable poll interval, batched fetch (50/cycle), and checkpoint-tracked resume.
4. History-skip on first connection: anchors checkpoint to current UIDNEXT, ignores backlog.
5. `imap_checkpoints` table (`0004_imap_checkpoints.sql`) for per-account/mailbox UID tracking.
6. `EnsureIMAPAccount` store function — auto-creates IMAP account row on first fetch.
7. Duplicate-safe ingestion: SHA256 constraint violations treated as no-ops.
- New config vars: `COMSTAC_IMAP_ADDR`, `COMSTAC_IMAP_USERNAME`, `COMSTAC_IMAP_MAILBOX`, `COMSTAC_IMAP_POLL_INTERVAL`, `COMSTAC_IMAP_CLIENT_ID`, `COMSTAC_IMAP_CLIENT_SECRET`, `COMSTAC_IMAP_REFRESH_TOKEN`.
- IMAP fetcher is opt-in: server starts normally with a log message if credentials are not configured.

## [0.1.0] - 2026-04-10
## Added
- Project governance documents:
1. `SYSTEM.md` technical design baseline.
2. `SPECS.md` software requirements baseline.
3. `ROADMAP.md` phased development plan.
4. `AGENDA.md` collaboration and execution tracker.
5. `DEV_COMPANION.md` developer-facing walkthrough of runtime behavior, API/SMTP flow, testing loop, and common debugging patterns.
6. `AGENTS.md` contributor guide with mandatory stateful documentation workflow.
7. `CONFIG.md` runtime configuration reference.
- Initial `0.1.x` implementation scaffold:
1. Go module and runnable entrypoint in `cmd/comstac`.
2. SMTP receiver scaffold using `go-smtp` that accepts mail, logs `DATA`, and forwards to ingest.
3. SQLite store layer with embedded migration runner and WAL mode.
4. `0001_initial.sql` schema for accounts, raw messages, normalized messages, threads, and indexes.
5. Unified ingest pipeline for SMTP/IMAP raw MIME normalization.
6. Placeholder package boundaries for `api`, `auth`, `imap`, `sync`, and `ui`.
- Runtime/API improvements:
1. Added HTTP server scaffold with `/healthz` endpoint.
2. Added mailbox list endpoint `GET /api/messages` with filter support (`limit`, `before_id`, `unread`, `archived`, `source`) and cursor field `next_before_id`.
3. Added message detail endpoint `GET /api/messages/{id}`.
4. Added message state action endpoints:
`POST /api/messages/{id}/read`, `POST /api/messages/{id}/archive`, `POST /api/messages/{id}/snooze`.
5. Added account management endpoint `GET/POST /api/accounts` (list accounts, create local account).
6. Main process now runs SMTP, HTTP, and sync-runner loops with coordinated context-based shutdown.
7. Added SMTP integration smoke tests for ingest success, account routing, and unknown-recipient rejection.
8. Added API integration tests validating auth, health, filter behavior, cursor pagination, detail fetch, message actions, and account create/list conflict behavior.
9. Added multipart MIME plain-text extraction path for message detail rendering.
10. Added store-level test covering multipart/alternative plain-text extraction.
- SMTP policy and routing:
1. Added local recipient/domain validation options via env (`COMSTAC_LOCAL_DOMAINS`, `COMSTAC_LOCAL_RECIPIENTS`).
2. SMTP `RCPT TO` now rejects unknown/unauthorized recipients with `550`.
3. Added local account bootstrap and recipient-to-account routing into ingest (`account_id`).
- IMAP-origin state sync stubs:
1. Added `0003_sync_jobs.sql` migration with queued sync job model.
2. Added enqueue-on-action behavior for IMAP-origin messages (`read`, `archive`, `snooze`).
3. Added background sync runner with action dispatch by source and IMAP adapter boundary (`internal/imap.StateSyncAdapter`).
4. Added sync job retry/failure lifecycle:
`retrying` status, exponential backoff (15s base, 15m cap), max-attempt terminal failure, permanent-error short-circuit.
5. Added sync package tests covering success, retry, and terminal-failure transitions.
6. Added API integration coverage ensuring IMAP actions create sync jobs.
- Authentication/session layer:
1. Added SQLite-backed `users` and `sessions` tables (`0002_auth.sql`).
2. Added bootstrap admin creation from config (`COMSTAC_ADMIN_USERNAME`, `COMSTAC_ADMIN_PASSWORD`).
3. Added session TTL config (`COMSTAC_SESSION_TTL_HOURS`).
4. Added login/logout routes (`/login`, `/logout`, `/api/login`, `/api/logout`) with cookie sessions.
5. Added auth gating for UI and API (except public health/login routes).
- UI scaffold:
1. Added desktop 3-pane HTMX + Alpine UI at `/`.
2. Added dynamic message list partial endpoint at `/ui/messages` wired to SQLite-backed mailbox queries.
3. Added dynamic reading-pane partial endpoint at `/ui/message?id=...` and row click wiring.
4. Added reading-pane state-action controls (read/unread, archive/unarchive, snooze/clear snooze).
- Developer workflow tooling:
1. Added `Makefile` with local run, full/focused test, and formatting targets.
2. Added `.env.example` baseline runtime configuration values.
- Account management UI:
1. Added `/ui/accounts` GET/POST endpoint for listing and creating local accounts.
2. Added Accounts panel to sidebar — loads account list and add form into reading pane via HTMX.
- SMTP domain config:
1. Added `COMSTAC_SMTP_DOMAIN` env var to set the SMTP banner hostname (default `localhost`).
2. Wired through `Config.SMTPDomain` and `Options.Domain` in smtpserver.

## Versioning Notes
- `0.x` indicates active API and architecture evolution prior to stability guarantees.
- Breaking changes may occur in minor releases before `1.0.0`.
