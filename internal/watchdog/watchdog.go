package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alexmchughdev/scorpius/internal/faults"
)

// DefaultThreshold is the heartbeat threshold used when none is supplied.
const DefaultThreshold = 30 * time.Second

const revertTimeout = 30 * time.Second

// ID identifies a registered fault inside one Watchdog.
type ID uint64

// Watchdog auto-reverts active faults whose heartbeats stop arriving.
// Safe for concurrent use.
type Watchdog struct {
	threshold time.Duration
	now       func() time.Time
	logger    *slog.Logger

	mu     sync.Mutex
	nextID ID
	active map[ID]*entry
}

type entry struct {
	name     string
	rev      faults.Reversion
	lastBeat time.Time
}

// Option configures a Watchdog at construction.
type Option func(*Watchdog)

// WithClock injects a clock function. Tests pass a fixed time.
func WithClock(now func() time.Time) Option {
	return func(w *Watchdog) { w.now = now }
}

// WithLogger sets a structured logger. Defaults to slog.Default.
func WithLogger(l *slog.Logger) Option {
	return func(w *Watchdog) { w.logger = l }
}

// New constructs a Watchdog. A zero or negative threshold collapses to
// DefaultThreshold so misconfiguration cannot disable the safety net.
func New(threshold time.Duration, opts ...Option) *Watchdog {
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	w := &Watchdog{
		threshold: threshold,
		now:       time.Now,
		logger:    slog.Default(),
		active:    make(map[ID]*entry),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Register adds a fault and returns an ID for Heartbeat and Deregister.
func (w *Watchdog) Register(name string, rev faults.Reversion) ID {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nextID++
	id := w.nextID
	w.active[id] = &entry{
		name:     name,
		rev:      rev,
		lastBeat: w.now(),
	}
	w.logger.Debug("watchdog registered", "fault", name, "id", id)
	return id
}

// Heartbeat updates the last-beat time. Unknown IDs are silently ignored.
func (w *Watchdog) Heartbeat(id ID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if e, ok := w.active[id]; ok {
		e.lastBeat = w.now()
	}
}

// Deregister removes the fault. Unknown IDs are a no-op.
func (w *Watchdog) Deregister(id ID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if e, ok := w.active[id]; ok {
		delete(w.active, id)
		w.logger.Debug("watchdog deregistered", "fault", e.name, "id", id)
	}
}

// Active reports the number of currently registered faults.
func (w *Watchdog) Active() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.active)
}

// CheckOnce scans active entries once and reverts any whose heartbeats
// have lapsed. Exposed for tests; production uses Run.
func (w *Watchdog) CheckOnce() {
	w.mu.Lock()
	now := w.now()
	var expired []*entry
	for id, e := range w.active {
		if now.Sub(e.lastBeat) > w.threshold {
			expired = append(expired, e)
			delete(w.active, id)
		}
	}
	w.mu.Unlock()

	for _, e := range expired {
		w.revert(e)
	}
}

func (w *Watchdog) revert(e *entry) {
	w.logger.Warn("heartbeat lapsed, auto-reverting", "fault", e.name)

	// Fresh background ctx: a parent shutdown must not abort an in-flight
	// revert. The watchdog's whole purpose is to revert even on shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), revertTimeout)
	defer cancel()

	if err := e.rev.Revert(ctx); err != nil {
		w.logger.Error("watchdog revert failed", "fault", e.name, "err", fmt.Errorf("revert: %w", err))
		return
	}
	if err := e.rev.Verify(ctx); err != nil {
		w.logger.Error("watchdog verify failed", "fault", e.name, "err", fmt.Errorf("verify: %w", err))
	}
}

// Run ticks the watchdog until ctx is cancelled. Call in its own goroutine.
func (w *Watchdog) Run(ctx context.Context) {
	interval := w.threshold / 3
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	w.logger.Info("watchdog started", "threshold", w.threshold, "tick", interval)

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("watchdog stopped")
			return
		case <-t.C:
			w.CheckOnce()
		}
	}
}
