//go:build testing

package alerts_test

import (
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/henrygd/beszel/internal/entities/system"
	beszelTests "github.com/henrygd/beszel/internal/tests"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fanAlertFixture struct {
	t      *testing.T
	hub    *beszelTests.TestHub
	system *core.Record
	alert  *core.Record
}

func newFanAlertFixture(t *testing.T, threshold, minutes int) *fanAlertFixture {
	t.Helper()
	hub, user := beszelTests.GetHubWithUser(t)
	t.Cleanup(hub.Cleanup)

	systems, err := beszelTests.CreateSystems(hub, 1, user.Id, "paused")
	require.NoError(t, err)

	alert, err := beszelTests.CreateRecord(hub, "alerts", map[string]any{
		"name":   "FanLow",
		"system": systems[0].Id,
		"user":   user.Id,
		"value":  threshold,
		"min":    minutes,
	})
	require.NoError(t, err)

	return &fanAlertFixture{t: t, hub: hub, system: systems[0], alert: alert}
}

// addStats stores a 1m stats record with the given fans, created at the given time.
func (f *fanAlertFixture) addStats(created time.Time, fans map[string]uint16) {
	f.t.Helper()
	statsJSON, err := json.Marshal(system.Stats{Cpu: 10, Fans: fans})
	require.NoError(f.t, err)
	record, err := beszelTests.CreateRecord(f.hub, "system_stats", map[string]any{
		"system": f.system.Id,
		"type":   "1m",
		"stats":  string(statsJSON),
	})
	require.NoError(f.t, err)
	record.SetRaw("created", created.Format(types.DefaultDateLayout))
	require.NoError(f.t, f.hub.SaveNoValidate(record))
}

// update runs the alert handler with the current fans and returns the triggered state.
func (f *fanAlertFixture) update(now time.Time, fans map[string]uint16) bool {
	f.t.Helper()
	f.system.Set("updated", now)
	data := &system.CombinedData{
		Stats: system.Stats{Cpu: 10, Fans: fans},
		Info:  system.Info{AgentVersion: "0.12.0", Cpu: 10},
	}
	am := f.hub.GetAlertManager()
	synctest.Test(f.t, func(t *testing.T) {
		require.NoError(t, am.HandleSystemAlerts(f.system, data))
	})
	alert, err := f.hub.FindFirstRecordByFilter("alerts", "id={:id}", dbx.Params{"id": f.alert.Id})
	require.NoError(f.t, err)
	return alert.GetBool("triggered")
}

func TestFanLowAlertCurrentValue(t *testing.T) {
	f := newFanAlertFixture(t, 500, 1)
	now := time.Now().UTC()

	assert.False(t, f.update(now, map[string]uint16{"cpu": 1200, "sys": 900}), "all fans above the limit")
	assert.True(t, f.update(now, map[string]uint16{"cpu": 1200, "sys": 300}), "the slowest fan is below the limit")
	assert.True(t, f.update(now, map[string]uint16{"cpu": 1200, "sys": 0}), "a stopped fan keeps the alert active")
	assert.False(t, f.update(now, map[string]uint16{"cpu": 1200, "sys": 800}), "the slowest fan is above the limit again")

	f2 := newFanAlertFixture(t, 500, 1)
	assert.True(t, f2.update(now, map[string]uint16{"cpu": 1200, "sys": 0}), "0 RPM fires the alert")
}

func TestFanLowAlertAveragesSlowestFanOfEachRecord(t *testing.T) {
	now := time.Now().UTC()

	// Slowest fans in the window: 400 and 700. The record with no fans is skipped,
	// so the average is 550, above the limit of 500.
	above := newFanAlertFixture(t, 500, 2)
	above.addStats(now.Add(-180*time.Second), map[string]uint16{"a": 0})
	above.addStats(now.Add(-90*time.Second), map[string]uint16{"a": 400, "b": 3000})
	above.addStats(now.Add(-60*time.Second), nil)
	above.addStats(now.Add(-30*time.Second), map[string]uint16{"a": 700, "b": 1000})
	assert.False(t, above.update(now, map[string]uint16{"a": 100}), "average of the slowest fans (550) is above the limit")

	// Slowest fans in the window: 300 and 600. The average is 450, below the limit.
	// An average of all fans of each record would give 875 and no alert.
	below := newFanAlertFixture(t, 500, 2)
	below.addStats(now.Add(-180*time.Second), map[string]uint16{"a": 5000})
	below.addStats(now.Add(-90*time.Second), map[string]uint16{"a": 300, "b": 2000})
	below.addStats(now.Add(-60*time.Second), nil)
	below.addStats(now.Add(-30*time.Second), map[string]uint16{"a": 600})
	assert.True(t, below.update(now, map[string]uint16{"a": 100}), "average of the slowest fans (450) is below the limit")
}

func TestFanLowAlertNoFanData(t *testing.T) {
	now := time.Now().UTC()

	current := newFanAlertFixture(t, 500, 1)
	assert.False(t, current.update(now, nil), "a system with no fans gets no alert")

	history := newFanAlertFixture(t, 500, 2)
	history.addStats(now.Add(-180*time.Second), nil)
	history.addStats(now.Add(-90*time.Second), nil)
	history.addStats(now.Add(-60*time.Second), nil)
	history.addStats(now.Add(-30*time.Second), nil)
	assert.False(t, history.update(now, map[string]uint16{"a": 100}), "no record in the window has fans")
}

func TestFanLowAlertNameAfterMigrations(t *testing.T) {
	hub, _ := beszelTests.GetHubWithUser(t)
	defer hub.Cleanup()

	collection, err := hub.FindCollectionByNameOrId("alerts")
	require.NoError(t, err)
	field := collection.Fields.GetByName("name").(*core.SelectField)
	assert.Contains(t, field.Values, "FanLow")
}
