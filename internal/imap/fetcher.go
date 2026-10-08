package imap

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"time"

	imaplib "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"

	"comstac/internal/ingest"
	"comstac/internal/mailcontent"
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

	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 30 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", f.cfg.Addr)
	if err != nil {
		return fmt.Errorf("dial imap: %w", err)
	}
	c := imapclient.New(conn, nil)
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

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
	return f.fetchMailbox(ctx, accountID, selectData.UIDNext, clientMailbox{client: c, conn: conn})
}

// mailboxReader isolates authenticated IMAP reads from checkpoint policy.
type mailboxReader interface {
	SearchUIDs(from, to imaplib.UID) ([]imaplib.UID, error)
	FetchMessages(uids []imaplib.UID) ([]*imapclient.FetchMessageBuffer, error)
}

type clientMailbox struct {
	client *imapclient.Client
	conn   net.Conn
}

// Close the transport before draining. Client.Close waits for the decoder,
// which itself waits for the active literal to be consumed.
func (m clientMailbox) abortFetch(cmd *imapclient.FetchCommand) {
	_ = m.conn.Close()
	_ = cmd.Close()
}

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
	if len(uids) != 1 {
		return nil, fmt.Errorf("body fetch requires one UID")
	}
	uid := uids[0]
	metadata := m.client.Fetch(imaplib.UIDSetNum(uid), &imaplib.FetchOptions{UID: true, RFC822Size: true})
	var size int64 = -1
	found := false
	for msg := metadata.Next(); msg != nil; msg = metadata.Next() {
		var got imaplib.UID
		var n int64 = -1
		for item := msg.Next(); item != nil; item = msg.Next() {
			switch item := item.(type) {
			case imapclient.FetchItemDataUID:
				got = item.UID
			case imapclient.FetchItemDataRFC822Size:
				n = item.Size
			}
		}
		if got == uid {
			found, size = true, n
		}
	}
	if err := metadata.Close(); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	} // Possible expunge; caller confirms absence.
	if size < 0 {
		return nil, fmt.Errorf("UID %d: missing size metadata", uid)
	}
	if size > mailcontent.MaxMessageBytes {
		return nil, fmt.Errorf("UID %d: %w", uid, mailcontent.ErrTooLarge)
	}

	// A partial request also bounds what a compliant server transmits. Check
	// literal size and read limit independently rather than trusting metadata.
	section := &imaplib.FetchItemBodySection{Peek: true, Partial: &imaplib.SectionPartial{Size: mailcontent.MaxMessageBytes + 1}}
	cmd := m.client.Fetch(imaplib.UIDSetNum(uid), &imaplib.FetchOptions{UID: true, BodySection: []*imaplib.FetchItemBodySection{section}})
	var result []*imapclient.FetchMessageBuffer
	for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
		if len(result) != 0 {
			m.abortFetch(cmd)
			return nil, fmt.Errorf("multiple bodies for one UID")
		}
		buf := &imapclient.FetchMessageBuffer{}
		for item := msg.Next(); item != nil; item = msg.Next() {
			switch item := item.(type) {
			case imapclient.FetchItemDataUID:
				buf.UID = item.UID
			case imapclient.FetchItemDataBodySection:
				if len(buf.BodySection) != 0 {
					m.abortFetch(cmd)
					return nil, fmt.Errorf("multiple MIME sections")
				}
				if item.Literal == nil {
					continue
				}
				if item.Literal.Size() > mailcontent.MaxMessageBytes {
					m.abortFetch(cmd) // Drain only after closing the transport.
					return nil, mailcontent.ErrTooLarge
				}
				raw, err := mailcontent.ReadBounded(item.Literal, mailcontent.MaxMessageBytes)
				if err != nil {
					// A literal read error already wakes the library decoder. Do
					// not drain/re-read that errored literal concurrently with it.
					_ = m.conn.Close()
					_ = m.client.Close()
					return nil, err
				}
				if int64(len(raw)) != size {
					m.abortFetch(cmd)
					return nil, fmt.Errorf("UID %d: incomplete MIME body", uid)
				}
				buf.BodySection = append(buf.BodySection, imapclient.FetchBodySectionBuffer{Section: item.Section, Bytes: raw})
			}
		}
		if buf.UID != uid || len(result) != 0 {
			m.abortFetch(cmd)
			return nil, fmt.Errorf("unexpected body fetch response")
		}
		result = append(result, buf)
	}
	return result, cmd.Close()
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
	seen := make(map[imaplib.UID]bool, len(uids))
	for _, uid := range uids {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		msgs, err := mailbox.FetchMessages([]imaplib.UID{uid})
		if err != nil {
			return 0, fmt.Errorf("fetch UID %d: %w", uid, err)
		}
		if len(msgs) == 0 {
			continue
		}
		if len(msgs) != 1 {
			return 0, fmt.Errorf("fetch returned multiple bodies for one UID")
		}
		msg := msgs[0]
		if msg == nil || msg.UID != uid {
			return 0, fmt.Errorf("fetch returned unrequested message")
		}
		if len(msg.BodySection) == 0 {
			return 0, fmt.Errorf("UID %d: missing MIME body", msg.UID)
		}
		raw := msg.BodySection[0].Bytes
		if int64(len(raw)) > mailcontent.MaxMessageBytes {
			return 0, fmt.Errorf("UID %d: %w", msg.UID, mailcontent.ErrTooLarge)
		}
		if len(raw) == 0 {
			return 0, fmt.Errorf("UID %d: empty MIME body", msg.UID)
		}

		err = f.ingestor.IngestRaw(ctx, ingest.IngestInput{
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
