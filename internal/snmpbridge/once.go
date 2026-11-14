package snmpbridge

import (
	"context"
	"log/slog"
	"sync"
)

// onceLog logs each kind of event one time for a Target, until reset is
// called. The bridge calls reset when the state of the Target changes (a good
// connection). So a Target that fails again and again writes one line, not one
// line for each attempt.
type onceLog struct {
	log  *slog.Logger
	mu   sync.Mutex
	seen map[string]struct{}
}

func (o *onceLog) write(level slog.Level, key, msg string, args ...any) {
	o.mu.Lock()
	if _, ok := o.seen[key]; ok {
		o.mu.Unlock()
		return
	}
	if o.seen == nil {
		o.seen = map[string]struct{}{}
	}
	o.seen[key] = struct{}{}
	o.mu.Unlock()
	o.log.Log(context.Background(), level, msg, args...)
}

func (o *onceLog) reset() {
	o.mu.Lock()
	o.seen = nil
	o.mu.Unlock()
}
