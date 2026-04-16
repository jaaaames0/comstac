package imap

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
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

	lastUID, err := store.GetIMAPCheckpoint(ctx, f.db, accountID, f.cfg.Mailbox)
	if err != nil {
		return fmt.Errorf("get checkpoint: %w", err)
	}

	// First ever connection: set checkpoint to UIDNEXT-1 so we only fetch
	// messages that arrive from this point forward, ignoring history.
	if lastUID == 0 {
		startUID := uint32(selectData.UIDNext)
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
	if uint32(selectData.UIDNext) <= lastUID+1 {
		return nil
	}

	// Search the exact known range: lastUID+1 to UIDNext-1.
	// Using a precise range avoids wildcard ambiguity.
	var uidRange imaplib.UIDSet
	uidRange.AddRange(imaplib.UID(lastUID+1), selectData.UIDNext-1)
	searchData, err := c.UIDSearch(&imaplib.SearchCriteria{
		UID: []imaplib.UIDSet{uidRange},
	}, nil).Wait()
	if err != nil {
		return fmt.Errorf("uid search: %w", err)
	}

	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return nil
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

		maxUID, err := f.fetchAndIngestBatch(ctx, c, accountID, batch)
		if err != nil {
			return fmt.Errorf("batch %d-%d: %w", i, end, err)
		}

		if maxUID > 0 {
			if err := store.SetIMAPCheckpoint(ctx, f.db, accountID, f.cfg.Mailbox, uint32(maxUID)); err != nil {
				slog.Error("imap checkpoint save", "component", "imap", "err", err)
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

func (f *Fetcher) fetchAndIngestBatch(ctx context.Context, c *imapclient.Client, accountID int64, uids []imaplib.UID) (imaplib.UID, error) {
	fetchSet := imaplib.UIDSetNum(uids...)
	msgs, err := c.Fetch(fetchSet, &imaplib.FetchOptions{
		UID: true,
		BodySection: []*imaplib.FetchItemBodySection{
			{Peek: true},
		},
	}).Collect()
	if err != nil {
		return 0, fmt.Errorf("fetch: %w", err)
	}

	var maxUID imaplib.UID
	for _, msg := range msgs {
		if len(msg.BodySection) == 0 {
			continue
		}
		raw := msg.BodySection[0].Bytes
		if len(raw) == 0 {
			continue
		}

		err := f.ingestor.IngestRaw(ctx, ingest.IngestInput{
			Source:    ingest.SourceIMAP,
			AccountID: sql.NullInt64{Int64: accountID, Valid: true},
			RemoteID:  fmt.Sprintf("%d", msg.UID),
			RawMIME:   raw,
		})
		if err != nil && !isDuplicateError(err) {
			slog.Error("imap ingest", "component", "imap", "uid", msg.UID, "err", err)
			continue
		}

		if msg.UID > maxUID {
			maxUID = msg.UID
		}
	}
	return maxUID, nil
}

func isDuplicateError(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
