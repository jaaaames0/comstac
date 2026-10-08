package imap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	imaplib "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"

	"comstac/internal/ingest"
	"comstac/internal/store"
)

// FetcherConfig holds IMAP connection and polling parameters.
type FetcherConfig struct {
	Addr         string // e.g. "imap.gmail.com:993"
	Username     string // Gmail address
	Mailbox      string // e.g. "INBOX"
	PollInterval time.Duration
	TokenSource  *TokenSource
}

// Fetcher polls an IMAP mailbox and ingests new messages.
type Fetcher struct {
	cfg      FetcherConfig
	db       *sql.DB
	ingestor *ingest.Service
}

func NewFetcher(cfg FetcherConfig, db *sql.DB, ingestor *ingest.Service) *Fetcher {
	if cfg.Mailbox == "" {
		cfg.Mailbox = "INBOX"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 60 * time.Second
	}
	return &Fetcher{cfg: cfg, db: db, ingestor: ingestor}
}

// Run polls the IMAP mailbox until ctx is cancelled.
func (f *Fetcher) Run(ctx context.Context) error {
	accountID, err := store.EnsureIMAPAccount(ctx, f.db, f.cfg.Username)
	if err != nil {
		return fmt.Errorf("imap fetcher: ensure account: %w", err)
	}

	slog.Info("imap fetcher starting", "component", "imap", "username", f.cfg.Username, "mailbox", f.cfg.Mailbox, "poll", f.cfg.PollInterval)

	for {
		if err := f.fetchOnce(ctx, accountID); err != nil {
			slog.Error("imap fetch", "component", "imap", "err", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(f.cfg.PollInterval):
		}
	}
}

func (f *Fetcher) fetchOnce(ctx context.Context, accountID int64) error {
	accessToken, err := f.cfg.TokenSource.AccessToken(ctx)
	if err != nil {
		return fmt.Errorf("get access token: %w", err)
	}

	c, err := imapclient.DialTLS(f.cfg.Addr, nil)
	if err != nil {
		return fmt.Errorf("dial imap: %w", err)
	}
	defer c.Close()

	saslClient := sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{
		Username: f.cfg.Username,
		Token:    accessToken,
	})
	if err := c.Authenticate(saslClient); err != nil {
		return fmt.Errorf("imap auth: %w", err)
	}

	selectData, err := c.Select(f.cfg.Mailbox, nil).Wait()
	if err != nil {
		return fmt.Errorf("select %s: %w", f.cfg.Mailbox, err)
	}
	return f.fetchMailbox(ctx, accountID, selectData.UIDNext, clientMailbox{c})
}

// mailboxReader isolates authenticated IMAP reads from checkpoint policy.
type mailboxReader interface {
	SearchUIDs(from, to imaplib.UID) ([]imaplib.UID, error)
	FetchMessages(uids []imaplib.UID) ([]*imapclient.FetchMessageBuffer, error)
}

type clientMailbox struct{ client *imapclient.Client }

func (m clientMailbox) SearchUIDs(from, to imaplib.UID) ([]imaplib.UID, error) {
	var uidRange imaplib.UIDSet
	uidRange.AddRange(from, to)
	data, err := m.client.UIDSearch(&imaplib.SearchCriteria{UID: []imaplib.UIDSet{uidRange}}, nil).Wait()
	if err != nil {
		return nil, err
	}
	return data.AllUIDs(), nil
}

func (m clientMailbox) FetchMessages(uids []imaplib.UID) ([]*imapclient.FetchMessageBuffer, error) {
	return m.client.Fetch(imaplib.UIDSetNum(uids...), &imaplib.FetchOptions{
		UID:         true,
		BodySection: []*imaplib.FetchItemBodySection{{Peek: true}},
	}).Collect()
}

func (f *Fetcher) fetchMailbox(ctx context.Context, accountID int64, uidNext imaplib.UID, mailbox mailboxReader) error {

	if err := ctx.Err(); err != nil {
		return err
	}
	if uidNext == 0 {
		return fmt.Errorf("mailbox has invalid UIDNEXT")
	}
	lastUID, initialized, err := store.GetIMAPCheckpoint(ctx, f.db, accountID, f.cfg.Mailbox)
	if err != nil {
		return fmt.Errorf("get checkpoint: %w", err)
	}

	// First ever connection: set checkpoint to UIDNEXT-1 so we only fetch
	// messages that arrive from this point forward, ignoring history.
	if !initialized {
		startUID := uint32(uidNext)
		if startUID > 0 {
			startUID-- // UIDNEXT is the next UID to be assigned; last existing is UIDNEXT-1
		}
		slog.Info("imap no checkpoint, skipping history", "component", "imap", "start_uid", startUID)
		if err := store.SetIMAPCheckpoint(ctx, f.db, accountID, f.cfg.Mailbox, startUID); err != nil {
			return fmt.Errorf("set initial checkpoint: %w", err)
		}
		return nil
	}

	// Nothing new: UIDNEXT hasn't advanced past our checkpoint.
	if uint32(uidNext)-1 <= lastUID {
		return nil
	}

	// Search the exact known range: lastUID+1 to UIDNext-1.
	// Using a precise range avoids wildcard ambiguity.
	uids, err := mailbox.SearchUIDs(imaplib.UID(lastUID+1), uidNext-1)
	if err != nil {
		return fmt.Errorf("uid search: %w", err)
	}

	if len(uids) == 0 {
		// SEARCH completed successfully: the known range contains no messages.
		return store.SetIMAPCheckpoint(ctx, f.db, accountID, f.cfg.Mailbox, uint32(uidNext)-1)
	}
	// Search responses are not required to be ordered. Each committed batch
	// must precede every UID in the next batch.
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	for _, uid := range uids {
		if uint32(uid) <= lastUID || uid >= uidNext {
			return fmt.Errorf("search returned UID %d outside requested range", uid)
		}
	}

	slog.Info("imap new messages found", "component", "imap", "count", len(uids), "mailbox", f.cfg.Mailbox)

	const batchSize = 50
	ingested := 0
	for i := 0; i < len(uids); i += batchSize {
		end := i + batchSize
		if end > len(uids) {
			end = len(uids)
		}
		batch := uids[i:end]

		maxUID, err := f.fetchAndIngestBatch(ctx, mailbox, accountID, batch)
		if err != nil {
			return fmt.Errorf("batch %d-%d: %w", i, end, err)
		}

		if maxUID > 0 {
			if err := store.SetIMAPCheckpoint(ctx, f.db, accountID, f.cfg.Mailbox, uint32(maxUID)); err != nil {
				return fmt.Errorf("save checkpoint: %w", err)
			}
		}

		ingested += len(batch)
		slog.Info("imap batch ingested", "component", "imap", "ingested", ingested, "total", len(uids))

		// Check for cancellation between batches.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}

	return nil
}

func (f *Fetcher) fetchAndIngestBatch(ctx context.Context, mailbox mailboxReader, accountID int64, uids []imaplib.UID) (imaplib.UID, error) {
	msgs, err := mailbox.FetchMessages(uids)
	if err != nil {
		return 0, fmt.Errorf("fetch: %w", err)
	}

	requested := make(map[imaplib.UID]bool, len(uids))
	var minUID, maxUID imaplib.UID
	for _, uid := range uids {
		requested[uid] = true
		if minUID == 0 || uid < minUID {
			minUID = uid
		}
		if uid > maxUID {
			maxUID = uid
		}
	}
	seen := make(map[imaplib.UID]bool, len(msgs))
	for _, msg := range msgs {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if msg == nil || !requested[msg.UID] {
			return 0, fmt.Errorf("fetch returned unrequested message")
		}
		if len(msg.BodySection) == 0 {
			return 0, fmt.Errorf("UID %d: missing MIME body", msg.UID)
		}
		raw := msg.BodySection[0].Bytes
		if len(raw) == 0 {
			return 0, fmt.Errorf("UID %d: empty MIME body", msg.UID)
		}

		err := f.ingestor.IngestRaw(ctx, ingest.IngestInput{
			Source:    ingest.SourceIMAP,
			AccountID: sql.NullInt64{Int64: accountID, Valid: true},
			RemoteID:  fmt.Sprintf("%d", msg.UID),
			RawMIME:   raw,
		})
		if err != nil && !isDuplicateError(err) {
			return 0, fmt.Errorf("ingest UID %d: %w", msg.UID, err)
		}

		seen[msg.UID] = true
	}
	if len(seen) != len(requested) {
		// Messages can be expunged between SEARCH and FETCH. Confirm their
		// absence before advancing; an omitted but still-present UID is retryable.
		present, err := mailbox.SearchUIDs(minUID, maxUID)
		if err != nil {
			return 0, fmt.Errorf("verify missing UIDs: %w", err)
		}
		for _, uid := range present {
			if requested[uid] && !seen[uid] {
				return 0, fmt.Errorf("UID %d: message still present but not fetched", uid)
			}
		}
	}
	return maxUID, nil
}

func isDuplicateError(err error) bool {
	return errors.Is(err, ingest.ErrDuplicate)
}
