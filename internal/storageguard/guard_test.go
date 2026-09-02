package storageguard

import (
	"errors"
	"testing"
)

func testGuard(t *testing.T, limits Limits, m measurement) *Guard {
	t.Helper()
	g, err := New(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	g.measure = func() (measurement, error) { return m, nil }
	return g
}

func TestSnapshotWatermarks(t *testing.T) {
	g := testGuard(t, Limits{MaxStateBytes: 1000, MinFreeBytes: 2000, WarnFreeBytes: 3000}, measurement{
		stateBytes: 850, availableBytes: 2500,
	})
	s, err := g.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Warning || s.Rejecting || s.Reason != "state_warning" {
		t.Fatalf("unexpected snapshot: %+v", s)
	}
}

func TestWithPayloadCapacitySerializesCheckAndWrite(t *testing.T) {
	limits := Limits{MaxStateBytes: 20 << 20, MinFreeBytes: 10 << 20, WarnFreeBytes: 11 << 20}
	m := measurement{stateBytes: 1 << 20, availableBytes: 30 << 20}
	g := testGuard(t, limits, m)

	called := false
	if err := g.WithPayloadCapacity(1<<20, func() error { called = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("persistence callback was not called")
	}
}

func TestRejectsStateAndFreeSpaceLimits(t *testing.T) {
	limits := Limits{MaxStateBytes: 20 << 20, MinFreeBytes: 10 << 20, WarnFreeBytes: 11 << 20}
	tests := []measurement{
		{stateBytes: 19 << 20, availableBytes: 30 << 20},
		{stateBytes: 1 << 20, availableBytes: 12 << 20},
	}
	for _, m := range tests {
		g := testGuard(t, limits, m)
		if err := g.CheckPayload(1 << 20); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("CheckPayload() error = %v", err)
		}
		s, err := g.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if s.Rejections != 1 {
			t.Fatalf("rejections = %d, want 1", s.Rejections)
		}
	}
}

func TestMeasurementFailureRejectsClosed(t *testing.T) {
	g := testGuard(t, Limits{MaxStateBytes: 100, MinFreeBytes: 100, WarnFreeBytes: 200}, measurement{})
	g.measure = func() (measurement, error) { return measurement{}, errors.New("stat failed") }
	if err := g.CheckPayload(1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("CheckPayload() error = %v", err)
	}
}
