package snmpbridge

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/henrygd/beszel/internal/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testEntry = ".1.3.6.1.4.1.99.1.1"

// row is one table row for tableText. typ is the snmprec type: "4" for text
// and "2" for an integer.
type row struct{ index, name, typ, value string }

// tableText makes .snmprec text with the name column 2 and the value column 5
// below testEntry. Tests change the rows in code, so they need no new file.
func tableText(rows ...row) string {
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%s.2.%s|4|%s\n", strings.TrimPrefix(testEntry, "."), r.index, r.name)
		fmt.Fprintf(&b, "%s.5.%s|%s|%s\n", strings.TrimPrefix(testEntry, "."), r.index, r.typ, r.value)
	}
	return b.String()
}

func txt(index, name, value string) row { return row{index, name, "4", value} }
func num(index, name string, value int) row {
	return row{index, name, "2", fmt.Sprint(value)}
}

func rule(kind string) TableRule {
	return TableRule{OID: testEntry, NameColumn: 2, ValueColumn: 5, Kind: kind, Scale: 1}
}

// newSource makes a TableSource with a log buffer. Each log line is one line
// of text.
func newSource(t *testing.T, text string, rules ...TableRule) (*TableSource, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return &TableSource{
		Target:  Target{ID: "sw-1", Name: "Switch 1"},
		Profile: Profile{Tables: rules},
		Reader:  fake(t, text),
		Logger:  slog.New(slog.NewTextHandler(&buf, nil)),
	}, &buf
}

func read(t *testing.T, s *TableSource) (fans map[string]uint16, temps map[string]float64) {
	t.Helper()
	data, err := s.Data(common.DataRequestOptions{})
	require.NoError(t, err)
	return data.Stats.Fans, data.Stats.Temperatures
}

func count(buf *bytes.Buffer, msg string) int { return strings.Count(buf.String(), msg) }

func TestRecordedFilesGiveFansAndTemperatures(t *testing.T) {
	nasTemp := rule(KindTemperature)
	nasTemp.OID, nasTemp.NameColumn, nasTemp.ValueColumn = ".1.3.6.1.4.1.21317.1.3.1", 13, 2
	nasTemp.Match = regexp.MustCompile(`Temp$`)
	nasTemp.Exclude = regexp.MustCompile(`^(CPU2|Vcpu2VRM|P[12]-DIMM[C-H]|VmemEFVRM|VmemGHVRM)`)
	nasFan := nasTemp
	nasFan.Kind, nasFan.Match, nasFan.Exclude = KindFan, regexp.MustCompile(`^FAN`), regexp.MustCompile(`^FAN(1|3|4|A|B)$`)

	nas, _ := newSource(t, mustRead(t, "testdata/nas-bmc.snmprec"), nasTemp, nasFan)
	fans, temps := read(t, nas)
	assert.Equal(t, map[string]uint16{"FAN2": 800, "FAN5": 800, "FAN6": 800}, fans)
	assert.Equal(t, map[string]float64{
		"CPU1 Temp": 42, "PCH Temp": 59, "System Temp": 42, "Peripheral Temp": 44,
		"Vcpu1VRM Temp": 71, "VmemABVRM Temp": 43, "VmemCDVRM Temp": 47,
		"P1-DIMMA1 Temp": 43, "P1-DIMMB1 Temp": 42,
	}, temps)

	cgwTemp := TableRule{OID: ".1.3.6.1.4.1.2021.13.16.2.1", NameColumn: 2, ValueColumn: 3, Kind: KindTemperature, Scale: 1000, Exclude: regexp.MustCompile(`^temp3$`)}
	cgwFan := TableRule{OID: ".1.3.6.1.4.1.2021.13.16.3.1", NameColumn: 2, ValueColumn: 3, Kind: KindFan, Scale: 1, Exclude: regexp.MustCompile(`^fan2$`)}
	cgw, _ := newSource(t, mustRead(t, "testdata/cgw-fiber.snmprec"), cgwTemp, cgwFan)
	cgw.Profile.CPU, cgw.Profile.Memory = SourceUCD, SourceUCD
	data, err := cgw.Data(common.DataRequestOptions{IncludeDetails: true})
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"temp-CPU": 60.562, "temp-Local": 57.437, "temp-PMIC": 64.25, "temp-cpu": 65.3}, data.Stats.Temperatures)
	assert.Equal(t, map[string]uint16{"fan-CPU": 2031}, data.Stats.Fans)
	assert.Equal(t, "Switch 1", data.Details.Hostname)
	assert.NotZero(t, data.Stats.Cpu)
}

func TestMovedIndexKeepsNameAndGoneSensorGivesWarning(t *testing.T) {
	s, buf := newSource(t, tableText(num("1", "FAN1", 1200), num("2", "FAN2", 900)), rule(KindFan))
	fans, _ := read(t, s)
	assert.Equal(t, map[string]uint16{"FAN1": 1200, "FAN2": 900}, fans)

	// The indexes move: each name keeps its value.
	s.Reader = fake(t, tableText(num("7", "FAN2", 905), num("9", "FAN1", 1205)))
	fans, _ = read(t, s)
	assert.Equal(t, map[string]uint16{"FAN1": 1205, "FAN2": 905}, fans)
	assert.Zero(t, count(buf, "sensor is gone"))

	// FAN1 is gone. FAN2 must not get the value of FAN1, and FAN1 has no key.
	s.Reader = fake(t, tableText(num("1", "FAN2", 910)))
	for range 3 {
		fans, _ = read(t, s)
		assert.Equal(t, map[string]uint16{"FAN2": 910}, fans)
	}
	assert.Equal(t, 1, count(buf, "sensor is gone"), buf.String())
	assert.Contains(t, buf.String(), "name=FAN1")

	// It comes back: one log line.
	s.Reader = fake(t, tableText(num("1", "FAN1", 1210), num("2", "FAN2", 910)))
	read(t, s)
	read(t, s)
	assert.Equal(t, 1, count(buf, "sensor is back"))
}

func TestStringValuesScaleExcludeAndBadValues(t *testing.T) {
	text := tableText(
		txt("1", "FAN1", "700.000"),
		txt("2", "FAN2", "0"),
		txt("3", "FAN3", "abc"),
		txt("4", "FAN4", ""),
		txt("5", "FAN5", "1e400"),
	)
	r := rule(KindFan)
	r.Exclude = regexp.MustCompile(`FAN2`)
	s, _ := newSource(t, text, r)
	fans, _ := read(t, s)
	assert.Equal(t, map[string]uint16{"FAN1": 700}, fans)

	// Milli-degrees, and a false 127.5 degree sensor that exclude removes.
	tr := rule(KindTemperature)
	tr.Scale = 1000
	tr.Exclude = regexp.MustCompile(`^Fake`)
	s, _ = newSource(t, tableText(
		num("1", "CPU", 45500),
		txt("2", "Board", "38250.000"),
		num("3", "Fake", 127500),
		txt("4", "Bad", "n/a"),
	), tr)
	_, temps := read(t, s)
	assert.Equal(t, map[string]float64{"CPU": 45.5, "Board": 38.25}, temps)

	// The match is a search, not an anchored match.
	tr = rule(KindTemperature)
	tr.Match = regexp.MustCompile(`PU`)
	s, _ = newSource(t, tableText(num("1", "CPU1", 40), num("2", "Board", 30)), tr)
	_, temps = read(t, s)
	assert.Equal(t, map[string]float64{"CPU1": 40}, temps)
}

func TestRepeatedNameGetsSuffixAndOneWarning(t *testing.T) {
	s, buf := newSource(t, tableText(num("1", "FAN1", 100), num("2", "FAN1", 200), num("3", "FAN1", 300), num("4", "FAN2", 400)), rule(KindFan))
	for range 3 {
		fans, _ := read(t, s)
		assert.Equal(t, map[string]uint16{"FAN1": 100, "FAN1 #2": 200, "FAN1 #3": 300, "FAN2": 400}, fans)
	}
	assert.Equal(t, 1, count(buf, "repeated sensor name"), buf.String())
}

func TestRepeatedNameAcrossRulesOfOneKind(t *testing.T) {
	r1, r2 := rule(KindFan), rule(KindFan)
	r2.NameColumn = 3 // a second name column with the same names as column 2
	text := tableText(num("1", "FAN1", 100), num("2", "FAN2", 200))
	text += "1.3.6.1.4.1.99.1.1.3.1|4|FAN1\n1.3.6.1.4.1.99.1.1.3.2|4|FAN2\n"
	s, buf := newSource(t, text, r1, r2)
	fans, _ := read(t, s)
	assert.Equal(t, map[string]uint16{"FAN1": 100, "FAN2": 200, "FAN1 #2": 100, "FAN2 #2": 200}, fans)
	assert.Equal(t, 2, count(buf, "repeated sensor name"), buf.String())
}

func TestPaddedNamesMatchAnchoredExpressions(t *testing.T) {
	r := rule(KindFan)
	r.Exclude = regexp.MustCompile(`^FAN1$`)
	s, _ := newSource(t, tableText(num("1", "FAN1\x00", 0), num("2", "FAN2 ", 800)), r)
	fans, _ := read(t, s)
	assert.Equal(t, map[string]uint16{"FAN2": 800}, fans)
}

// countReader counts the Walk calls of each OID.
type countReader struct {
	Reader
	walks map[string]int
}

func (c *countReader) Walk(ctx context.Context, oid string) ([]Value, error) {
	c.walks[oid]++
	return c.Reader.Walk(ctx, oid)
}

func TestRulesOfOneTableWalkItOnce(t *testing.T) {
	fan, temp := rule(KindFan), rule(KindTemperature)
	fan.Match, temp.Match = regexp.MustCompile(`^FAN`), regexp.MustCompile(`^CPU`)
	s, _ := newSource(t, tableText(num("1", "FAN1", 100), num("2", "CPU", 40)), fan, temp)
	cr := &countReader{Reader: s.Reader, walks: map[string]int{}}
	s.Reader = cr
	fans, temps := read(t, s)
	assert.Equal(t, map[string]uint16{"FAN1": 100}, fans)
	assert.Equal(t, map[string]float64{"CPU": 40}, temps)
	assert.Equal(t, map[string]int{testEntry: 1}, cr.walks)
}

func TestFailedWalkOfOneRuleWarnsOnceAndOthersGiveData(t *testing.T) {
	good, bad := rule(KindTemperature), rule(KindFan)
	bad.OID = ".1.3.6.1.4.1.77.1"
	s, buf := newSource(t, tableText(num("1", "CPU", 40)), good, bad)
	s.Reader = &failWalk{Reader: s.Reader, oid: bad.OID}
	for range 3 {
		_, temps := read(t, s)
		assert.Equal(t, map[string]float64{"CPU": 40}, temps)
	}
	assert.Equal(t, 1, count(buf, "cannot read its table"), buf.String())
	assert.Contains(t, buf.String(), "rule=2")
}

// failWalk fails the Walk of one OID.
type failWalk struct {
	Reader
	oid string
}

func (f *failWalk) Walk(ctx context.Context, oid string) ([]Value, error) {
	if oid == f.oid {
		return nil, &ReadError{Target: "sw-1", Op: "walk", OID: oid}
	}
	return f.Reader.Walk(ctx, oid)
}

func TestValueOutOfRangeGivesNoDataPoint(t *testing.T) {
	s, _ := newSource(t, tableText(
		num("1", "A", 65535),
		num("2", "B", 65536),
		num("3", "C", 70000),
		num("4", "D", -1),
		num("5", "E", 0),
		txt("6", "F", "12.5"),
		txt("7", "G", "-0.5"),
	), rule(KindFan))
	fans, _ := read(t, s)
	assert.Equal(t, map[string]uint16{"A": 65535, "E": 0}, fans)

	// A temperature must be finite, also after the scale.
	tr := rule(KindTemperature)
	tr.Scale = 1e-320
	s, _ = newSource(t, tableText(txt("1", "Inf", "1e300"), txt("2", "NaN", "NaN"), txt("3", "Pinf", "Inf"), num("4", "Ok", 0)), tr)
	_, temps := read(t, s)
	assert.Equal(t, map[string]float64{"Ok": 0}, temps)
	for _, v := range temps {
		assert.False(t, math.IsInf(v, 0))
	}
}

func TestZeroRowsWarnsOnceAndOtherRulesGiveData(t *testing.T) {
	temp := rule(KindTemperature)
	temp.Match = regexp.MustCompile(`^CPU`)
	fan := rule(KindFan)
	fan.Match = regexp.MustCompile(`^FAN`)
	s, buf := newSource(t, tableText(num("1", "CPU", 40)), temp, fan)
	for range 3 {
		_, temps := read(t, s)
		assert.Equal(t, map[string]float64{"CPU": 40}, temps)
	}
	assert.Equal(t, 1, count(buf, "table rule gives no data"), buf.String())
	assert.Contains(t, buf.String(), "target=sw-1")
	assert.Contains(t, buf.String(), "rule=2")

	// The rule gets data, then 0 rows again: one more warning.
	s.Reader = fake(t, tableText(num("1", "CPU", 40), num("2", "FAN1", 900)))
	fans, _ := read(t, s)
	assert.Equal(t, map[string]uint16{"FAN1": 900}, fans)
	assert.Equal(t, 1, count(buf, "gives data again"))
	s.Reader = fake(t, tableText(num("1", "CPU", 40)))
	read(t, s)
	read(t, s)
	assert.Equal(t, 2, count(buf, "table rule gives no data"))
	// A rule with 0 rows does not make a warning for each name that it gave.
	assert.Zero(t, count(buf, "sensor is gone"))
}

func TestNoRuleGivesDataFailsTheRead(t *testing.T) {
	fan := rule(KindFan)
	s, buf := newSource(t, tableText(num("1", "CPU", 40)), fan)
	fan.Match = regexp.MustCompile(`^FAN`)
	s.Profile.Tables = []TableRule{fan}
	_, err := s.Data(common.DataRequestOptions{})
	assert.ErrorIs(t, err, ErrNoData)
	assert.Equal(t, 1, count(buf, "table rule gives no data"))

	// A cpu source with no data counts with the table rules.
	s.Profile.CPU = SourceUCD
	_, err = s.Data(common.DataRequestOptions{})
	assert.ErrorIs(t, err, ErrNoData)

	// A reader error also fails the read, and it is not a "0 rows" state.
	s.Reader.(*FakeReader).Err = true
	_, err = s.Data(common.DataRequestOptions{})
	var re *ReadError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, 1, count(buf, "table rule gives no data"))
}
