package audit_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexmchughdev/scorpius/internal/audit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestEmitWritesOneJSONLinePerEvent(t *testing.T) {
	var buf bytes.Buffer
	when := time.Date(2026, 5, 14, 17, 55, 38, 123456789, time.UTC)
	l := audit.NewLogger(&buf, audit.WithClock(fixedClock(when)))

	require.NoError(t, l.Emit(audit.Event{Type: audit.EventFaultApplied, Fault: "network_latency"}))
	require.NoError(t, l.Emit(audit.Event{Type: audit.EventFaultReverted, Fault: "network_latency"}))

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	assert.Len(t, lines, 2)

	for _, line := range lines {
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &got))
	}
}

func TestEmitFieldsAndShape(t *testing.T) {
	var buf bytes.Buffer
	when := time.Date(2026, 5, 14, 17, 55, 38, 123456789, time.UTC)
	l := audit.NewLogger(&buf, audit.WithClock(fixedClock(when)))

	require.NoError(t, l.Emit(audit.Event{
		Type:      audit.EventFaultApplied,
		Operator:  "alex",
		Ticket:    "INC-1234",
		Fault:     "network_latency",
		Interface: "eth0",
		Address:   "10.0.0.5:5432",
		Duration:  "5m",
		Parameters: map[string]any{
			"delay_ms":  200,
			"jitter_ms": 50,
		},
	}))

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))

	assert.Equal(t, "fault_applied", got["type"])
	assert.Equal(t, "alex", got["operator"])
	assert.Equal(t, "INC-1234", got["ticket"])
	assert.Equal(t, "network_latency", got["fault"])
	assert.Equal(t, "eth0", got["interface"])
	assert.Equal(t, "10.0.0.5:5432", got["address"])
	assert.Equal(t, "5m", got["duration"])
	assert.Equal(t, "2026-05-14T17:55:38.123456789Z", got["timestamp"])

	params, ok := got["parameters"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 200, params["delay_ms"])
	assert.EqualValues(t, 50, params["jitter_ms"])
}

func TestOmitEmptyFields(t *testing.T) {
	var buf bytes.Buffer
	when := time.Date(2026, 5, 14, 17, 55, 38, 0, time.UTC)
	l := audit.NewLogger(&buf, audit.WithClock(fixedClock(when)))

	require.NoError(t, l.Emit(audit.Event{Type: audit.EventFaultReverted}))

	got := buf.String()
	for _, field := range []string{"operator", "ticket", "fault", "interface", "process", "address", "duration", "parameters", "reason", "message", "error"} {
		assert.NotContainsf(t, got, `"`+field+`"`, "empty %q must be omitted", field)
	}
	assert.Contains(t, got, `"type":"fault_reverted"`)
	assert.Contains(t, got, `"timestamp":"2026-05-14T17:55:38Z"`)
}

func TestTimestampDefaultsAndIsForcedToUTC(t *testing.T) {
	var buf bytes.Buffer
	when := time.Date(2026, 5, 14, 17, 55, 38, 0, time.UTC)
	l := audit.NewLogger(&buf, audit.WithClock(fixedClock(when)))

	require.NoError(t, l.Emit(audit.Event{Type: audit.EventFaultApplied}))

	bst, err := time.LoadLocation("Europe/London")
	require.NoError(t, err)
	require.NoError(t, l.Emit(audit.Event{
		Type:      audit.EventFaultApplied,
		Timestamp: time.Date(2026, 6, 1, 13, 0, 0, 0, bst),
	}))

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 2)

	assert.Contains(t, lines[0], `"timestamp":"2026-05-14T17:55:38Z"`)
	assert.Contains(t, lines[1], `"timestamp":"2026-06-01T12:00:00Z"`,
		"BST 13:00 must be rewritten as UTC 12:00")
}

func TestEventTypeConstants(t *testing.T) {
	cases := []struct {
		got  audit.EventType
		want string
	}{
		{audit.EventFaultApplied, "fault_applied"},
		{audit.EventFaultReverted, "fault_reverted"},
		{audit.EventAbortTriggered, "abort_triggered"},
		{audit.EventVerificationFailed, "verification_failed"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, string(c.got))
	}
}

func TestOpenFileWritesAppendOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	when := time.Date(2026, 5, 14, 17, 55, 38, 0, time.UTC)

	l1, err := audit.OpenFile(path, audit.WithClock(fixedClock(when)))
	require.NoError(t, err)
	require.NoError(t, l1.Emit(audit.Event{Type: audit.EventFaultApplied, Fault: "first"}))
	require.NoError(t, l1.Close())

	l2, err := audit.OpenFile(path, audit.WithClock(fixedClock(when)))
	require.NoError(t, err)
	require.NoError(t, l2.Emit(audit.Event{Type: audit.EventFaultReverted, Fault: "second"}))
	require.NoError(t, l2.Close())

	body, err := os.ReadFile(path)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	require.Len(t, lines, 2, "second OpenFile must append, not truncate")
	assert.Contains(t, lines[0], `"fault":"first"`)
	assert.Contains(t, lines[1], `"fault":"second"`)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestCloseDoesNotCloseUnownedWriter(t *testing.T) {
	var buf bytes.Buffer
	l := audit.NewLogger(&buf)
	require.NoError(t, l.Close())

	require.NoError(t, l.Emit(audit.Event{Type: audit.EventFaultApplied}))
	assert.NotEmpty(t, buf.String())
}

func TestConcurrentEmitsAreSerialisedAndComplete(t *testing.T) {
	var buf bytes.Buffer
	when := time.Date(2026, 5, 14, 17, 55, 38, 0, time.UTC)
	l := audit.NewLogger(&buf, audit.WithClock(fixedClock(when)))

	const writers, perWriter = 10, 50
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				err := l.Emit(audit.Event{Type: audit.EventFaultApplied, Fault: "concurrent"})
				assert.NoError(t, err)
			}
		}()
	}
	wg.Wait()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, writers*perWriter)

	for _, line := range lines {
		var got map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(line), &got),
			"each line must remain complete under concurrent writes: %q", line)
		assert.Equal(t, "fault_applied", got["type"])
	}
}

type errWriter struct{ err error }

func (e *errWriter) Write(p []byte) (int, error) { return 0, e.err }

func TestEmitSurfacesWriteError(t *testing.T) {
	want := errors.New("disk full")
	l := audit.NewLogger(&errWriter{err: want})

	err := l.Emit(audit.Event{Type: audit.EventFaultApplied})
	require.Error(t, err)
	assert.ErrorIs(t, err, want)
}
