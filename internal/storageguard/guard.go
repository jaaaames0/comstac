// Package storageguard prevents Comstac ingestion from consuming storage that
// has been reserved for other services on the same filesystem.
package storageguard

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
)

const (
	growthMultiplier = int64(4)
	growthOverhead   = int64(4 << 20)
)

// ErrUnavailable identifies a temporary storage-capacity rejection.
var ErrUnavailable = errors.New("storage capacity unavailable")

// Limits defines the hard state ceiling and filesystem reserve watermarks.
type Limits struct {
	MaxStateBytes int64
	MinFreeBytes  int64
	WarnFreeBytes int64
}

// Snapshot is a point-in-time, non-secret view of Comstac storage capacity.
type Snapshot struct {
	StateBytes     int64
	AvailableBytes int64
	MaxStateBytes  int64
	MinFreeBytes   int64
	WarnFreeBytes  int64
	Warning        bool
	Rejecting      bool
	Reason         string
	Rejections     int64
}

type measurement struct {
	stateBytes     int64
	availableBytes int64
}

// Guard serializes ingestion capacity checks with their corresponding writes.
type Guard struct {
	stateDir   string
	limits     Limits
	mu         sync.Mutex
	measure    func() (measurement, error)
	rejections atomic.Int64
}

// New creates a guard for all regular files beneath stateDir.
func New(stateDir string, limits Limits) (*Guard, error) {
	if !filepath.IsAbs(stateDir) {
		return nil, fmt.Errorf("storage state directory must be absolute")
	}
	if limits.MaxStateBytes <= 0 || limits.MinFreeBytes <= 0 || limits.WarnFreeBytes <= limits.MinFreeBytes {
		return nil, fmt.Errorf("invalid storage limits")
	}

	g := &Guard{stateDir: filepath.Clean(stateDir), limits: limits}
	g.measure = g.measureFilesystem
	return g, nil
}

// CheckPayload checks whether a conservatively estimated payload expansion can
// be accepted. It does not reserve space; WithPayloadCapacity should surround
// the actual persistence operation.
func (g *Guard) CheckPayload(payloadBytes int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.checkLocked(payloadBytes, true)
}

// WithPayloadCapacity checks capacity and serializes accepted ingestion writes
// so concurrent messages cannot all pass the same capacity measurement.
func (g *Guard) WithPayloadCapacity(payloadBytes int64, persist func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := g.checkLocked(payloadBytes, true); err != nil {
		return err
	}
	return persist()
}

// Snapshot reports current capacity without incrementing rejection counters.
func (g *Guard) Snapshot() (Snapshot, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	m, err := g.measure()
	if err != nil {
		return Snapshot{}, fmt.Errorf("measure storage: %w", err)
	}
	return g.snapshotFor(m), nil
}

func (g *Guard) checkLocked(payloadBytes int64, countRejection bool) error {
	estimated, err := estimateGrowth(payloadBytes)
	if err != nil {
		if countRejection {
			g.rejections.Add(1)
		}
		return fmt.Errorf("%w: invalid payload size", ErrUnavailable)
	}
	m, err := g.measure()
	if err != nil {
		if countRejection {
			g.rejections.Add(1)
		}
		return fmt.Errorf("%w: capacity measurement failed: %v", ErrUnavailable, err)
	}

	if m.stateBytes > g.limits.MaxStateBytes-estimated {
		if countRejection {
			g.rejections.Add(1)
		}
		return fmt.Errorf("%w: state ceiling reached", ErrUnavailable)
	}
	if m.availableBytes < g.limits.MinFreeBytes || m.availableBytes-g.limits.MinFreeBytes < estimated {
		if countRejection {
			g.rejections.Add(1)
		}
		return fmt.Errorf("%w: filesystem reserve reached", ErrUnavailable)
	}
	return nil
}

func (g *Guard) snapshotFor(m measurement) Snapshot {
	s := Snapshot{
		StateBytes: m.stateBytes, AvailableBytes: m.availableBytes,
		MaxStateBytes: g.limits.MaxStateBytes, MinFreeBytes: g.limits.MinFreeBytes,
		WarnFreeBytes: g.limits.WarnFreeBytes, Rejections: g.rejections.Load(),
	}
	s.Rejecting = m.stateBytes >= g.limits.MaxStateBytes || m.availableBytes <= g.limits.MinFreeBytes
	stateWarningAt := g.limits.MaxStateBytes - g.limits.MaxStateBytes/5
	s.Warning = s.Rejecting || m.stateBytes >= stateWarningAt || m.availableBytes <= g.limits.WarnFreeBytes
	switch {
	case m.stateBytes >= g.limits.MaxStateBytes:
		s.Reason = "state_limit"
	case m.availableBytes <= g.limits.MinFreeBytes:
		s.Reason = "free_reserve"
	case m.stateBytes >= stateWarningAt:
		s.Reason = "state_warning"
	case m.availableBytes <= g.limits.WarnFreeBytes:
		s.Reason = "free_warning"
	}
	return s
}

func estimateGrowth(payloadBytes int64) (int64, error) {
	if payloadBytes < 0 || payloadBytes > (math.MaxInt64-growthOverhead)/growthMultiplier {
		return 0, fmt.Errorf("payload size out of range")
	}
	return payloadBytes*growthMultiplier + growthOverhead, nil
}

func (g *Guard) measureFilesystem() (measurement, error) {
	var stateBytes int64
	err := filepath.WalkDir(g.stateDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Size() > math.MaxInt64-stateBytes {
				return fmt.Errorf("state size overflow")
			}
			stateBytes += info.Size()
		}
		return nil
	})
	if err != nil {
		return measurement{}, err
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(g.stateDir, &stat); err != nil {
		return measurement{}, err
	}
	if stat.Bsize <= 0 || stat.Bavail > uint64(math.MaxInt64)/uint64(stat.Bsize) {
		return measurement{}, fmt.Errorf("filesystem size out of range")
	}
	return measurement{stateBytes: stateBytes, availableBytes: int64(stat.Bavail) * stat.Bsize}, nil
}
