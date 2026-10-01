# ROADMAP.md

## Current Status

Comstac is a production-used `0.x` single-user mail client. Core mail, browser,
mobile, backup-integration and security work are complete. The remaining `1.0`
gates are a soak period, clean-install/upgrade validation, and an explicit
release/tag decision. Deferred features are not blockers unless promoted below.

## Product Timeline
Versioning follows SemVer, beginning at `0.1.0` for first usable alpha.

## Phase 0: Foundation (`0.1.x`)
## Objectives
- Establish runnable project skeleton and core runtime wiring.

## Milestones
1. Go module and package boundaries (`smtp`, `imap`, `ingest`, `store`, `api`, `ui`, `auth`, `sync`).
2. SQLite migrations and baseline schema.
3. Basic SMTP listener that accepts and logs `DATA` payload.
4. Unified ingest function from raw MIME input.

## Exit Criteria
- Local run demonstrates SMTP accept + DB write path.

## Phase 1: Unified Ingest + Mailbox Core (`0.2.x`) ✓
## Objectives
- Persist and render real messages from SMTP and IMAP.

## Milestones
1. IMAP fetch loop with checkpoint tracking. ✓
2. MIME normalization into message/thread tables. ✓
3. Embedded HTML/CSS/JavaScript UI scaffold using HTMX. ✓
4. Inbox list supports high-density view (20+ rows visible). — deferred to 0.2.x polish

## Exit Criteria
- User can view both local and external messages in one stream. ✓

## Notes
- Systemd process supervision added as a prerequisite for reliable production use before 0.2.0 tag.

## Phase 2: State Actions + Auth (`0.3.x`)
## Objectives
- Add secure access and actionable mailbox operations.

## Milestones
1. Session-based auth flow backed by SQLite.
2. Read/unread, archive, and snooze actions.
3. Background sync job queue for external-origin actions.
4. Basic audit logging for auth and state changes.

## Exit Criteria
- Protected UI with stable local-first state handling.

## Phase 3: Outbound Mail + Relay (`0.3.x`) ✓
## Objectives
- Deliver replies/composes through smart-host relay.

## Milestones
1. Outbound compose/reply API and UI. ✓
2. SMTP relay client integration with smart-host SMTP (STARTTLS, PLAIN auth). ✓
3. Outbound attempt tracking in `outbound_messages` table. ✓
4. Identity selection on reply (`From` address choice). — deferred; single approved sender for now

## Exit Criteria
- End-to-end send works through configured smart-host. ✓

## Phase 4: Mailbox Polish (`0.4.x`) ✓
## Objectives
- Make the mailbox genuinely usable day-to-day.

## Milestones
1. HTML email rendering — sandboxed iframe for HTML bodies; `htmlToText` stripping for FTS and plain-text fallback. ✓
2. Sent mail view — sidebar tab backed by `outbound_messages`. ✓
3. Full-text search — FTS5 migration + `body_text` column + search box in sidebar. ✓
4. High-density message list (20+ rows visible). — deferred to 0.5.x polish

## Exit Criteria
- All common email formats display correctly; user can find old messages by keyword. ✓

## Phase 5: Threading (`0.7.x`) — Deferred
## Objectives
- Opt-in conversation threading that avoids the repetition/confusion of quoted-reply clutter.

## Milestones
1. Thread grouping by In-Reply-To / References chain.
2. Thread view shows distinct messages only — quoted body repetition suppressed.
3. Threading is opt-in per user preference; inbox/sent dichotomy remains the default.

## Exit Criteria
- Threads feel additive, not confusing.

## Phase 6: Hardening (`0.5.x`–`0.6.x`, expanded in `Unreleased`) ✓
## Objectives
- Close security and reliability gaps; meet all NFRs from SPECS.md.

## Milestones
1. CSRF protection for all state-changing POST endpoints. ✓ (`0.5.0`)
2. Structured logging — component-tagged log lines (smtp, imap, ingest, api, sync). ✓ (`0.5.0`)
3. Basic SPF/DKIM/DMARC annotation on inbound — persisted in `auth_results`, displayed as badges. ✓ (`0.6.0`)
4. Retry/backoff policies for fetch and sync workers. ✓ (sync backoff in place since `0.1.0`)
5. Metrics endpoint: ingest throughput, sync queue depth, DB latency. ✓ (`0.6.0`)
6. TLS for HTTP: nginx reverse proxy with Let's Encrypt — preferred over in-process TLS. ✓
7. Query/index tuning — deferred; SQLite single-user scale does not require it yet.
8. Identity selection on compose/reply — deferred; single approved sender, requires upstream send-as config.

## Exit Criteria
- Stable alpha for daily single-user usage with the planned application and
  browser hardening boundaries in place. ✓

## Phase 7: Operational Polish (`0.7.x`) ✓
## Objectives
- Make the product genuinely reliable for long-term daily use.

## Milestones
1. Split inbox by stream — sidebar tabs `SMTP` and `Gmail` filtering by source. ✓
2. Backup/restore — independent encrypted daily snapshot service with bounded retention and isolated restore validation; the superseded bundled local/SCP helper has been removed. ✓
3. Spam flag — `spam` boolean on messages, `Mark Spam/Not Spam` UI, auto-flag on SPF+DKIM dual failure. ✓
4. Naive Bayes spam filter — deferred; only needed if spam becomes a problem on this private domain.
5. Better reconciliation semantics for upstream folder/state sync — deferred to Phase 8.
6. Optional direct-send SMTP feature gate — deferred.

## Exit Criteria
- Candidate release process and documented operational playbook.

## Phase 8: Attachments + CC/BCC (`0.8.x`) ✓
## Objectives
- Complete last functional gaps before the UI overhaul.

## Milestones
1. On-demand attachment download — MIME walker, streaming endpoint, attachment list in reading pane. ✓
2. CC/BCC on compose and reply — wired through relay MIME headers and SMTP envelope. ✓

## Exit Criteria
- All core functionality shipped. ✓

---

> **Core functionality is complete as of 0.8.0.**
> What remains is UI/aesthetics polish (0.9.x) then release (1.0.0).

---

## Phase 9: UI + Aesthetics + Mobile (`0.9.x`)
## Objectives
- Make the interface genuinely pleasant to use daily, including on mobile.

## Milestones
1. Visual overhaul: typography, spacing, colour, panel proportions. ✓ (`0.9.0`)
2. Dark design system with jade-green accent, command/filter bar, 2-pane workspace. ✓ (`0.9.0`)
3. Independent filter × stream selection; selected-row highlight; smart reading-pane clearing. ✓ (`0.9.0`)
4. Gmail-style expanding timestamps in local timezone. ✓ (`0.9.0`)
5. **Mobile layout**: push pattern — single-column on portrait, list slides out and detail slides in on tap; back button returns to list. ✓ (`0.9.1`)
6. **Mobile topbar**: two-row topbar (brand + filter nav); search and compose stay visible; back button shares brand area. ✓ (`0.9.1`)
7. **Swipe to trash/restore on mobile**: swipe gesture on list rows replaces invisible hover button. ✓ (`0.9.1`)
8. **Web-based Gmail re-authorization**: `/ui/oauth/start` + `/ui/oauth/callback` with IMAP auth failure detection and in-app notification. ✓ (`0.9.1`; web flow removed as unused and broken, failure banner retained)
9. **Push notifications**: Web Push (VAPID) via service worker — subscribe on mobile, trigger on inbound SMTP/IMAP ingest. ✓ (`0.9.2`, production-validated on Android + desktop Chrome)
10. CC/BCC fields styled properly in the new design (currently plain `<details>` toggle). (low priority — may ship as-is)
11. Inline CID image support in HTML emails (replace `cid:` refs with base64 data URIs). (low priority)

## Exit Criteria
- UI feels polished and intentional on both desktop and mobile; push notifications work on Android and desktop. ✓ (iOS PWA push requires Safari 16.4+ — not validated but architecture supports it)

## Phase 10: v1.0.0
## Objectives
- Production-grade single-user release. Exit criterion: comstac is the primary daily driver for all inbound and outbound mail.

## Milestones
1. Soak period — daily-driver use surfaces any remaining functional bugs. (in progress)
2. Full public documentation accuracy pass. ✓ (2026-09-04)
3. Security review and hardening program: authentication/session/CSRF, browser
   and email rendering, resource/storage bounds, SMTP transport and operational
   recovery. ✓ (2026-09-04)
4. Upgrade and migration validation from a clean install.
5. Tagged `1.0.0` release and changelog freeze.

The optional token-authenticated agent SSE endpoint remains dormant code. The
former ghost-mail consumer was abandoned in favour of polling and is not a
`1.0` release requirement.

## Exit Criteria
- Comstac is the primary mail client for daily use with no known functional regressions.
- `1.0.0` release published.

## Risks and Dependencies
- VPS provider outbound port constraints reinforce relay-first strategy.
- Provider-specific IMAP behavior may require adapter abstractions.
- Deliverability quality depends on phased anti-spam maturity.

## Possible Future Security Hardening (Evidence-Triggered)

The current security-hardening program is complete. The items below are
deliberately deferred: they are neither known active vulnerabilities nor
release commitments. Promote one only when monitoring, a changed threat model
or a concrete incident shows that its security gain justifies its operational
complexity, and deploy each production boundary as its own guarded transaction.

1. UID-based outbound egress filtering after every required destination,
   resolver path and independent rollback has been proven.
2. An AppArmor profile if filesystem or execution controls demonstrate
   meaningful protection beyond the existing systemd sandbox.
3. Stronger inbound mail-authentication alignment evaluation (SPF, DKIM and
   DMARC) before treating authentication results as more than observations.
4. MTA-STS and TLS reporting after operational ownership of DNS, policy hosting
   and report handling is established.
5. More generic public SMTP recipient-response wording if address enumeration
   becomes a demonstrated concern.
6. A duplicate nginx exact-location throttle for `/api/login` where deployment
   configuration does not already match Comstac's shared in-process limiter.
7. Broader protocol fuzzing and end-to-end integration tests as maintenance
   capacity permits.
8. Moving Comstac to a separate VM or host if future exposure, tenant count or
   privilege requirements invalidate the present shared-host threat model.
