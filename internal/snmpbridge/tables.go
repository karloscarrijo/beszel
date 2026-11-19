package snmpbridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/system"
)

// uptimeTimeout is the time limit of the sysUpTime read.
const uptimeTimeout = 3 * time.Second

// ErrNoData is the error of a read where no rule of the Profile gives data.
var ErrNoData = errors.New("no rule of the profile gives data")

// TableSource is a Source that reads one Target through a Reader, with the
// rules of its Profile.
type TableSource struct {
	Target  Target
	Profile Profile
	Reader  Reader
	Logger  *slog.Logger

	mu    sync.Mutex
	rules map[int]*ruleState
	dups  map[string]bool
}

var _ ReadFunc = (*TableSource)(nil).Read

// ruleState is what the bridge remembers of one table rule.
type ruleState struct {
	failed  bool
	known   bool
	hasData bool
	seen    map[string]bool
	gone    map[string]bool
}

// Read is a ReadFunc. It reads each rule of the Profile and gives ctx to each
// SNMP request. An error means that no rule gave data, so the hub sets the
// system down.
func (s *TableSource) Read(ctx context.Context, opts common.DataRequestOptions) (*system.CombinedData, error) {
	log := s.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("target", s.Target.ID)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rules == nil {
		s.rules = map[int]*ruleState{}
		s.dups = map[string]bool{}
	}

	stats := system.Stats{}
	gave := false // a rule gave data
	var firstErr error
	note := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	// why: 2 rules can walk the same table, and 2 rules of one kind can give
	// the same name. One walk for each OID and one name set for each kind
	// serve the whole read.
	walks := map[string]walkResult{}
	names := map[string]*nameSet{KindFan: newNameSet(), KindTemperature: newNameSet()}
	for i, rule := range s.Profile.Tables {
		points, rows, err := s.readRule(ctx, log, walks, names[rule.Kind], rule)
		if err != nil {
			// The rule did not read. Its state stays as it was: a read
			// error is not "0 rows". The bridge logs it one time for each
			// run of errors.
			if st := s.state(i); !st.failed {
				st.failed = true
				log.Warn("table rule cannot read its table", "rule", i+1, "oid", rule.OID, "kind", rule.Kind, "err", err)
			}
			note(err)
			continue
		}
		s.state(i).failed = false
		s.update(log, i, rule, points, rows)
		if len(points) == 0 {
			continue
		}
		gave = true
		switch rule.Kind {
		case KindFan:
			if stats.Fans == nil {
				stats.Fans = map[string]uint16{}
			}
			for name, v := range points {
				stats.Fans[name] = uint16(v)
			}
		case KindTemperature:
			if stats.Temperatures == nil {
				stats.Temperatures = map[string]float64{}
			}
			for name, v := range points {
				stats.Temperatures[name] = v
			}
		}
	}
	// A Profile with no cpu or memory source sends 0 in these fields.
	if s.Profile.CPU == SourceUCD || s.Profile.CPU == SourceHR {
		cpuSource := CPUUCD
		if s.Profile.CPU == SourceHR {
			cpuSource = CPUHR
		}
		pct, ok, err := cpuSource(ctx, s.Reader)
		switch {
		case err != nil:
			note(err)
		case ok:
			stats.Cpu, gave = twoDecimals(pct), true
		}
	}
	if s.Profile.Memory == SourceUCD {
		mem, ok, err := MemoryUCD(ctx, s.Reader)
		switch {
		case err != nil:
			note(err)
		case ok:
			stats.MemPct = twoDecimals(mem.Percent)
			stats.MemBytes = [4]uint64{mem.Total, mem.Used, mem.BuffCache, 0}
			gave = true
		}
	}
	if !gave {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, ErrNoData
	}

	info := system.Info{AgentVersion: beszel.Version, Cpu: stats.Cpu, MemPct: stats.MemPct}
	// why: the hub reads DashboardTemp for the Temperature alert and the Temp
	// column. The stock agent sets it to the highest sensor when it has no
	// PRIMARY_SENSOR. With no temperature the field stays 0.
	for _, v := range stats.Temperatures {
		info.DashboardTemp = max(info.DashboardTemp, v)
	}
	// why: read the uptime only after a rule gave data. A Target that is off
	// then does not use the deadline of the read for it.
	// why: the uptime must never fail the read. It has its own short limit,
	// and it is skipped when less time is left, so a silent sysUpTime cannot
	// push the read past the deadline of the scheduler.
	if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > uptimeTimeout {
		uctx, cancel := context.WithTimeout(ctx, uptimeTimeout)
		info.Uptime = Uptime(uctx, s.Reader)
		cancel()
	}
	data := &system.CombinedData{Stats: stats, Info: info}
	// why: the hub asks for the details until it gets them. The hostname is
	// the name, because SYNC_SYSTEM_NAMES=true renames a system to it.
	if opts.IncludeDetails {
		data.Details = &system.Details{Hostname: s.Target.Name}
	}
	return data, nil
}

// twoDecimals rounds to 2 decimals, as the stock agent does.
func twoDecimals(v float64) float64 { return math.Round(v*100) / 100 }

// tableRow is one joined row of a table.
type tableRow struct {
	name  string
	value Value
}

// readRule walks the table of a rule and returns the data points of the rule
// by name, and the number of rows that passed match and exclude.
func (s *TableSource) readRule(ctx context.Context, log *slog.Logger, walks map[string]walkResult, ns *nameSet, rule TableRule) (map[string]float64, int, error) {
	w, ok := walks[rule.OID]
	if !ok {
		w.vals, w.err = s.Reader.Walk(ctx, rule.OID)
		walks[rule.OID] = w
	}
	if w.err != nil {
		return nil, 0, w.err
	}
	rows := joinRows(w.vals, rule)

	kept := rows[:0:0]
	for _, row := range rows {
		if rule.Match != nil && !rule.Match.MatchString(row.name) {
			continue
		}
		if rule.Exclude != nil && rule.Exclude.MatchString(row.name) {
			continue
		}
		kept = append(kept, row)
	}
	kept = s.suffixNames(log, rule.Kind, ns, kept)

	points := map[string]float64{}
	for _, row := range kept {
		v, ok := parseValue(row.value)
		if !ok {
			continue
		}
		v /= rule.Scale
		if !inRange(rule.Kind, v) {
			continue
		}
		points[row.name] = v
	}
	return points, len(kept), nil
}

// joinRows joins the name column and the value column by the row index. The
// index is the part of the OID after the column number, so it can hold more
// than one number. A row that has only one of the 2 columns is dropped, and
// so is a row with an empty name.
func joinRows(vals []Value, rule TableRule) []tableRow {
	prefix := normalizeOID(rule.OID) + "."
	nameCol, valueCol := strconv.Itoa(rule.NameColumn), strconv.Itoa(rule.ValueColumn)
	names := map[string]string{}
	values := map[string]Value{}
	var order []string // the indexes in the order of the value column
	for _, v := range vals {
		rest, ok := strings.CutPrefix(v.OID, prefix)
		if !ok {
			continue
		}
		col, index, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		if col == nameCol {
			names[index] = valueText(v)
		}
		if col == valueCol {
			values[index] = v
			order = append(order, index)
		}
	}
	var rows []tableRow
	for _, index := range order {
		name, ok := names[index]
		if !ok || name == "" {
			continue
		}
		rows = append(rows, tableRow{name: name, value: values[index]})
	}
	return rows
}

// valueText gives a value as the text of a name.
func valueText(v Value) string {
	switch v.Type {
	case TypeOctetString:
		// why: a BMC can pad a name with spaces or NUL bytes, and a
		// `match` or `exclude` with ^ or $ would then miss it.
		return strings.TrimFunc(v.String(), func(r rune) bool { return r == 0 || unicode.IsSpace(r) })
	case TypeInteger, TypeGauge, TypeCounter, TypeTimeTicks:
		return strconv.FormatInt(v.Int, 10)
	}
	return ""
}

// walkResult is the result of one Walk in one read.
type walkResult struct {
	vals []Value
	err  error
}

// nameSet holds the names of one kind in one read, over all rules.
type nameSet struct {
	count map[string]int
	taken map[string]bool
}

func newNameSet() *nameSet {
	return &nameSet{count: map[string]int{}, taken: map[string]bool{}}
}

// suffixNames adds " #2", " #3" and so on to a repeated name, so each row
// keeps its own data point. The set holds the rows of all rules of one kind,
// so 2 rules cannot give one name. A repeated name gives one warning for each
// Target.
func (s *TableSource) suffixNames(log *slog.Logger, kind string, ns *nameSet, rows []tableRow) []tableRow {
	used := map[string]bool{}
	for _, row := range rows {
		used[row.name] = true
	}
	for i, row := range rows {
		raw := row.name
		ns.count[raw]++
		if ns.count[raw] == 1 {
			ns.taken[raw] = true
			continue
		}
		n := ns.count[raw]
		name := fmt.Sprintf("%s #%d", raw, n)
		// why: a row can already have the name that the suffix makes.
		for used[name] || ns.taken[name] {
			n++
			name = fmt.Sprintf("%s #%d", raw, n)
		}
		ns.taken[name] = true
		rows[i].name = name
		key := kind + "/" + raw
		if !s.dups[key] {
			s.dups[key] = true
			log.Warn("repeated sensor name, a suffix is added", "kind", kind, "name", raw)
		}
	}
	return rows
}

// state returns the state of the rule with this index.
func (s *TableSource) state(idx int) *ruleState {
	st := s.rules[idx]
	if st == nil {
		st = &ruleState{seen: map[string]bool{}, gone: map[string]bool{}}
		s.rules[idx] = st
	}
	return st
}

// parseValue reads a number from an integer or from a string with a decimal
// number, such as "700.000". The decimal point is a dot, with no locale.
func parseValue(v Value) (float64, bool) {
	switch v.Type {
	case TypeInteger, TypeGauge, TypeCounter, TypeTimeTicks:
		return float64(v.Int), true
	case TypeOctetString:
		f, err := strconv.ParseFloat(strings.TrimSpace(v.String()), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// inRange checks the value after the scale. A fan value must be an integer
// from 0 to 65535, so a conversion to uint16 never wraps. A temperature must
// be a finite number.
func inRange(kind string, v float64) bool {
	switch kind {
	case KindFan:
		return v >= 0 && v <= math.MaxUint16 && v == math.Trunc(v)
	case KindTemperature:
		return !math.IsNaN(v) && !math.IsInf(v, 0)
	}
	return false
}

// update compares the result of a read with the state of the rule and logs
// each change: a sensor that is gone or back, and a rule that gives no data
// or gives data again.
func (s *TableSource) update(log *slog.Logger, idx int, rule TableRule, points map[string]float64, rows int) {
	st := s.state(idx)
	has := len(points) > 0
	if !has {
		if !st.known || st.hasData {
			log.Warn("table rule gives no data", "rule", idx+1, "oid", rule.OID, "kind", rule.Kind, "rows", rows)
		}
		st.known, st.hasData = true, false
		// why: with no data, each name would give a "gone" warning. The
		// warning of the rule says it already.
		return
	}
	if st.known && !st.hasData {
		log.Info("table rule gives data again", "rule", idx+1, "oid", rule.OID, "kind", rule.Kind)
	}
	st.known, st.hasData = true, true
	for name := range st.seen {
		if _, ok := points[name]; !ok && !st.gone[name] {
			st.gone[name] = true
			log.Warn("sensor is gone", "rule", idx+1, "name", name)
		}
	}
	for name := range points {
		if st.gone[name] {
			delete(st.gone, name)
			log.Info("sensor is back", "rule", idx+1, "name", name)
		}
		st.seen[name] = true
	}
}
