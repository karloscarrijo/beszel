package snmpbridge

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Guard: a longer probe or backoff makes a Status alert of 1 minute fire on one failed read (beszel#026).
func TestTimingConstantsKeepStatusAlertQuiet(t *testing.T) {
	assert.Equal(t, 30*time.Second, ProbeInterval)
	assert.Less(t, ProbeInterval+ReadDeadline, time.Minute)
	assert.GreaterOrEqual(t, ProbeInterval, MinReadGap, "each probe must start a new read")
	assert.Equal(t, 5*time.Second, DefaultBackoffInitial)
	assert.Equal(t, 10*time.Second, DefaultBackoffMax)
}
