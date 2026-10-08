# Release checklist

Run the source gate on the exact candidate commit before release review.
Passing it provides automated source and synthetic migration evidence;
daily-driver soak, real-device acceptance, tagging and deployment need their
own operator decision.

## Source gate

Requirements: Linux, Go 1.26+ (including the race detector's supported build
toolchain), Make and Node.js 18+ with `node --test`. Existing Go dependencies
must be available in the module cache, or fetched through the ordinary Go
module workflow. The gate does not install tools or run the application.

In an isolated development worktree with disposable test state:

```bash
git status --short
make verify
git diff --check
```

`make verify` runs these commands, stopping on any failure:

```bash
go test -race -count=1 ./...
go vet ./...
go mod verify
go build -trimpath -buildvcs=true -o /dev/null ./cmd/comstac
make test-notifications test-contacts
```

It does not invoke formatting, install a binary, start services, read production
configuration or migrate a live database. Review any existing uncommitted work
before changing it; a release candidate must have a clean committed tree.
Module verification checks dependency integrity, not current vulnerability
advisories.

## Migration evidence

`TestMigrationFreshInstallAndPopulatedUpgrades` uses the embedded SQL history
and the application's modernc SQLite driver, including FTS5:

| Starting schema | Coverage |
|---|---|
| Fresh database | Full schema, populated data round trip and reopen |
| 0005 outbound | Pre-FTS mail, search-index seeding and later mail columns |
| 0013 sender-image preferences | Retained mailbox/auth/outbound/preferences before calendar additions |
| 0017 calendar suggestions | Calendar wall-clock/zone/recurrence data and fired-reminder identity before AI/ref/notification additions |
| 0019 message-event references | Retained AI usage, message references and reminder identity before notification snoozes |
| 0020 notification snoozes | Populated pre-contact schema upgrades and stores contacts across reopen |
| 0021 contacts | Existing saved contacts and newly added contacts survive reopening |

Each case upgrades through the real opening/migration path with a database-size
limit, checks a complete migration ledger, reopens without reapplying migrations,
and verifies retained mail bytes/state, checkpoint, synthetic auth records,
outbound history and FTS updates. Calendar-capable cases also prove a retained
fire claim remains one-shot and supports an idempotent notification snooze.
Every starting schema also writes a contact after upgrading and verifies it on
reopen; fresh and current-schema cases preserve pre-existing contacts.

`TestMigrationFailureRollsBackPayloadAndLedger` injects SQL-payload and
migration-ledger failures. Both must roll back the failing migration's DDL/DML
and ledger entry, preserve earlier committed migrations and allow a corrected
retry. All databases and payloads are synthetic temporary test files. This
matrix does not claim every historical release binary can read the new schema.

## Candidate handoff

- Record the candidate commit, exact command outcomes and any known limitations.
- Review changes to persistence, importer checkpoints, notification identity
  and browser/auth boundaries; keep user data and credentials out of fixtures.
- Record an advisory check separately if performed; do not equate `go mod
  verify` with a security audit.
- Leave daily-driver soak open until the operator accepts normal inbound,
  outbound, search, calendar and notification behavior on the candidate.
- Choose the version/tag and freeze the changelog only after release acceptance.

## Separate production transaction

Before deployment, follow `/home/james/AGENTS.md`, the current topology and
operator handbook. Build/export the accepted clean commit into a new immutable
versioned runtime, record its source/binary hashes and build metadata, and
identify the exact consumer, service identity, state, protected credential paths
and dependencies. Never overwrite the running binary or borrow production
credentials for ordinary tests.

Prove a consistent backup and isolated restore, migration on a disposable copy,
and the exact previous-binary/current-schema rollback behavior needed for that
release. Preserve current user data; synthetic source tests are not permission
to restore an older database over it. Prepare independent timed rollback before
switching the consumer.

After the authorized switch, verify mail/UI/auth/CSRF, reminders and real-device
actions, service/resource/storage controls, backup/monitor/heartbeat and unchanged
dependencies. Disarm rollback only after operator acceptance. Then reconcile
the accepted source/runtime provenance and operational documentation. Nothing in
`make verify` performs or authorizes these live steps.

## Optional contact browser check

With Playwright Core and a matching Chromium already available, start the
fixture in one terminal, using a private temporary directory for its URL file:

```bash
preview_dir=$(mktemp -d /tmp/comstac-contact-check.XXXXXX)
COMSTAC_CONTACT_PREVIEW_INFO="$preview_dir/preview.json" \
  go test -count=1 ./internal/ui -run '^TestContactBrowserFixture$' -timeout 8m -v
```

In a second terminal, pass that exact generated URL-file path:

```bash
node internal/ui/testdata/contacts.browser.cjs /tmp/comstac-contact-check.XXXXXX/preview.json
```

Set `COMSTAC_PLAYWRIGHT_MODULE` to an existing Playwright Core module directory
when it is outside normal Node resolution. The browser script stops the fixture
on completion and writes a mobile screenshot beside the URL file. The fixture
binds an ephemeral loopback port, uses disposable synthetic mail/contacts and a
fake sender, and never reads application credentials or delivers mail. It has a
seven-minute fallback shutdown. Authentication/CSRF are tested separately against
the actual API handler. Browser automation is additional evidence; it does not
replace acceptance on the user's real device.
