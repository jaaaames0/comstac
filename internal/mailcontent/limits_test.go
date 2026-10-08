package mailcontent

import (
	"errors"
	"io"
	"strings"
	"testing"
)

type endlessReader struct{ read int }

func (r *endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	r.read += len(p)
	return len(p), nil
}

func TestReadBoundedNeverReadsPastLimitPlusOne(t *testing.T) {
	r := &endlessReader{}
	if raw, err := ReadBounded(r, 128); !errors.Is(err, ErrTooLarge) || raw != nil || r.read != 129 {
		t.Fatalf("raw=%d err=%v read=%d", len(raw), err, r.read)
	}
	if raw, err := ReadBounded(strings.NewReader(strings.Repeat("x", 128)), 128); err != nil || len(raw) != 128 {
		t.Fatalf("exact fit: %d %v", len(raw), err)
	}
	if _, err := ReadBounded(io.MultiReader(strings.NewReader("x"), brokenReader{}), 128); err == nil {
		t.Fatal("read error swallowed")
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("injected read failure") }
