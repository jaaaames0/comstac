package imap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// StateAction describes an upstream mailbox state update that should be replayed.
type StateAction struct {
	MessageID   int64
	AccountID   sql.NullInt64
	RemoteID    string
	Action      string
	PayloadJSON string
}

// StateSyncAdapter applies local mailbox actions to a remote IMAP provider.
type StateSyncAdapter interface {
	ApplyStateAction(ctx context.Context, action StateAction) error
}

// ErrPermanent marks a sync error as non-retryable.
var ErrPermanent = errors.New("imap sync permanent error")

func PermanentError(err error) error {
	if err == nil {
		return ErrPermanent
	}
	return errors.Join(ErrPermanent, err)
}

func IsPermanentError(err error) bool {
	return errors.Is(err, ErrPermanent)
}

type noopStateSyncAdapter struct {
	strict bool
}

func NewNoopStateSyncAdapter(strict bool) StateSyncAdapter {
	return noopStateSyncAdapter{strict: strict}
}

func (n noopStateSyncAdapter) ApplyStateAction(_ context.Context, action StateAction) error {
	if n.strict {
		return fmt.Errorf("imap adapter not configured for action %q", action.Action)
	}
	return nil
}
