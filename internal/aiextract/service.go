package aiextract

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/mail"
	"sync"
	"time"

	"comstac/internal/extract"
	"comstac/internal/store"
)

var (
	// ErrDailyLimit means the day's call allowance is used up.
	ErrDailyLimit = errors.New("daily AI limit reached")
	// ErrNotEligible means the message is missing, trashed or spam.
	ErrNotEligible = errors.New("message is not eligible for AI extraction")
)

// Summary describes one run for the UI.
type Summary struct {
	Status       string
	Proposed     int
	Kept         int
	Created      int
	CostMicroUSD int64
}

// Service runs AI extraction for stored messages, one at a time.
type Service struct {
	db         *sql.DB
	proposer   Proposer
	dailyLimit int
	loc        *time.Location
	nowFn      func() time.Time
	mu         sync.Mutex
}

// NewService returns a service; dates and the daily cap use loc.
func NewService(db *sql.DB, p Proposer, dailyLimit int, loc *time.Location) *Service {
	return &Service{db: db, proposer: p, dailyLimit: dailyLimit, loc: loc, nowFn: time.Now}
}

// Model returns the model id in use.
func (s *Service) Model() string { return s.proposer.Model() }

// DailyLimit returns the configured calls per day.
func (s *Service) DailyLimit() int { return s.dailyLimit }

// DayStart returns the start of the current day in the service's zone.
func (s *Service) DayStart() time.Time {
	n := s.nowFn().In(s.loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, s.loc)
}

// Run extracts dates from one message and stores them as suggestions.
func (s *Service) Run(ctx context.Context, messageID int64, trigger string) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	usage, err := store.AIUsageSince(ctx, s.db, s.DayStart())
	if err != nil {
		return Summary{}, err
	}
	if usage.Calls >= s.dailyLimit {
		return Summary{}, ErrDailyLimit
	}
	t, archived, spam, err := store.GetExtractionTarget(ctx, s.db, messageID)
	if err != nil {
		return Summary{}, err
	}
	if t == nil || archived || spam {
		return Summary{}, ErrNotEligible
	}

	now := s.nowFn()
	sent := messageDate(t.DateHdr, t.CreatedAt)
	html, text := store.ExtractBodyHTML(t.Raw), store.ExtractBodyText(t.Raw)
	compact := extract.CompactText(html, text)
	prompt := BuildPrompt(now, sent, s.loc, t.FromAddr, t.Subject, compact)

	run := store.AIRun{MessageID: messageID, Model: s.proposer.Model(), Trigger: trigger}
	resp, err := s.proposer.Propose(ctx, prompt)
	run.InputTokens, run.OutputTokens = resp.InputTokens, resp.OutputTokens
	run.CostMicroUSD, run.RequestID = resp.CostMicroUSD, resp.RequestID
	run.Status = resp.Status
	if err != nil {
		run.Status, run.Error = "error", truncate(err.Error(), 300)
		s.record(ctx, run)
		return Summary{Status: run.Status, CostMicroUSD: run.CostMicroUSD}, err
	}

	in := extract.Input{Subject: t.Subject, From: t.FromAddr, Date: sent, Text: text, HTML: html}
	cands, dropped := extract.FromProposals(resp.Events, in, compact, now, "ai:"+run.Model)
	run.Proposed, run.Kept = len(resp.Events), len(cands)
	var suggestions []store.CalendarEvent
	for _, c := range cands {
		details, _ := json.Marshal(store.SuggestionDetails{Extractor: c.Extractor, Evidence: c.Evidence, Fields: c.Details})
		suggestions = append(suggestions, store.CalendarEvent{
			Title: c.Title, Kind: c.Kind, Location: c.Location, Notes: c.Notes, AllDay: c.AllDay,
			StartLocal: c.StartLocal, EndLocal: c.EndLocal, TZ: c.TZ, DedupeKey: c.Key, Details: string(details),
		})
	}
	if run.Status == "ok" {
		if run.Created, err = store.SaveAISuggestions(ctx, s.db, messageID, suggestions); err != nil {
			run.Status, run.Error = "error", "store suggestions failed"
			s.record(ctx, run)
			return Summary{}, err
		}
	}
	s.record(ctx, run)
	slog.Info("ai date extraction", "component", "aiextract", "message_id", messageID, "trigger", trigger,
		"status", run.Status, "proposed", run.Proposed, "kept", run.Kept, "dropped", dropped, "created", run.Created,
		"input_tokens", run.InputTokens, "output_tokens", run.OutputTokens, "cost_microusd", run.CostMicroUSD)
	return Summary{Status: run.Status, Proposed: run.Proposed, Kept: run.Kept, Created: run.Created, CostMicroUSD: run.CostMicroUSD}, nil
}

func (s *Service) record(ctx context.Context, run store.AIRun) {
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := store.RecordAIRun(recCtx, s.db, run); err != nil {
		slog.Error("record ai run", "component", "aiextract", "err", err)
	}
}

// senderBatch bounds automatic runs per scheduler tick.
const senderBatch = 3

// RunSenderQueue runs extraction for new inbox mail from automatic sender
// domains, stopping at the daily cap.
func (s *Service) RunSenderQueue(ctx context.Context) error {
	ids, err := store.PendingAISenderMessages(ctx, s.db, senderBatch)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.Run(ctx, id, "sender"); err != nil {
			if errors.Is(err, ErrDailyLimit) {
				return nil
			}
			slog.Warn("automatic ai extraction failed", "component", "aiextract", "message_id", id, "err", err)
		}
	}
	return nil
}

func messageDate(dateHdr, createdAt string) time.Time {
	if t, err := mail.ParseDate(dateHdr); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", createdAt); err == nil {
		return t
	}
	return time.Now()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
