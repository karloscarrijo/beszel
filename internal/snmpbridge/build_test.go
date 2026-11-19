package snmpbridge

import (
	"context"
	"log/slog"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const uptimeRow = "\n1.3.6.1.2.1.1.3.0|67|123456\n"

// cgwProfile has the table rules, cpu and memory of the CGW-Fiber Profile.
func cgwProfile() Profile {
	return Profile{
		Tables: []TableRule{
			{OID: ".1.3.6.1.4.1.2021.13.16.2.1", NameColumn: 2, ValueColumn: 3, Kind: KindTemperature, Scale: 1000, Exclude: regexp.MustCompile(`^temp3$`)},
			{OID: ".1.3.6.1.4.1.2021.13.16.3.1", NameColumn: 2, ValueColumn: 3, Kind: KindFan, Scale: 1, Exclude: regexp.MustCompile(`^fan2$`)},
		},
		CPU:    SourceUCD,
		Memory: SourceUCD,
	}
}

func cgwSource(t *testing.T, p Profile, extra string) *TableSource {
	t.Helper()
	return &TableSource{
		Target:  Target{ID: "cgw-1", Name: "CGW"},
		Profile: p,
		Reader:  fake(t, mustRead(t, "testdata/cgw-fiber.snmprec")+extra),
		Logger:  slog.New(slog.NewTextHandler(&discard{}, nil)),
	}
}

type discard struct{}

func (*discard) Write(p []byte) (int, error) { return len(p), nil }

// Guard: a Profile with `cpu: hr` and `memory: ucd` reads the recorded U6+.
func TestReadHRWithRecordedU6Plus(t *testing.T) {
	src := &TableSource{
		Target:  Target{ID: "u6-1", Name: "U6+"},
		Profile: Profile{CPU: SourceHR, Memory: SourceUCD},
		Reader:  fake(t, mustRead(t, "testdata/u6-plus.snmprec")),
		Logger:  slog.New(slog.NewTextHandler(&discard{}, nil)),
	}
	data, err := src.Read(context.Background(), common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1.0, data.Stats.Cpu)
	assert.Equal(t, 46.22, data.Stats.MemPct)
	assert.Equal(t, [4]uint64{230092 * 1024, 106340 * 1024, 43256 * 1024, 0}, data.Stats.MemBytes)
}

// Guard: the Memory chart of the hub reads the bytes from MemBytes, and the
// info bar reads Info. A Target that fills only the percent shows an empty
// chart.
func TestReadFillsCPUMemoryAndInfoFields(t *testing.T) {
	data, err := cgwSource(t, cgwProfile(), "").Read(context.Background(), common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Equal(t, 16.0, data.Stats.Cpu) // ssCpuIdle 84
	assert.Equal(t, 52.8, data.Stats.MemPct)
	// kB * 1024: total, used (total - avail - buffer - cached), buffer + cached.
	assert.Equal(t, [4]uint64{2960768 * 1024, 1563268 * 1024, (209844 + 997908) * 1024, 0}, data.Stats.MemBytes)
	assert.Equal(t, data.Stats.Cpu, data.Info.Cpu)
	assert.Equal(t, data.Stats.MemPct, data.Info.MemPct)
	// One reply holds the table data too.
	assert.Equal(t, map[string]uint16{"fan-CPU": 2031}, data.Stats.Fans)
	assert.Len(t, data.Stats.Temperatures, 4)
}

// failUptime fails only the Get of sysUpTime.
type failUptime struct{ Reader }

func (f failUptime) Get(ctx context.Context, oids []string) ([]Value, error) {
	if len(oids) == 1 && oids[0] == oidSysUpTime {
		return nil, &ReadError{Target: "cgw-1", Op: "get", OID: oids[0]}
	}
	return f.Reader.Get(ctx, oids)
}

// Guard: the uptime is not a rule. A missing value or a failed read must give
// 0 and must not set the Target down.
func TestUptimeIsInSecondsAndNeverFailsTheRead(t *testing.T) {
	data, err := cgwSource(t, cgwProfile(), uptimeRow).Read(context.Background(), common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Equal(t, uint64(1234), data.Info.Uptime) // 123456 / 100

	data, err = cgwSource(t, cgwProfile(), "").Read(context.Background(), common.DataRequestOptions{})
	require.NoError(t, err, "no sysUpTime")
	assert.Zero(t, data.Info.Uptime)

	s := cgwSource(t, cgwProfile(), uptimeRow)
	s.Reader = failUptime{s.Reader}
	data, err = s.Read(context.Background(), common.DataRequestOptions{})
	require.NoError(t, err, "failed uptime read")
	assert.Zero(t, data.Info.Uptime)
	assert.Equal(t, 16.0, data.Stats.Cpu)
}

// hangUptime blocks the Get of sysUpTime until its context ends.
type hangUptime struct{ Reader }

func (h hangUptime) Get(ctx context.Context, oids []string) ([]Value, error) {
	if len(oids) == 1 && oids[0] == oidSysUpTime {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return h.Reader.Get(ctx, oids)
}

// Guard: a sysUpTime that does not answer must not push the read past the
// deadline of the scheduler. The scheduler would drop all data and set the
// Target down.
func TestSilentUptimeDoesNotUseTheDeadline(t *testing.T) {
	s := cgwSource(t, cgwProfile(), uptimeRow)
	s.Reader = hangUptime{s.Reader}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second) // less than uptimeTimeout
	defer cancel()
	start := time.Now()
	data, err := s.Read(ctx, common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Less(t, time.Since(start), time.Second)
	assert.Zero(t, data.Info.Uptime)
	assert.Equal(t, 16.0, data.Stats.Cpu)
}

// Guard: a negative buffer value must not make the percent and the bytes
// disagree.
func TestNegativeBufferCountsAsZero(t *testing.T) {
	text := "1.3.6.1.4.1.2021.4.5.0|2|1000\n1.3.6.1.4.1.2021.4.6.0|2|300\n1.3.6.1.4.1.2021.4.14.0|2|-100\n"
	mem, ok, err := MemoryUCD(context.Background(), fake(t, text))
	require.NoError(t, err)
	require.True(t, ok)
	assert.InDelta(t, 70, mem.Percent, 1e-9)
	assert.Equal(t, [3]uint64{1024000, 716800, 0}, [3]uint64{mem.Total, mem.Used, mem.BuffCache})
}

// Guard: a Profile with no cpu or memory source gives 0 in these fields and
// the read does not fail.
func TestProfileWithOnlyTablesSendsZeroCPUAndMemory(t *testing.T) {
	p := cgwProfile()
	p.CPU, p.Memory = "", ""
	data, err := cgwSource(t, p, uptimeRow).Read(context.Background(), common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Zero(t, data.Stats.Cpu)
	assert.Zero(t, data.Stats.MemPct)
	assert.Zero(t, data.Stats.MemBytes)
	assert.Zero(t, data.Info.Cpu)
	assert.Zero(t, data.Info.MemPct)
	assert.Equal(t, map[string]uint16{"fan-CPU": 2031}, data.Stats.Fans)
}

// deadlineReader keeps the deadline of the context of each call.
type deadlineReader struct {
	Reader
	mu        sync.Mutex
	deadlines []time.Time
	missing   int
}

func (d *deadlineReader) note(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		d.deadlines = append(d.deadlines, dl)
	} else {
		d.missing++
	}
}

func (d *deadlineReader) Get(ctx context.Context, oids []string) ([]Value, error) {
	d.note(ctx)
	return d.Reader.Get(ctx, oids)
}

func (d *deadlineReader) Walk(ctx context.Context, oid string) ([]Value, error) {
	d.note(ctx)
	return d.Reader.Walk(ctx, oid)
}

// Guard: the command must give each scheduler the logger of the bridge. With
// no logger, a failed read leaves no reason in the log.
func TestNewBridgeLogsAFailedReadWithTheTarget(t *testing.T) {
	cfg := &Config{
		Profiles: map[string]Profile{"cgw": cgwProfile()},
		Targets:  []TargetConfig{{Target: Target{ID: "cgw-1", Name: "CGW"}, Profile: "cgw"}},
	}
	var buf syncBuffer
	b := NewBridge(cfg, &Settings{}, func(TargetConfig) Reader {
		return &FakeReader{Err: true, TargetID: "cgw-1"}
	}, slog.New(slog.NewTextHandler(&buf, nil)))
	_, err := b.Source(b.Targets[0]).Data(common.DataRequestOptions{})
	require.Error(t, err)
	assert.Contains(t, buf.String(), "read failed")
	assert.Contains(t, buf.String(), "target=cgw-1")
}

// Guard: the command must give each Target its own data, through the
// scheduler. A Target that gets the stub data, or a read with no deadline,
// shows wrong data or blocks for a Target that does not answer.
func TestNewBridgeGivesEachTargetOwnDataWithTheDeadlineOfTheScheduler(t *testing.T) {
	cfg := &Config{
		Profiles: map[string]Profile{
			"cgw": cgwProfile(),
			"tab": {Tables: []TableRule{rule(KindFan)}},
		},
		Targets: []TargetConfig{
			{Target: Target{ID: "cgw-1", Name: "CGW"}, Profile: "cgw"},
			{Target: Target{ID: "sw-2", Name: "Switch"}, Profile: "tab"},
		},
	}
	readers := map[string]*deadlineReader{
		"cgw-1": {Reader: fake(t, mustRead(t, "testdata/cgw-fiber.snmprec"))},
		"sw-2":  {Reader: fake(t, tableText(num("1", "FANX", 4321)))},
	}
	b := NewBridge(cfg, &Settings{HubURL: "http://hub.example", Token: "tok"}, func(tc TargetConfig) Reader {
		return readers[tc.ID]
	}, slog.New(slog.NewTextHandler(&discard{}, nil)))
	require.Len(t, b.Targets, 2)
	assert.Equal(t, "http://hub.example", b.HubURL)

	src := map[string]Source{}
	for _, tg := range b.Targets {
		src[tg.ID] = b.Source(tg)
	}
	before := time.Now()
	cgw, err := src["cgw-1"].Data(common.DataRequestOptions{IncludeDetails: true})
	require.NoError(t, err)
	sw, err := src["sw-2"].Data(common.DataRequestOptions{IncludeDetails: true})
	require.NoError(t, err)

	assert.Equal(t, "CGW", cgw.Details.Hostname)
	assert.Equal(t, map[string]uint16{"fan-CPU": 2031}, cgw.Stats.Fans)
	assert.Equal(t, 16.0, cgw.Stats.Cpu)
	assert.Equal(t, "Switch", sw.Details.Hostname)
	assert.Equal(t, map[string]uint16{"FANX": 4321}, sw.Stats.Fans)
	assert.Zero(t, sw.Stats.Cpu)

	for id, r := range readers {
		require.NotEmpty(t, r.deadlines, id)
		assert.Zero(t, r.missing, "%s: a call has a context with no deadline", id)
		// The uptime read has a shorter limit of its own. No call may have
		// a later deadline than the scheduler, and the table reads have it.
		limit := before.Add(ReadDeadline + 2*time.Second)
		fromScheduler := 0
		for _, dl := range r.deadlines {
			assert.False(t, dl.After(limit), id)
			if dl.After(before.Add(ReadDeadline - 2*time.Second)) {
				fromScheduler++
			}
		}
		assert.Positive(t, fromScheduler, "%s: no call has the deadline of the scheduler", id)
	}
	_, isGate := src["cgw-1"].(readGate)
	assert.True(t, isGate, "the source holds the state of the last read")
}
