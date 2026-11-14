package snmpbridge

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a clock that a test moves by hand.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newClockScheduler(read ReadFunc) (*Scheduler, *fakeClock) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	s := NewScheduler(read)
	s.Now = clk.Now
	return s, clk
}

func goodData(fan uint16) *system.CombinedData {
	return &system.CombinedData{
		Stats:   system.Stats{Fans: map[string]uint16{"FAN1": fan}},
		Details: &system.Details{Hostname: "t"},
	}
}

// Guard: more than one read at a time, or a read at each request, floods a
// weak device with SNMP requests.
func TestSchedulerOneReadAtATimeAndMinGap(t *testing.T) {
	var reads, running, maxRunning atomic.Int64
	release := make(chan struct{})
	s, clk := newClockScheduler(func(ctx context.Context, _ common.DataRequestOptions) (*system.CombinedData, error) {
		reads.Add(1)
		n := running.Add(1)
		maxRunning.Store(max(maxRunning.Load(), n))
		<-release
		running.Add(-1)
		return goodData(uint16(reads.Load())), nil
	})

	// 5 requests while one read runs: they all wait for it.
	var wg sync.WaitGroup
	results := make([]*system.CombinedData, 5)
	for i := range results {
		wg.Go(func() {
			d, err := s.Data(common.DataRequestOptions{})
			assert.NoError(t, err)
			results[i] = d
		})
	}
	require.Eventually(t, func() bool { return reads.Load() == 1 }, time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	assert.Equal(t, int64(1), reads.Load())
	assert.Equal(t, int64(1), maxRunning.Load())
	for _, d := range results {
		assert.Equal(t, uint16(1), d.Stats.Fans["FAN1"])
	}

	// A request each second for 9 s: no new read.
	for range 9 {
		clk.Advance(time.Second)
		d, err := s.Data(common.DataRequestOptions{})
		require.NoError(t, err)
		assert.Equal(t, uint16(1), d.Stats.Fans["FAN1"])
	}
	assert.Equal(t, int64(1), reads.Load())

	// After 10 s a request starts a new read.
	clk.Advance(time.Second)
	d, err := s.Data(common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Equal(t, uint16(2), d.Stats.Fans["FAN1"])
	assert.Equal(t, int64(2), reads.Load())
}

// Guard: the hub must get the details when it asks for them, and must not
// get them at each poll.
func TestSchedulerDetailsOnlyOnRequest(t *testing.T) {
	s, _ := newClockScheduler(func(context.Context, common.DataRequestOptions) (*system.CombinedData, error) {
		return goodData(1), nil
	})
	d, err := s.Data(common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Nil(t, d.Details)
	d, err = s.Data(common.DataRequestOptions{IncludeDetails: true})
	require.NoError(t, err)
	assert.NotNil(t, d.Details)
}

// Guard: after a failed read the bridge must give an error, never the old
// result: the hub would then show a Target that is off as up.
func TestSchedulerFailedReadGivesErrorNotOldData(t *testing.T) {
	var fail atomic.Bool
	var reads atomic.Int64
	s, clk := newClockScheduler(func(context.Context, common.DataRequestOptions) (*system.CombinedData, error) {
		reads.Add(1)
		if fail.Load() {
			return nil, errors.New("snmp get failed")
		}
		return goodData(7), nil
	})
	_, err := s.Data(common.DataRequestOptions{})
	require.NoError(t, err)
	assert.False(t, s.Failed())

	fail.Store(true)
	clk.Advance(MinReadGap)
	d, err := s.Data(common.DataRequestOptions{})
	assert.Error(t, err)
	assert.Nil(t, d)
	assert.True(t, s.Failed())

	// In the window the error stays.
	clk.Advance(time.Second)
	d, err = s.Data(common.DataRequestOptions{})
	assert.Error(t, err)
	assert.Nil(t, d)
	assert.Equal(t, int64(2), reads.Load())

	// The next good read is the only source of data.
	fail.Store(false)
	clk.Advance(MinReadGap)
	d, err = s.Data(common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Equal(t, uint16(7), d.Stats.Fans["FAN1"])
	assert.False(t, s.Failed())
}

// Guard: a Target that never answers must give an error at the deadline, and
// a late reply must not become data.
func TestSchedulerDeadlineFailsTheRead(t *testing.T) {
	late := make(chan struct{})
	var reads atomic.Int64
	s := NewScheduler(func(ctx context.Context, _ common.DataRequestOptions) (*system.CombinedData, error) {
		reads.Add(1)
		// A reader that ignores the context and answers late.
		<-late
		return goodData(1), nil
	})
	s.Deadline = 50 * time.Millisecond
	start := time.Now()
	d, err := s.Data(common.DataRequestOptions{})
	assert.Error(t, err)
	assert.Nil(t, d)
	assert.Less(t, time.Since(start), 2*time.Second)
	// While the late reader still runs, no second read starts.
	time.Sleep(2 * s.Deadline)
	_, err = s.Data(common.DataRequestOptions{})
	assert.Error(t, err)
	assert.Equal(t, int64(1), reads.Load(), "no second read while the first runs")
	close(late)
	time.Sleep(20 * time.Millisecond)
	assert.True(t, s.Failed(), "a late reply must not clear the failure")
}

// Guard: a failed read with no log line leaves the operator with no reason
// ("the real reason is in the log of the bridge"). 5 failed reads must give
// one line, and the first good read after them one line.
func TestSchedulerLogsFailedReadOnceAndRecovery(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	fail := atomic.Bool{}
	fail.Store(true)
	s, clk := newClockScheduler(func(context.Context, common.DataRequestOptions) (*system.CombinedData, error) {
		if fail.Load() {
			return nil, errors.New("request timeout (after 2 retries)")
		}
		return goodData(1), nil
	})
	s.SetLogger(logger.With("target", "nas-bmc"))
	for range 5 {
		_, err := s.Data(common.DataRequestOptions{})
		require.Error(t, err)
		clk.Advance(MinReadGap)
	}
	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, "read failed"), out)
	assert.Contains(t, out, "target=nas-bmc")
	assert.Contains(t, out, "request timeout")

	fail.Store(false)
	_, err := s.Data(common.DataRequestOptions{})
	require.NoError(t, err)
	clk.Advance(MinReadGap)
	_, err = s.Data(common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(buf.String(), "read is good again"), buf.String())

	// A new failure after the recovery logs again.
	fail.Store(true)
	clk.Advance(MinReadGap)
	_, _ = s.Data(common.DataRequestOptions{})
	assert.Equal(t, 2, strings.Count(buf.String(), "read failed"), buf.String())
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
