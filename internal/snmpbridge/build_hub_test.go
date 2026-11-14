//go:build testing

package snmpbridge

import (
	"log/slog"
	"testing"

	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Guard: a Target with the recorded data must reach the hub with CPU, memory,
// the bytes of the Memory chart and the table data in one record. This is
// the main path "a Target shows in the hub as a system, with fans and
// temperatures".
func TestHubTargetGetsCPUMemoryFansAndTemperatures(t *testing.T) {
	h := newTestHub(t)
	cfg := &Config{
		Profiles: map[string]Profile{"cgw": cgwProfile()},
		Targets:  []TargetConfig{{Target: alpha, Profile: "cgw"}},
	}
	reader := fake(t, mustRead(t, "testdata/cgw-fiber.snmprec")+uptimeRow)
	b := NewBridge(cfg, &Settings{HubURL: h.url(), Token: permToken, Keys: h.keys}, func(TargetConfig) Reader { return reader }, slog.New(&logCapture{}))
	b.Backoff = Backoff{Initial: 20e6, Max: 80e6}
	tb := &testBridge{Bridge: b}
	tb.start(t)

	eventually(t, "Alpha up with stats", func() bool {
		return h.status("Alpha") == "up" && h.count("system_stats") >= 1
	})
	stats, err := h.hub.FindAllRecords("system_stats")
	require.NoError(t, err)
	var st system.Stats
	require.NoError(t, stats[0].UnmarshalJSONField("stats", &st))
	assert.Equal(t, 16.0, st.Cpu)
	assert.Equal(t, 52.8, st.MemPct)
	assert.Equal(t, [4]uint64{2960768 * 1024, 1563268 * 1024, (209844 + 997908) * 1024, 0}, st.MemBytes)
	assert.Equal(t, map[string]uint16{"fan-CPU": 2031}, st.Fans)
	assert.Equal(t, 60.562, st.Temperatures["temp-CPU"])

	systems, err := h.hub.FindAllRecords("systems")
	require.NoError(t, err)
	require.Len(t, systems, 1)
	var info system.Info
	require.NoError(t, systems[0].UnmarshalJSONField("info", &info))
	assert.Equal(t, 16.0, info.Cpu)
	assert.Equal(t, 52.8, info.MemPct)
	assert.Equal(t, uint64(1234), info.Uptime)
}
