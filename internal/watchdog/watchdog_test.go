package watchdog_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexmchughdev/scorpius/internal/watchdog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingReversion struct {
	reverts  atomic.Int32
	verifies atomic.Int32
	revErr   atomic.Value
	verErr   atomic.Value
}

func (r *recordingReversion) Revert(ctx context.Context) error {
	r.reverts.Add(1)
	if v := r.revErr.Load(); v != nil {
		return v.(error)
	}
	return nil
}

func (r *recordingReversion) Verify(ctx context.Context) error {
	r.verifies.Add(1)
	if v := r.verErr.Load(); v != nil {
		return v.(error)
	}
	return nil
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newWatchdog(t *testing.T, threshold time.Duration) (*watchdog.Watchdog, *fakeClock) {
	t.Helper()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	w := watchdog.New(threshold,
		watchdog.WithClock(clock.Now),
		watchdog.WithLogger(silentLogger()),
	)
	return w, clock
}

func TestNewClampsZeroThresholdToDefault(t *testing.T) {
	w := watchdog.New(0, watchdog.WithLogger(silentLogger()))
	rev := &recordingReversion{}
	w.Register("test", rev)
	w.CheckOnce()
	assert.Zero(t, rev.reverts.Load())
}

func TestHeartbeatKeepsFaultAlive(t *testing.T) {
	w, clock := newWatchdog(t, 100*time.Millisecond)
	rev := &recordingReversion{}
	id := w.Register("test", rev)

	for i := 0; i < 10; i++ {
		clock.Advance(50 * time.Millisecond)
		w.Heartbeat(id)
		w.CheckOnce()
	}

	assert.Zero(t, rev.reverts.Load())
	assert.Equal(t, 1, w.Active())
}

func TestMissedHeartbeatsTriggerReversionAndVerification(t *testing.T) {
	w, clock := newWatchdog(t, 100*time.Millisecond)
	rev := &recordingReversion{}
	w.Register("test", rev)

	clock.Advance(200 * time.Millisecond)
	w.CheckOnce()

	assert.Equal(t, int32(1), rev.reverts.Load())
	assert.Equal(t, int32(1), rev.verifies.Load())
	assert.Equal(t, 0, w.Active())
}

func TestExactlyAtThresholdDoesNotTrigger(t *testing.T) {
	w, clock := newWatchdog(t, 100*time.Millisecond)
	rev := &recordingReversion{}
	w.Register("test", rev)

	clock.Advance(100 * time.Millisecond)
	w.CheckOnce()

	assert.Zero(t, rev.reverts.Load())
	assert.Equal(t, 1, w.Active())
}

func TestDeregisterPreventsReversion(t *testing.T) {
	w, clock := newWatchdog(t, 100*time.Millisecond)
	rev := &recordingReversion{}
	id := w.Register("test", rev)
	w.Deregister(id)

	clock.Advance(200 * time.Millisecond)
	w.CheckOnce()

	assert.Zero(t, rev.reverts.Load())
}

func TestHeartbeatForUnknownIDIsHarmless(t *testing.T) {
	w, _ := newWatchdog(t, 100*time.Millisecond)
	w.Heartbeat(watchdog.ID(99999))
}

func TestDeregisterForUnknownIDIsHarmless(t *testing.T) {
	w, _ := newWatchdog(t, 100*time.Millisecond)
	w.Deregister(watchdog.ID(99999))
}

func TestRevertErrorIsLoggedButDoesNotPanic(t *testing.T) {
	w, clock := newWatchdog(t, 100*time.Millisecond)
	rev := &recordingReversion{}
	rev.revErr.Store(errors.New("revert failed"))
	w.Register("test", rev)

	clock.Advance(200 * time.Millisecond)
	w.CheckOnce()

	assert.Equal(t, int32(1), rev.reverts.Load())
	assert.Zero(t, rev.verifies.Load(), "Verify must not run if Revert errored")
}

func TestVerifyErrorIsLogged(t *testing.T) {
	w, clock := newWatchdog(t, 100*time.Millisecond)
	rev := &recordingReversion{}
	rev.verErr.Store(errors.New("leftover qdisc"))
	w.Register("test", rev)

	clock.Advance(200 * time.Millisecond)
	w.CheckOnce()

	assert.Equal(t, int32(1), rev.reverts.Load())
	assert.Equal(t, int32(1), rev.verifies.Load())
}

func TestMultipleFaultsTrackedIndependently(t *testing.T) {
	w, clock := newWatchdog(t, 100*time.Millisecond)
	a := &recordingReversion{}
	b := &recordingReversion{}
	idA := w.Register("a", a)
	w.Register("b", b)

	clock.Advance(60 * time.Millisecond)
	w.Heartbeat(idA)
	clock.Advance(60 * time.Millisecond)
	w.CheckOnce()

	assert.Zero(t, a.reverts.Load())
	assert.Equal(t, int32(1), b.reverts.Load())
	assert.Equal(t, 1, w.Active())
}

func TestRunShutsDownCleanlyOnContextCancel(t *testing.T) {
	w := watchdog.New(50*time.Millisecond, watchdog.WithLogger(silentLogger()))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of context cancel")
	}
}

func TestRunTriggersReversionAgainstWallClock(t *testing.T) {
	w := watchdog.New(30*time.Millisecond, watchdog.WithLogger(silentLogger()))
	rev := &recordingReversion{}
	w.Register("test", rev)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	require.Eventually(t,
		func() bool { return rev.reverts.Load() >= 1 },
		500*time.Millisecond,
		5*time.Millisecond,
	)

	cancel()
	<-done

	assert.GreaterOrEqual(t, rev.verifies.Load(), int32(1))
}
