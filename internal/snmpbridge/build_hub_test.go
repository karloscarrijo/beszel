//go:build testing

package snmpbridge

import (
	"log/slog"
	"testing"

	"github.com/henrygd/beszel/internal/entities/system"
	beszelTests "github.com/henrygd/beszel/internal/tests"
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
	// Guard: the "Temp" column and the Temperature alert read this field.
	assert.Equal(t, 65.3, info.DashboardTemp)
}

// Guard: a Temperature alert on the system of a Target must trigger when the
// highest sensor is above the limit. The hub skips the alert when
// Info.DashboardTemp is below 1.
func TestHubTemperatureAlertTriggersForATarget(t *testing.T) {
	h := newTestHub(t)
	cfg := &Config{
		Profiles: map[string]Profile{"cgw": cgwProfile()},
		Targets:  []TargetConfig{{Target: alpha, Profile: "cgw"}},
	}
	reader := fake(t, mustRead(t, "testdata/cgw-fiber.snmprec")+uptimeRow)
	newBridge := func() *testBridge {
		b := NewBridge(cfg, &Settings{HubURL: h.url(), Token: permToken, Keys: h.keys}, func(TargetConfig) Reader { return reader }, slog.New(&logCapture{}))
		b.Backoff = Backoff{Initial: 20e6, Max: 80e6}
		return &testBridge{Bridge: b}
	}
	stop := newBridge().start(t)
	eventually(t, "Alpha up with stats", func() bool {
		return h.status("Alpha") == "up" && h.count("system_stats") >= 1
	})
	stop()

	systems, err := h.hub.FindAllRecords("systems")
	require.NoError(t, err)
	require.Len(t, systems, 1)
	// The highest temperature is 65.3, so a limit of 60 must trigger.
	alert, err := beszelTests.CreateRecord(h.hub, "alerts", map[string]any{
		"name": "Temperature", "system": systems[0].Id, "user": h.user.Id, "min": 1, "value": 60,
	})
	require.NoError(t, err)

	newBridge().start(t)
	eventually(t, "Temperature alert triggered", func() bool {
		rec, err := h.hub.FindRecordById("alerts", alert.Id)
		return err == nil && rec.GetBool("triggered")
	})
}
