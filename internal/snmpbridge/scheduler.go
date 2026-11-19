package snmpbridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/system"
)

// The read timing of a Target (beszel#008). They are constants, with no
// config key.
const (
	MinReadGap = 10 * time.Second
	// why: the hub waits 30 s for a reply.
	ReadDeadline = 20 * time.Second
	// why: a Status alert of 1 minute must not end before the Target connects again (beszel#026).
	ProbeInterval = 30 * time.Second
)

// ReadFunc reads the full data of one Target. It must stop when ctx ends.
type ReadFunc func(ctx context.Context, opts common.DataRequestOptions) (*system.CombinedData, error)

// Scheduler decides when a Target is read. It is a Source. It makes one read
// at a time, starts a read at most each MinGap, and gives the result of the
// last read in between. It lives as long as the Target, so it spans the
// connections of the Target.
type Scheduler struct {
	Read     ReadFunc
	MinGap   time.Duration
	Deadline time.Duration
	Now      func() time.Time

	mu       sync.Mutex
	inflight *flight
	started  time.Time
	haveLast bool
	lastData *system.CombinedData
	lastErr  error
	failed   bool
	// why: busy stays true after the deadline until the Read call returns.
	busy bool
	log  *onceLog
}

// SetLogger sets the logger of the Scheduler. Call it before the first read.
func (s *Scheduler) SetLogger(l *slog.Logger) {
	s.mu.Lock()
	s.log = &onceLog{log: l}
	s.mu.Unlock()
}

var _ Source = (*Scheduler)(nil)

// flight is one read that runs. The fields are set before done closes.
type flight struct {
	done chan struct{}
	data *system.CombinedData
	err  error
}

// NewScheduler returns a Scheduler with the default times.
func NewScheduler(read ReadFunc) *Scheduler {
	return &Scheduler{Read: read, MinGap: MinReadGap, Deadline: ReadDeadline, Now: time.Now}
}

// Data implements Source. A request that comes while a read runs waits for
// that read. A request in the window after a read gets the result of that
// read: the data, or the error of a failed read.
func (s *Scheduler) Data(opts common.DataRequestOptions) (*system.CombinedData, error) {
	s.mu.Lock()
	f := s.inflight
	if f == nil {
		// why: a reader that ran past the deadline can still run. Start no
		// second read of the Target until it returns.
		if s.haveLast && (s.busy || s.Now().Sub(s.started) < s.MinGap) {
			data, err := s.lastData, s.lastErr
			s.mu.Unlock()
			return view(data, opts), err
		}
		f = s.startLocked()
	}
	s.mu.Unlock()
	<-f.done
	return view(f.data, opts), f.err
}

// Failed reports whether the last read failed. The bridge does not connect a
// Target in this state, until a read is good.
func (s *Scheduler) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// Probe reads the Target through the same rules as Data. It returns when the
// read ends or ctx ends.
func (s *Scheduler) Probe(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.Data(common.DataRequestOptions{})
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// startLocked starts a read. The caller holds s.mu.
func (s *Scheduler) startLocked() *flight {
	f := &flight{done: make(chan struct{})}
	s.inflight = f
	s.busy = true
	s.started = s.Now()
	go s.run(f)
	return f
}

func (s *Scheduler) run(f *flight) {
	ctx, cancel := context.WithTimeout(context.Background(), s.Deadline)
	defer cancel()
	type result struct {
		data *system.CombinedData
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		// why: always ask for the details. A result in the window can then
		// answer a request that needs them. view removes them for a request
		// that does not.
		data, err := s.Read(ctx, common.DataRequestOptions{IncludeDetails: true})
		if err == nil && data == nil {
			err = errors.New("the read gave no data")
		}
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
		ch <- result{data, err}
	}()
	var r result
	select {
	case r = <-ch:
	case <-ctx.Done():
		// A late result of the reader goes to the buffered channel and is dropped.
		r = result{err: fmt.Errorf("the read of the Target took more than %s", s.Deadline)}
	}
	if r.err != nil {
		r.data = nil
	}
	s.mu.Lock()
	s.inflight = nil
	s.haveLast = true
	s.lastData, s.lastErr = r.data, r.err
	wasFailed := s.failed
	s.failed = r.err != nil
	lg := s.log
	s.mu.Unlock()
	// why: the client sends the error to the hub and logs nothing, so the
	// reason of a failed read is only in the log of this line. The once log
	// makes 5 failed reads give one line.
	if lg != nil {
		if r.err != nil {
			lg.write(slog.LevelWarn, "read", "read failed", "err", r.err)
		} else if wasFailed {
			lg.reset()
			lg.log.Info("read is good again")
		}
	}
	f.data, f.err = r.data, r.err
	close(f.done)
}

// view returns a copy of data for one request. The copy has no details when
// the request did not ask for them, so the hub gets them only on request.
func view(data *system.CombinedData, opts common.DataRequestOptions) *system.CombinedData {
	if data == nil {
		return nil
	}
	c := *data
	if !opts.IncludeDetails {
		c.Details = nil
	}
	return &c
}
