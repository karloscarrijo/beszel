package snmpbridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fake(t *testing.T, text string) *FakeReader {
	t.Helper()
	f, err := NewFakeReader(text)
	require.NoError(t, err)
	f.TargetID = "sw-1"
	return f
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestFakeReaderTypesAndWalk(t *testing.T) {
	f := fake(t, `
# a comment
1.3.6.1.2.1.1.5.0|4|switch-1
1.3.6.1.2.1.1.3.0|67|123456
1.3.6.1.4.1.99.1.1.2.10|4x|46616e2031
1.3.6.1.4.1.99.1.1.2.2|4|Fan 0
1.3.6.1.4.1.99.1.1.5.2|66|4200
1.3.6.1.2.1.2.2.1.10.1|65|77
1.3.6.1.2.1.31.1.1.1.6.1|70|88
1.3.6.1.2.1.1.7.0|2|-5
`)
	vals, err := f.Get(context.Background(), []string{".1.3.6.1.2.1.1.5.0", "1.3.6.1.2.1.1.3.0", ".1.3.6.1.2.1.1.7.0", ".1.3.6.1.9.9.9"})
	require.NoError(t, err)
	require.Len(t, vals, 4)
	assert.Equal(t, TypeOctetString, vals[0].Type)
	assert.Equal(t, "switch-1", vals[0].String())
	assert.Equal(t, TypeTimeTicks, vals[1].Type)
	assert.EqualValues(t, 123456, vals[1].Int)
	assert.Equal(t, TypeInteger, vals[2].Type)
	assert.EqualValues(t, -5, vals[2].Int)
	assert.Equal(t, TypeNoSuch, vals[3].Type)

	walk, err := f.Walk(context.Background(), "1.3.6.1.4.1.99.1.1")
	require.NoError(t, err)
	require.Len(t, walk, 3)
	// OID order is numeric: .2.2 comes before .2.10
	assert.Equal(t, ".1.3.6.1.4.1.99.1.1.2.2", walk[0].OID)
	assert.Equal(t, "Fan 1", walk[1].String(), "a hex value gives its bytes")
	assert.Equal(t, TypeGauge, walk[2].Type)

	c, _ := f.Get(context.Background(), []string{".1.3.6.1.2.1.2.2.1.10.1", ".1.3.6.1.2.1.31.1.1.1.6.1"})
	assert.Equal(t, TypeCounter, c[0].Type)
	assert.Equal(t, TypeCounter, c[1].Type)
}

func TestFakeReaderBadText(t *testing.T) {
	for _, text := range []string{"1.3|2", "1.3|2|x", "1.3|4x|zz", "1.3|99|1"} {
		_, err := NewFakeReader(text)
		assert.Error(t, err, text)
	}
}

func TestReadErrorIsFixedText(t *testing.T) {
	f := fake(t, "")
	f.Err = true
	_, err := f.Get(context.Background(), []string{".1.3.6.1.4.1.2021.11.11.0"})
	var re *ReadError
	require.True(t, errors.As(err, &re))
	assert.Equal(t, `snmp get of target "sw-1" at .1.3.6.1.4.1.2021.11.11.0 failed`, err.Error())
}

// A real read against a port with no answer. The error must not hold the
// community or the target string of gosnmp.
func TestGoSNMPReaderErrorHidesCommunity(t *testing.T) {
	const secret = "s3cret-community"
	r := &GoSNMPReader{TargetID: "sw-1", Host: "127.0.0.1", Port: 1, Community: secret}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := r.Get(ctx, []string{".1.3.6.1.2.1.1.5.0"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.NotContains(t, err.Error(), "127.0.0.1")
	assert.Contains(t, err.Error(), `target "sw-1"`)
	assert.Contains(t, err.Error(), ".1.3.6.1.2.1.1.5.0")

	_, err = r.Walk(ctx, ".1.3.6.1.2.1.2.2.1")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.Contains(t, err.Error(), ".1.3.6.1.2.1.2.2.1")

	// a format verb and a log line hide the community of the reader
	var buf strings.Builder
	slog.New(slog.NewTextHandler(&buf, nil)).Info("reader", "r", r)
	for _, s := range []string{fmt.Sprintf("%v", r), fmt.Sprintf("%+v", *r), fmt.Sprintf("%#v", r), buf.String()} {
		assert.NotContains(t, s, secret)
	}
}

// A read that passes the context deadline keeps the deadline as its cause.
func TestGoSNMPReaderKeepsDeadlineCause(t *testing.T) {
	// A UDP socket that never answers.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port
	r := &GoSNMPReader{TargetID: "sw-1", Host: "127.0.0.1", Port: port, Community: "c"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = r.Get(ctx, []string{".1.3.6.1.2.1.1.5.0"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), err.Error())
}

func TestCPUUCD(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		text string
		want float64
		ok   bool
	}{
		{"idle 90", "1.3.6.1.4.1.2021.11.11.0|2|90", 10, true},
		{"idle 0", "1.3.6.1.4.1.2021.11.11.0|2|0", 100, true},
		{"idle above 100 is limited", "1.3.6.1.4.1.2021.11.11.0|2|120", 0, true},
		{"idle below 0 is limited", "1.3.6.1.4.1.2021.11.11.0|2|-20", 100, true},
		{"missing", "1.3.6.1.4.1.2021.11.1.0|2|1", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := CPUUCD(ctx, fake(t, tc.text))
			require.NoError(t, err)
			assert.Equal(t, tc.ok, ok)
			assert.InDelta(t, tc.want, got, 1e-9)
		})
	}
	f := fake(t, "")
	f.Err = true
	_, _, err := CPUUCD(ctx, f)
	assert.Error(t, err)
}

func TestMemoryUCD(t *testing.T) {
	ctx := context.Background()
	mem := func(total, avail, buf, cached string) string {
		s := ""
		for oid, v := range map[string]string{"5": total, "6": avail, "14": buf, "15": cached} {
			if v != "" {
				s += "1.3.6.1.4.1.2021.4." + oid + ".0|2|" + v + "\n"
			}
		}
		return s
	}
	tests := []struct {
		name  string
		text  string
		want  float64
		ok    bool
		bytes [3]uint64 // total, used, buffer + cached
	}{
		{"formula", mem("1000", "300", "100", "200"), 40, true, [3]uint64{1024000, 409600, 307200}},
		{"missing buffer and cached count as 0", mem("1000", "300", "", ""), 70, true, [3]uint64{1024000, 716800, 0}},
		{"zero total", mem("0", "0", "0", "0"), 0, false, [3]uint64{}},
		{"missing total", mem("", "300", "100", "200"), 0, false, [3]uint64{}},
		{"missing avail", mem("1000", "", "100", "200"), 0, false, [3]uint64{}},
		{"used below 0", mem("1000", "800", "200", "200"), 0, false, [3]uint64{}},
		{"used exactly 0", mem("1000", "800", "100", "100"), 0, true, [3]uint64{1024000, 0, 204800}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := MemoryUCD(ctx, fake(t, tc.text))
			require.NoError(t, err)
			assert.Equal(t, tc.ok, ok)
			assert.InDelta(t, tc.want, got.Percent, 1e-9)
			if ok {
				// The UCD values are in kB: the bytes are 1024 times more.
				assert.Equal(t, tc.bytes, [3]uint64{got.Total, got.Used, got.BuffCache})
			}
		})
	}
}

// Guard: `cpu: hr` gives the mean of the numeric hrProcessorLoad rows, limited
// to 0 to 100, and gives no data (not an error) with no numeric row.
func TestCPUHR(t *testing.T) {
	const base = "1.3.6.1.2.1.25.3.3.1.2."
	tests := []struct {
		name string
		text string
		want float64
		ok   bool
	}{
		{"mean of rows", base + "1|2|10\n" + base + "2|2|20\n" + base + "3|2|30\n", 20, true},
		{"row above 100", base + "1|2|10\n" + base + "2|2|20\n" + base + "3|2|150\n", 60, true},
		{"mean limited to 100", base + "1|2|150\n" + base + "2|2|200\n", 100, true},
		{"row that is not a number is skipped", base + "1|2|40\n" + base + "2|4|abc\n", 40, true},
		{"no row", "1.3.6.1.2.1.1.5.0|4|x\n", 0, false},
		{"no numeric row", base + "1|4|abc\n", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := CPUHR(context.Background(), fake(t, tc.text))
			require.NoError(t, err)
			assert.Equal(t, tc.ok, ok)
			assert.InDelta(t, tc.want, got, 1e-9)
		})
	}
}

// The recorded data of the real Targets give a CPU and memory value in range.
func TestUCDWithRecordedData(t *testing.T) {
	text := mustRead(t, "testdata/cgw-fiber.snmprec")
	f := fake(t, text)
	cpu, ok, err := CPUUCD(context.Background(), f)
	require.NoError(t, err)
	require.True(t, ok)
	assert.InDelta(t, 16, cpu, 1e-9) // ssCpuIdle 84
	mem, ok, err := MemoryUCD(context.Background(), f)
	require.NoError(t, err)
	require.True(t, ok)
	// (2960768 - 189748 - 209844 - 997908) / 2960768
	assert.InDelta(t, 52.8, mem.Percent, 0.1)
}
