//go:build testing

package snmpbridge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schedBridge makes a bridge whose Targets read through a Scheduler with
// short times. read gives the reader of each Target id.
func (h *testHub) schedBridge(read map[string]ReadFunc, targets ...Target) (*testBridge, map[string]*Scheduler) {
	tb := h.bridge(permToken, targets...)
	scheds := map[string]*Scheduler{}
	for _, tg := range targets {
		s := NewScheduler(read[tg.ID])
		s.MinGap = 50 * time.Millisecond
		s.Deadline = 300 * time.Millisecond
		scheds[tg.ID] = s
	}
	tb.Source = func(tg Target) Source { return scheds[tg.ID] }
	tb.ProbeInterval = 50 * time.Millisecond
	return tb, scheds
}

func (h *testHub) status(name string) string {
	recs, _ := h.hub.FindAllRecords("systems")
	for _, r := range recs {
		if r.GetString("name") == name {
			return r.GetString("status")
		}
	}
	return ""
}

func stubRead(name string) ReadFunc {
	stub := StubSource{Name: name}
	return func(_ context.Context, opts common.DataRequestOptions) (*system.CombinedData, error) {
		return stub.Data(opts)
	}
}

// Guard: a Target that does not answer must show as down and must add no
// new row of stats: an old result would show a dead device as up.
func TestHubTargetThatHangsIsDownAndAddsNoStats(t *testing.T) {
	h := newTestHub(t)
	tb, scheds := h.schedBridge(map[string]ReadFunc{alpha.ID: func(ctx context.Context, o common.DataRequestOptions) (*system.CombinedData, error) {
		<-ctx.Done() // the device does not answer
		return nil, ctx.Err()
	}}, alpha)
	start := time.Now()
	tb.start(t)
	eventually(t, "the read failed", func() bool { return scheds[alpha.ID].Failed() })
	assert.GreaterOrEqual(t, time.Since(start), scheds[alpha.ID].Deadline, "the error comes at the deadline")
	eventually(t, "Alpha down", func() bool { return h.status("Alpha") == "down" })
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, 0, h.count("system_stats"), "no row of stats for a Target that does not answer")
	assert.Equal(t, "down", h.status("Alpha"))
}

// Guard: one Target that hangs must not slow the other Targets.
func TestHubHangingTargetDoesNotBlockTheOthers(t *testing.T) {
	h := newTestHub(t)
	hangDone := make(chan struct{})
	t.Cleanup(func() { close(hangDone) })
	tb, scheds := h.schedBridge(map[string]ReadFunc{
		alpha.ID: func(ctx context.Context, _ common.DataRequestOptions) (*system.CombinedData, error) {
			<-hangDone
			return nil, errors.New("late")
		},
		beta.ID: stubRead("Beta"),
	}, alpha, beta)
	scheds[alpha.ID].Deadline = 30 * time.Second
	tb.start(t)

	eventually(t, "Beta up with stats", func() bool {
		return h.status("Beta") == "up" && h.count("system_stats") >= 1
	})
	systems, err := h.hub.FindAllRecords("systems")
	require.NoError(t, err)
	for _, rec := range systems {
		if rec.GetString("name") != "Beta" {
			continue
		}
		sys, err := h.hub.GetSystemManager().GetSystem(rec.Id)
		require.NoError(t, err)
		start := time.Now()
		req, err := sys.WsConn.SendRequest(context.Background(), common.GetData, common.DataRequestOptions{})
		require.NoError(t, err)
		select {
		case msg := <-req.ResponseCh:
			msg.Close()
		case <-time.After(time.Second):
			t.Fatal("Beta did not answer in 1 s while Alpha hangs")
		}
		assert.Less(t, time.Since(start), time.Second)
		req.Cancel()
	}
}

// Guard: a connection that closes while a read runs must not start a second
// read of the same Target: the scheduler outlives the connection.
func TestHubReadSurvivesAClosedConnection(t *testing.T) {
	h := newTestHub(t)
	var started, running, maxRunning atomic.Int64
	release := make(chan struct{})
	stub := stubRead("Alpha")
	tb, scheds := h.schedBridge(map[string]ReadFunc{alpha.ID: func(ctx context.Context, o common.DataRequestOptions) (*system.CombinedData, error) {
		started.Add(1)
		n := running.Add(1)
		maxRunning.Store(max(maxRunning.Load(), n))
		defer running.Add(-1)
		<-release
		return stub(ctx, o)
	}}, alpha)
	scheds[alpha.ID].Deadline = 30 * time.Second
	scheds[alpha.ID].MinGap = 10 * time.Second
	n := attempts(tb)
	tb.start(t)
	eventually(t, "the first read runs", func() bool { return started.Load() == 1 })

	h.stop()
	h.restart()
	eventually(t, "the bridge connects again", func() bool { return n.Load() >= 2 })
	// The new connection asks for data. Give its request time to join the read.
	time.Sleep(300 * time.Millisecond)
	close(release)
	eventually(t, "Alpha up", func() bool { return h.status("Alpha") == "up" && h.count("system_stats") >= 1 })
	assert.Equal(t, int64(1), started.Load(), "one read for the 2 connections")
	assert.Equal(t, int64(1), maxRunning.Load())
}

// Guard: a Target that stays off must not connect again and again. The hub
// logs an ERROR line for each connect and asks for data at once.
func TestHubFailedTargetDoesNotReconnectUntilAGoodRead(t *testing.T) {
	h := newTestHub(t)
	var fail atomic.Bool
	var reads atomic.Int64
	stub := stubRead("Alpha")
	tb, scheds := h.schedBridge(map[string]ReadFunc{alpha.ID: func(ctx context.Context, o common.DataRequestOptions) (*system.CombinedData, error) {
		reads.Add(1)
		if fail.Load() {
			return nil, errors.New("snmp get failed")
		}
		return stub(ctx, o)
	}}, alpha)
	tb.ProbeInterval = 100 * time.Millisecond
	connects := attempts(tb)
	// why: the Target is off at the start. The hub asks for data at the
	// first connect, so the first connect meets the failed read.
	fail.Store(true)
	tb.start(t)
	eventually(t, "the read failed", func() bool { return scheds[alpha.ID].Failed() })
	eventually(t, "Alpha down", func() bool { return h.status("Alpha") == "down" })
	assert.Equal(t, int64(1), connects.Load(), "1 connect before the failure")
	before := reads.Load()
	eventually(t, "5 failed reads", func() bool { return reads.Load() >= before+5 })
	assert.Equal(t, int64(1), connects.Load(), "0 connects in 5 failed reads")

	fail.Store(false)
	eventually(t, "Alpha up again", func() bool { return h.status("Alpha") == "up" && h.count("system_stats") >= 1 })
	assert.Equal(t, int64(2), connects.Load(), "1 connect after the first good read")
}
