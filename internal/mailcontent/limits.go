// Package mailcontent defines bounded processing of mail payloads.
package mailcontent

import (
	"errors"
	"io"
)

const MaxMessageBytes int64 = 25 << 20

var ErrTooLarge = errors.New("message exceeds supported 25 MiB size")

// ReadBounded distinguishes exact fit from truncation and never reads more
// than limit+1 bytes. Network callers must arrange cancellation of their reader.
func ReadBounded(r io.Reader, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, errors.New("invalid message size limit")
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}
