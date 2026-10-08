# Remaining reliability fixes and address book

Prepared 2026-10-08. Work sequentially through the stages below. Complete and
verify one stage before beginning the next; keep each stage in its own commit.

Implementation worktree: `/home/james/src/comstac-followups`, branch
`agent/reliability-followups`. S1 is implemented with real-decoder size/literal
and cancellation regressions, metadata-only extraction selection, bounded SQL
reads and oversized-input progress tests. Focused checks and `make verify` pass.
The completed stages are documented in order below.

B3 is implemented with a context-aware verified-TLS relay and shared bounded
post-send history recording. Local SMTP tests cover cancellation at dial,
greeting, TLS, command and DATA boundaries, certificate/host rejection, explicit
rejection, lost acceptance and acceptance followed by disconnect. Compose/reply
tests cover cancelled requests, failed history writes and uncertainty display.
Focused checks and `make verify` pass.

B4 is implemented: whole-message validation precedes network work, uploaded
files fail as a unit on size/open/read errors, empty files are retained, and
canonical address parsing separates MIME names from SMTP mailboxes. Tests cover
draft retention, zero sends for invalid inputs, MIME/filename/Unicode/BCC round
trips, exact serialized bounds and base64 line wrapping. Focused checks and
`make verify` pass.

B5 is implemented with calendar-date preset calculations and matching occurrence
windows. Regressions cover both Sydney transitions, days crossing each boundary,
recurrence, queue rejection/restart, imported New York events and the exact
six-hour grace cutoff. Timed/custom offsets retain elapsed-minute semantics.
Focused checks and `make verify` pass.

N1 hidden-content cleanup is implemented with bounded MIME/HTML processing,
charset and transfer decoding, plain/HTML alternative selection and conservative
padding cleanup. The supplied hidden div now yields visible prose in both mail
and resurfaced-mail payloads; raw and stored/search text remain unchanged.
Focused checks and `make verify` pass. The separate `96` prefix remains
unreproduced; meaningful numbers are preserved, and a sanitized MIME excerpt
has been requested.

F2 is implemented with migration 0021, validated saved contacts, private bounded
search, add/edit/delete and explicit sender saving. Compose/reply autocomplete
quotes names, replaces only the current recipient, preserves unmatched drafts,
and supports arrows/Enter/Escape/Tab and mobile taps across HTMX swaps. Browser
regressions also guard delayed autofocus and touch clicks on underlying controls.
Store/API tests cover validation, duplicates, persistence, literal searches,
authentication and CSRF; the migration matrix includes populated 0020 and 0021.
Focused checks, Node tests, disposable Chromium desktop/mobile checks and
`make verify` pass. The collaborative preview reported no attached desktop host,
so browser checks used existing headless Chromium with temporary local libraries.
No host packages, production configuration or live mail were used.

Ordered implementation commits: S1 `90648f6`, B3 `bbfaa98`, B4 `b28d386`,
B5 `341007c`, N1 `1432150`; F2 is the final address-book commit on this branch.
The only unresolved reported symptom is the unexplained notification `96` prefix.
All work remains a source candidate; main and the running service are unchanged.

## Starting point

The candidate already contains B1 (retry-safe Gmail checkpoints), B2 (ordinary
reminder queue retry), and T1 (migration matrix and `make verify`):

- `bd40aa8` — Gmail checkpoints and exact MIME deduplication.
- `d726b44` — Reminder queue rejection and disabled-push behavior.
- `7897b0a` — Release verification and synthetic upgrade/rollback coverage.

They are committed on `agent/t1-release-verification` in
`/home/james/src/comstac-t1`. Main remains at `a25954b`; these changes are not
merged or deployed. The original planning branch added documentation only;
implementation is now on the worktree and branch recorded above.

## Order and scope

| Stage | Item | Outcome | Main dependency |
|---|---|---|---|
| 1 | S1: bounded mail processing | Large batches cannot retain dozens of full MIME bodies | Preserve B1 checkpoint guarantees |
| 2 | B3: send cancellation/history | Sending obeys deadlines; known acceptance is recorded despite request disconnect | Establish the shared send boundary |
| 3 | B4: attachments and recipients | Uploads remain intact; valid address lists become correct SMTP envelopes | Extend B3's boundary; reuse parsing for contacts |
| 4 | B5: all-day DST reminders | A preset labelled 9am fires at local 9am through DST | Preserve B2 retry/idempotency behavior |
| 5 | N1: readable notification snippets | Hidden padding and entity artifacts do not crowd out readable message text | Bounded MIME processing from S1 |
| 6 | F2: address book | Manage saved contacts and select recipients in compose/reply | Correct recipient parsing from B4 |

The four review items are fixes. N1 includes a confirmed hidden-content issue
and an unresolved numeric-prefix report. F2 is a new feature with a deliberately
small initial scope. The AI accounting issue (B6) and optional calendar-zone and
Spence features are outside this sequence.

## Workflow and common acceptance

Read `/home/james/AGENTS.md` and applicable instructions. Preserve existing work.
Use a new implementation branch/worktree based on the accepted candidate, e.g.:

```bash
git -C /home/james/src/comstac worktree add -b agent/reliability-followups /home/james/src/comstac-followups agent/reliability-address-book-plan
```

Confirm that base still contains the three prerequisite commits before editing.
Do not develop on main. Do not start simultaneous edits to these stages: S1
shares importer code with B1, B3/B4 share send handlers, and B4/F2 share address
parsing. A later session can continue from the last verified stage commit.

For each fix, first add a focused regression that reproduces the old behavior,
implement the smallest change, then run the stage checks below. Use synthetic
MIME, temporary databases and local/injected network fakes. Finish each stage
with `make verify` and `git diff --check`, then commit and record its behavior,
test results and limitations. Update the changelog and relevant documentation.
Do not rerun unchanged checks simply to collect more output.

Keep the production runtime, credentials and live database untouched. No real
Gmail, outbound messages, push delivery or paid AI calls are needed. Existing
SQL migrations stay immutable; an address-book migration is a new source file
tested on synthetic state. Branch handoff does not merge, push or deploy.

## 1. S1 — Bound IMAP and extraction bodies

Evidence: `internal/imap/fetcher.go` still uses `Fetch(...).Collect()` for
50-message batches; `internal/store/suggestions.go` includes every MIME blob
in a 40-target slice consumed by `internal/scheduler/extract.go`.

- Select IDs/metadata first. Fetch/read and process individual MIME bodies with
  explicit limits instead of retaining a batch of complete messages.
- Inspect IMAP size metadata before reading a full literal, and enforce the
  bound on the reader too. Reuse the existing supported 25 MiB policy; account
  for parsing copies as well as the raw body when documenting the actual bound.
- Preserve B1: a failed or oversized UID remains retryable and observable, and
  no later checkpoint may hide it. Do not silently delete/skip oversized mail.
- Handle oversized extraction inputs without marking them successfully scanned
  or starving unrelated ordinary messages. Bound selection and logging so an
  old oversized message cannot create an endless busy loop.
- Test streaming/read limits with instrumented readers and modest fixtures:
  aggregate buffering does not grow with batch count, oversized bodies stop
  before full allocation, cancellation cleans up, and ordinary messages still
  progress. Rerun the checkpoint, duplicate and extraction-version regressions.

Focused check: `go test -count=1 ./internal/imap ./internal/ingest ./internal/store ./internal/scheduler`.

Stop to report any required policy to permanently discard or skip oversized
messages; do not raise systemd limits or run host OOM stress tests.

## 2. B3 — Align SMTP lifetime and sent-history lifetime

Evidence: `internal/relay/relay.go` ignores the context passed to `Send`.
Compose/reply in `internal/ui/handler.go` subsequently save the outcome using
the potentially cancelled request context.

- Use context-aware dialing plus connection cancellation/deadlines with the
  pinned SMTP library. Bound the operation consistently with the HTTP timeout.
  Preserve required STARTTLS, hostname/certificate verification and approved
  sender/authentication behavior.
- Centralize compose/reply send-and-record behavior behind a narrow testable
  boundary. Persist a known SMTP result using a fresh, bounded context even if
  the request is cancelled after acceptance.
- Distinguish a known accepted send with failed history recording from a failed
  send. Present clear status when the browser remains available; record safe
  operational diagnostics when it has disconnected.
- For ambiguous DATA outcomes, report uncertainty and never retry automatically.
  Do not describe a socket disconnect as proof the recipient did not get mail.
- Test cancellation during dial/greeting/commands/DATA, TLS rejection, known
  acceptance followed by request cancellation, and failed history writes. Prove
  neither handler issues a second SMTP attempt after acceptance.

Focused check: `go test -count=1 ./internal/relay ./internal/ui ./internal/store ./internal/api`.

Stop for an outbox/schema redesign or a required product decision about durable
ambiguous-send tracking; keep the demonstrated lifetime fix bounded.

## 3. B4 — Preserve attachments and correctly parse recipients

Evidence: uploaded files are read through a 25 MiB `LimitReader` and errors are
silently ignored. Address lists are split on commas, and display-name strings
are passed directly into the SMTP envelope.

- Read at most limit+1 bytes and reject oversize/open/read failures before
  sending. Support intact zero-byte attachments. Preserve entered recipients,
  subject and body when reporting a form error; never send a partial upload.
- Bound total serialized MIME, including base64 expansion and message headers,
  and make any effective upload limit visible. Validate the full message before
  network work; use the existing size policy unless a separate change is needed.
- Parse with `net/mail.ParseAddressList`. Keep display names in headers, use
  mailbox addresses for envelope recipients, and keep BCC out of MIME headers.
  Use the same validation in compose, reply and reply-all.
- Test exact-limit and over-limit inputs, upload failures, empty attachments,
  byte-for-byte round trips, quoted commas, Unicode display names, invalid
  addresses, injection attempts and CC/BCC confidentiality. Reuse B3's fake
  send boundary to prove validation failure makes zero sends.

Focused check: `go test -count=1 ./internal/relay ./internal/ui ./internal/api`.

## 4. B5 — Calculate all-day presets in local calendar time

Evidence: `internal/ui/calendar.go` labels -540/900/3780/9540-minute offsets as
local 9am presets, while `internal/scheduler/events.go` subtracts elapsed minutes
from midnight. Sydney transition-day results can be 8am or 10am.

- Map existing named all-day presets to local calendar-day shifts plus 09:00 in
  the event's zone. Keep timed-event offsets as elapsed durations.
- Share the calculation with scheduler candidate-window selection; correcting
  only the final fire time can still omit reminders near the window boundary.
- Preserve existing stored offsets/occurrence keys and B2's six-hour retry
  grace. Keep existing non-preset offset behavior deliberate and documented.
- Test both Sydney DST transitions, day/previous-day/three-day/week presets,
  recurring events, another IANA zone, grace boundaries and queue rejection.
  Timed-event tests must retain their current elapsed-time behavior.

Focused check: `go test -count=1 ./internal/calendar ./internal/scheduler ./internal/store ./internal/ui`.

## 5. N1 — Notification body snippets

The supplied example explicitly hides its `<div>` with `display: none` and
zero-height/overflow styles. Current `htmlToText` removes tags but keeps that
element's text; zero-width padding survives the final whitespace collapse.
`ExtractBodyText` also prefers a plain-text MIME alternative, whose literal
entities currently receive no HTML decoding. These paths explain a plausible
padding/entity failure, but do not establish the cause of the reported `96`.

- Add synthetic fixtures based on the supplied hidden div, followed by visible
  prose. Cover HTML-only and multipart/alternative messages, quoted-printable,
  base64 and declared character encodings, named/numeric entities, multiline
  styles, hidden descendants and actual versus literal zero-width padding.
- Use bounded structural HTML parsing to omit explicitly hidden content and
  script/style/head text from previews. The existing `golang.org/x/net/html`
  dependency is available. Do not fetch images, stylesheets or links.
- Normalize entity/padding artifacts before applying the 300-character preview
  limit. Preserve visible Unicode text, emoji and legitimate joiners in words;
  do not globally strip all formatting characters or reinterpret ordinary
  plain text as HTML.
- Evaluate MIME alternatives: clean readable plain text remains useful, but a
  padding-only or HTML-contaminated alternative should not eclipse readable
  visible HTML. Bound nesting, decoded bytes and parser work using S1's policy.
- Keep this cleanup specific to notification previews initially. Retain raw
  MIME, stored/search body text, mail rendering and date-extraction behavior;
  shared body changes would need their own regression coverage.
- Reproduce `96` separately. Inspect each transformation using synthetic or
  explicitly supplied sanitized input. Do not remove a numeric prefix by rule:
  retain real text such as “96 items”, amounts and dates. If no fixture exposes
  the cause, record it as unresolved and request only a sanitized MIME/header
  excerpt sufficient to distinguish transport encoding from body extraction.
- Test mail and resurfaced-mail payloads, empty-body fallback, truncation,
  existing tracking-link cleanup and unchanged original message bytes.

Focused check: `go test -count=1 ./internal/push ./internal/store ./internal/ui`;
run `make test-notifications` for the worker boundary as well.

Success: the example's hidden padding contributes nothing to the snippet, and
the preview starts with visible prose. Treat the `96` symptom as fixed only
after a reproducer demonstrates its cause and the regression passes.

## 6. F2 — Address book, first version

Accepted scope: explicitly saved contacts only, with add/edit/delete, search,
a “save contact” action from an email and To/CC/BCC autocomplete in compose/reply.
James selected saved contacts only on 2026-10-08. Autocomplete searches saved
contacts; recent correspondence does not supply additional suggestions.

- Store a display name and validated email address per contact in SQLite.
  Define duplicate/update behavior while preserving the address as entered;
  avoid silently changing a mailbox's meaningful local part.
- Add a small contact-management page that fits the existing HTMX/mobile UI.
  Saving a sender must require explicit user action; a display name/address in
  mail is untrusted input. No automatic contact creation.
- Add bounded authenticated contact search and recipient autocomplete. Reuse
  B4's parsing and quoting so names containing commas select/send correctly.
  Suggestions must work with multiple recipients and HTMX-replaced forms.
- Cover keyboard selection, focus, Escape, accessible labels and mobile taps.
  Preserve the user's typed address when there is no saved match.
- Protect mutations with existing authentication/CSRF, validate lengths and
  page/query limits, escape names, and avoid exposing contacts in public routes,
  logs or notification payloads.
- Add the next migration without modifying prior migrations. Extend T1 with
  a populated pre-contact upgrade/reopen case and contact retention tests.
- Use store/API tests for persistence, duplicates, validation and access denial;
  browser checks for contact management and compose/reply autocomplete. Use
  the product's collaborative browser when available, with disposable app state.

Focused check: `go test -count=1 ./internal/store ./internal/api ./internal/ui ./internal/relay`,
plus interaction checks and the common `make verify` gate.

Defer groups, phone/postal fields, provider sync, bulk import/export and automatic
harvesting until the basic address book is useful. If recent correspondents are
considered later, review them as a separate feature.

## Final handoff

Provide the ordered stage commits, focused and full verification outcomes,
the notification-prefix reproduction status and address-book interaction notes.
List any remaining product decisions or limitations explicitly. Keep deployment
and daily-driver acceptance as a separate authorized transaction under
`RELEASE-CHECKLIST.md`; source tests do not change the running application.
