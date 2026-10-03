# Calendar design

Status: agreed design, 2026-10-02. Steps 1 (push reliability), 2 (calendar
core) and 3 (rule and template extraction) are implemented; step 4 (AI
extraction) is built and evaluated against real mail.

Step 2 as built: `internal/calendar` (RRULE subset, occurrence expansion,
roster parser for the phone and laptop layouts), `internal/store/calendar.go`
with migration 0016, `internal/scheduler` (snoozes and event reminders, 6 h
catch-up grace, one claim row per sent reminder), and the calendar page
(`internal/ui/calendar.go`, `templates/calendar.html`).

Step 3 as built: `internal/extract` (JSON-LD reservations, iCalendar parts,
Sabre e-tickets, stacked flight tables, hotel check-in/check-out blocks,
trigger-word rules), migration 0017 (`message_extractions`; dedupe keys unique
only among confirmed events), a scheduler task that scans inbox mail in
batches of 40 and on arrival, and the "dates found" strip with add, dismiss,
update and roster-from-yourself import (`internal/ui/suggestions.go`). Flight
times use the airport's zone from an embedded table; times embedded in mail
are not trusted. On the 2026-10-02 mailbox the rules produced 83 candidates
from six months of mail before de-duplication; the inbox backfill produced 11
suggestions from 35 messages.

Step 4 as built: `internal/aiextract` calls NanoGPT's OpenAI-compatible chat
completions API over plain HTTP (`gemini-2.5-flash-lite` by default) with a
strict JSON schema, prices calls from NanoGPT's published per-model rates,
and records the provider request id. `extract.CompactText` (links removed,
stacked table cells unstacked into rows, 30,000-character cap) is what the
model sees; `extract.FromProposals` keeps an event only if its evidence words
appear in order close together in the email and support its date, then
corrects common slips: flight zones from the airport table, flight titles
rebuilt, relative dates ("in 14 days") computed from the email date, stays
made all-day with times in the notes. Migration 0018 (`ai_extraction_runs`,
`ai_sender_domains`); strip controls "find dates with AI" and "always for
<domain>"; accounts-page usage and sender list. Disabled unless
`COMSTAC_NANOGPT_API_KEY` is set; capped by `COMSTAC_AI_DAILY_LIMIT`
(default 50 calls per Sydney day); never sends spam or trash; failed
automatic runs are not retried automatically.

Evaluation on 2026-10-03 against 14 real messages (Ticketek, Jetstar booking,
itinerary and change, Virgin e-tickets, Expedia, Revolut, and noise): after
the evidence and correction changes, gemini-2.5-flash-lite kept 16 of 16
correct events on the booking set at about $0.0005 per email;
qwen/qwen3.7-flash 15 at about $0.0001; openai/gpt-4.1-nano 13 with airport
and time errors; openai/gpt-5-nano failed every call (provider content-policy
400); minimax/minimax-m2.7 ignored the schema. No noise email produced an
event. Later the same day openai/gpt-6-luna and gpt-6-luna-pro also returned
`content_policy_violation` for a bare "Say hi" through NanoGPT, while
gpt-4.1-nano and Gemini answered; OpenAI's newer reasoning models appear to
be blocked upstream for this account, so the UI now shows NanoGPT's reason.

Message view (2026-10-03): the "dates found" strip and the attachment list
moved out of the body into header buttons with compact popover panels, so
the body keeps the full height. The calendar button is greyed out when
nothing was found and the email names no upcoming date
(`extract.MentionsUpcomingDate`); shows a count of dates to add; and, when
the rules found nothing but upcoming dates are mentioned, asks AI on its
first click. Migration 0019 (`message_event_refs`) records every dedupe key
a message's rules or AI found, so an email naming a flight another email
already suggested (or that was dismissed there) shows it too and can add
it; extractor version 2 rescans the inbox to fill these in.

Not yet built: time-zone choice for manual events,
and multi-day timed events spanning the grid.

## Goals

- A calendar inside Comstac, with no external calendar app or feed. Comstac
  owns the display and the reminders.
- Dates found in email (flights, stays, deadlines, expiries, events, promotions)
  become **suggestions** that are added with one click; nothing is added without
  confirmation.
- Custom events, recurring events and a fortnightly work roster.
- Reminders delivered through Comstac's own Web Push.

## Findings that shaped the design

- Of 1135 stored messages (2026-10-02), 2 carried schema.org JSON-LD and 6
  microdata, none of them flight, lodging or event reservations, and none had a
  `text/calendar` part. Structured booking data is a bonus, not the main path.
- With no external feed, Web Push is the only reminder channel, so its
  reliability is a prerequisite (step 1).

## Data model

- `calendar_events`: title, notes, location, `start_at`/`end_at` (UTC),
  `all_day`, `tz` (IANA, default `Australia/Sydney`), `kind` (flight, stay,
  deadline, expiry, event, promo, shift, custom), `source` (manual, email,
  import, roster, ai), `message_id` back-link, `status` (suggested, confirmed,
  dismissed), `details` JSON (flight number, airports, booking reference),
  optional `rrule`, `dedupe_key`, extractor name and version.
- `event_reminders`: event, offset before start or absolute time, fired time.
- `dedupe_key` (for example `PSFU4K/JQ761/2026-12-22`) lets a "your flight has
  changed" email update the existing event and show the difference instead of
  duplicating it.
- Time zones are explicit: flights take the departure airport's zone from an
  embedded IATA table (Adelaide is not Sydney time); Australian addresses map
  by state.

## Reminder scheduler

Generalise the snooze waker into one scheduler for snoozes and event reminders:
one ticker, one idempotent fired record, one notification path. Default
reminders by kind when a suggestion is confirmed: flight 24 h and 3 h before;
stay check-in the evening before; deadline/expiry 3 days before and the morning
of. All editable.

## Extraction

Every extractor writes to the same suggestions table. Suggestions appear as a
"dates found" strip in the message view. Past dates are not suggested.

1. **Structured data**: JSON-LD, microdata, `text/calendar` parts.
2. **Per-sender HTML templates** using the existing HTML tokenizer. Example:
   Sabre/Virgin markup has stable ids (`air-N-flight-number`,
   `air-N-departure-time`, `air-N-departure-date`, `air-N-departure-city-code2`,
   ...). Table layouts (Jetstar changes, hotel check-in/out) are parsed by
   column from HTML rather than flattened text.
3. **Generic rules** on text: absolute dates (`3 Oct 2026`,
   `30 September 2026`, `22Dec26`, `Thu, Oct 29`), relative dates
   (`in 14 days`, anchored to the message `Date` header), and a nearby keyword
   for the kind (expires, removed, due, check-in, departs). Missing years infer
   the next future date and are cross-checked against any weekday given.
4. **AI (opt-in)**: Claude Haiku 4.5 to start, behind a provider interface.
   - Input: HTML reduced to compact cell-preserving text (no tracking URLs,
     images, styles or footers), size-capped, plus subject and `Date`.
   - Output: schema-constrained JSON events, each with confidence and an
     evidence quote. Events whose quote is not in the input, or whose date
     disagrees with it, are rejected.
   - Triggers: per-message "extract with AI" button and an optional per-sender
     automatic list. Never spam or trash. Daily call cap and visible usage and
     cost counts; record real per-call costs before deciding on wider use.
   - Email content is untrusted: the model has no tools and only produces
     suggestions that need confirmation. API key in `/etc/comstac/comstac.env`.

Extraction runs at ingest plus a one-off backfill over inbox mail; extractor
versions allow re-scans when an extractor improves.

## Roster import

The roster app only offers copyable text (or a screenshot). Each day reads as
`[weekday] -> [shift time | "No Shift"] -> [day number] -> [details]`:

- the number closes the day above it; details lines after it belong to that day;
- a stray `(` before a number (an encircled date) is ignored;
- a missing weekday label is inferred from neighbours;
- month and year come from the day-number sequence plus weekdays (28, 29, 30,
  01 with Mon..Thu fixes 28 Sep - 1 Oct 2026); an inconsistent block is
  refused, not guessed;
- a shift ending before it starts ends the next day; location is kept.

Routes: paste into a "roster" box on the calendar page (preview, then confirm),
or email the text to yourself with a subject starting `roster` (same preview;
From is spoofable so it is never applied automatically). Screenshots can go
through the AI provider when enabled. Importing a range replaces existing shifts
in that range so updated rosters do not duplicate.

## Calendar UI

- **Month view scrolls vertically and infinitely** (within a bounded window
  that extends as you scroll), never swiping sideways. Months flow into each
  other with only a light boundary, no hard border or page break.
- Agenda list grouped by day, opening at today; kinds visually distinct.
- Events link back to their source email.
- Custom event form: all-day or timed, location, notes, reminders, recurrence
  presets (daily, weekly, fortnightly, monthly, yearly, chosen weekdays,
  until/count, skip a date) on a small in-house RRULE subset.

## Order

1. Push reliability: TTL, urgency, per-message tags, subscription repair,
   delivery log and test button. Ship with snooze.
2. Calendar core: tables, custom and recurring events, scheduler, views,
   roster paste import.
3. Rule and template extraction, suggestion strip, inbox backfill,
   roster-by-email.
4. AI extraction with cost measurement; optional screenshot roster import.
