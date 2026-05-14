package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// EventType is the canonical event name. Renaming a constant breaks
// downstream consumers; treat these as a schema.
type EventType string

const (
	EventFaultApplied       EventType = "fault_applied"
	EventFaultReverted      EventType = "fault_reverted"
	EventAbortTriggered     EventType = "abort_triggered"
	EventVerificationFailed EventType = "verification_failed"
)

// Event is one audit log entry, marshalled as a single JSON line.
type Event struct {
	Timestamp  time.Time      `json:"timestamp"`
	Type       EventType      `json:"type"`
	Operator   string         `json:"operator,omitempty"`
	Ticket     string         `json:"ticket,omitempty"`
	Fault      string         `json:"fault,omitempty"`
	Interface  string         `json:"interface,omitempty"`
	Process    string         `json:"process,omitempty"`
	Address    string         `json:"address,omitempty"`
	Duration   string         `json:"duration,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	Message    string         `json:"message,omitempty"`
	Error      string         `json:"error,omitempty"`
}

// Logger writes audit events. Safe for concurrent use.
type Logger struct {
	mu    sync.Mutex
	out   io.Writer
	owned io.Closer
	now   func() time.Time
}

// Option configures a Logger at construction.
type Option func(*Logger)

// WithClock injects a clock. Tests pass a fixed time.
func WithClock(now func() time.Time) Option {
	return func(l *Logger) { l.now = now }
}

// NewLogger wraps dest. Caller owns dest; use OpenFile for scorpius-owned
// file handles.
func NewLogger(dest io.Writer, opts ...Option) *Logger {
	l := &Logger{out: dest, now: nowUTC}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Default returns a Logger writing to os.Stdout.
func Default() *Logger { return NewLogger(os.Stdout) }

// OpenFile opens path append-only (mode 0600). Rotation is the caller's
// responsibility (logrotate, journald); scorpius does not implement it.
func OpenFile(path string, opts ...Option) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}
	l := NewLogger(f, opts...)
	l.owned = f
	return l, nil
}

// Emit writes the event as one JSON line. A zero Timestamp is filled from
// the clock; a non-UTC timestamp is rewritten to UTC.
func (l *Logger) Emit(e Event) error {
	if e.Timestamp.IsZero() {
		e.Timestamp = l.now()
	} else {
		e.Timestamp = e.Timestamp.UTC()
	}

	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal audit event: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.out.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	return nil
}

// Close closes the writer only if the Logger owns it (i.e. opened by
// OpenFile). NewLogger-wrapped writers like os.Stdout are left alone.
func (l *Logger) Close() error {
	if l.owned == nil {
		return nil
	}
	if err := l.owned.Close(); err != nil {
		return fmt.Errorf("close audit log: %w", err)
	}
	return nil
}

func nowUTC() time.Time { return time.Now().UTC() }
