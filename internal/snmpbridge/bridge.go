package snmpbridge

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Default backoff values. A connect that fails waits 5 s, then 10 s, and so on
// up to 5 minutes.
const (
	DefaultBackoffInitial = 5 * time.Second
	DefaultBackoffMax     = 5 * time.Minute
)

// Backoff sets the wait between two connects of a Target.
type Backoff struct {
	Initial time.Duration
	Max     time.Duration
}

// Bridge connects each Target to the hub, each with its own WebSocket, and
// connects again after a failure.
type Bridge struct {
	HubURL  string
	Token   string
	Keys    []ssh.PublicKey
	Targets []Target
	Source  func(Target) Source
	Gate    func(Target) bool
	Backoff Backoff
	// why: the default is ProbeInterval (60 s).
	ProbeInterval time.Duration
	Logger        *slog.Logger
}

// readGate is the part of a Source that holds the state of its last read. A
// Source with this state (the Scheduler) stops the connect of a Target
// after a failed read, until a read is good.
type readGate interface {
	Failed() bool
	Probe(ctx context.Context)
}

// Run serves all Targets until ctx ends. It then closes each WebSocket and
// returns.
func (b *Bridge) Run(ctx context.Context) {
	if b.Logger == nil {
		b.Logger = slog.Default()
	}
	if b.Backoff.Initial <= 0 {
		b.Backoff.Initial = DefaultBackoffInitial
	}
	if b.Backoff.Max <= 0 {
		b.Backoff.Max = DefaultBackoffMax
	}
	if b.ProbeInterval <= 0 {
		b.ProbeInterval = ProbeInterval
	}
	var wg sync.WaitGroup
	for _, t := range b.Targets {
		wg.Go(func() { b.runTarget(ctx, t) })
	}
	wg.Wait()
}

func (b *Bridge) runTarget(ctx context.Context, t Target) {
	src := b.Source(t)
	rg, _ := src.(readGate)
	client := &Client{
		HubURL: b.HubURL,
		Token:  b.Token,
		Keys:   b.Keys,
		Target: t,
		Source: src,
		Logger: b.Logger,
	}
	// why: Run creates the once log lazily. Make it here, so this loop can
	// log and reset it.
	client.once = &onceLog{log: b.Logger.With("target", t.ID)}
	delay := b.Backoff.Initial
	for ctx.Err() == nil {
		// why: after a failed read the hub sets the system down and closes
		// the WebSocket. A connect then only makes the hub log an ERROR line
		// and ask for data again. Read the Target each ProbeInterval, and
		// connect after the first good read.
		if rg != nil && rg.Failed() {
			if !sleep(ctx, b.ProbeInterval) {
				return
			}
			rg.Probe(ctx)
			continue
		}
		if b.Gate != nil && !b.Gate(t) {
			if !sleep(ctx, b.Backoff.Initial) {
				return
			}
			continue
		}
		err := client.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		if client.Served() {
			// A good connection: the state of the Target changes.
			client.once.reset()
			delay = b.Backoff.Initial
		}
		b.logRunError(client.once, t, err)
		if rg != nil && rg.Failed() {
			// The next loop reads the Target first. No backoff wait.
			delay = b.Backoff.Initial
			continue
		}
		if !sleep(ctx, delay) {
			return
		}
		delay = min(delay*2, b.Backoff.Max)
	}
}

// logRunError writes one line for each kind of error.
func (b *Bridge) logRunError(once *onceLog, t Target, err error) {
	const hint = "turn on the permanent universal token"
	var hs *HandshakeError
	var de *DialError
	switch {
	case errors.Is(err, ErrBadKey):
		once.write(slog.LevelError, "key", "the hub signature does not match KEY: check the KEY value")
	case errors.As(err, &hs) && hs.Status == 401 && strings.Contains(hs.Body, "Invalid token"):
		once.write(slog.LevelError, "token", "the hub refused the token: "+hint, "name", t.Name, "status", hs.Status, "body", hs.Body)
	case errors.As(err, &hs):
		once.write(slog.LevelError, "handshake", "the hub refused the connection", "name", t.Name, "err", err)
	case CloseReason(err) == "fingerprint mismatch":
		once.write(slog.LevelError, "token", "the hub does not accept this Target: "+hint, "name", t.Name, "reason", CloseReason(err))
	case errors.As(err, &de):
		once.write(slog.LevelWarn, "connect", "cannot connect to the hub", "name", t.Name, "err", err)
	default:
		once.write(slog.LevelWarn, "closed", "the hub closed the connection", "name", t.Name, "err", err)
	}
}

// sleep waits d or until ctx ends. It returns false when ctx ended.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
