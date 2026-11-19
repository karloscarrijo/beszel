package snmpbridge

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Guard: a build with no http*.go files stops at the start for a url Target or
// an http Profile. The test runs only in that build (scripts/check-snmpbridge-nohttp.sh).
func TestNoHTTPSourceStopsTheStart(t *testing.T) {
	if checkHTTPProfile != nil || checkHTTPTarget != nil || newHTTPRead != nil {
		t.Skip("this build has the HTTP source")
	}
	_, err := ParseConfig([]byte("profiles:\n  d:\n    http:\n      cpu: a\ntargets:\n  - id: a\n    name: A\n    profile: d\n    url: http://192.0.2.1/\n"))
	require.ErrorContains(t, err, "this build has no HTTP source")
	_, err = ParseConfig([]byte("profiles:\n  p:\n    cpu: ucd\ntargets:\n  - id: a\n    name: A\n    profile: p\n    url: http://192.0.2.1/\n"))
	require.ErrorContains(t, err, "this build has no HTTP source")
}
