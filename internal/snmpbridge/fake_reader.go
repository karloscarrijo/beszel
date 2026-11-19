package snmpbridge

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// FakeReader is a Reader that answers from .snmprec text. Tests use it, so no
// test needs a real device.
type FakeReader struct {
	values   []Value
	Err      bool
	TargetID string
}

var _ Reader = (*FakeReader)(nil)

// NewFakeReader parses .snmprec text: one `oid|type|value` line each. The
// types are 2 (integer), 4 (octet string), 4x (hex octet string), 65
// (counter), 66 (gauge), 67 (timeticks) and 70 (counter64). An empty line and
// a line that starts with # are skipped.
func NewFakeReader(text string) (*FakeReader, error) {
	f := &FakeReader{}
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("snmprec line %d: want oid|type|value", n+1)
		}
		v := Value{OID: normalizeOID(parts[0])}
		switch parts[1] {
		case "2", "65", "66", "67", "70":
			i, err := strconv.ParseInt(parts[2], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("snmprec line %d: bad number", n+1)
			}
			v.Int = i
			v.Type = map[string]ValueType{"2": TypeInteger, "65": TypeCounter, "66": TypeGauge, "67": TypeTimeTicks, "70": TypeCounter}[parts[1]]
		case "4":
			v.Type, v.Bytes = TypeOctetString, []byte(parts[2])
		case "4x":
			b, err := hex.DecodeString(parts[2])
			if err != nil {
				return nil, fmt.Errorf("snmprec line %d: bad hex", n+1)
			}
			v.Type, v.Bytes = TypeOctetString, b
		default:
			return nil, fmt.Errorf("snmprec line %d: type %s is not supported", n+1, parts[1])
		}
		f.values = append(f.values, v)
	}
	slices.SortStableFunc(f.values, func(a, b Value) int { return compareOID(a.OID, b.OID) })
	return f, nil
}

func (f *FakeReader) fail(op, oid string) error {
	return &ReadError{Target: f.TargetID, Op: op, OID: oid}
}

// Get implements Reader.
func (f *FakeReader) Get(ctx context.Context, oids []string) ([]Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, &ReadError{Target: f.TargetID, Op: "get", OID: firstOID(oids), Cause: err}
	}
	if f.Err {
		return nil, f.fail("get", firstOID(oids))
	}
	out := make([]Value, 0, len(oids))
	for _, oid := range oids {
		oid = normalizeOID(oid)
		v := Value{OID: oid, Type: TypeNoSuch}
		if i, ok := slices.BinarySearchFunc(f.values, oid, func(v Value, o string) int { return compareOID(v.OID, o) }); ok {
			v = f.values[i]
		}
		out = append(out, v)
	}
	return out, nil
}

// Walk implements Reader.
func (f *FakeReader) Walk(ctx context.Context, oid string) ([]Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, &ReadError{Target: f.TargetID, Op: "walk", OID: oid, Cause: err}
	}
	if f.Err {
		return nil, f.fail("walk", oid)
	}
	prefix := normalizeOID(oid) + "."
	var out []Value
	for _, v := range f.values {
		if strings.HasPrefix(v.OID, prefix) {
			out = append(out, v)
		}
	}
	return out, nil
}

func firstOID(oids []string) string {
	if len(oids) == 0 {
		return ""
	}
	return oids[0]
}

// compareOID orders OIDs by number, not by text.
func compareOID(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x - y
		}
	}
	return len(as) - len(bs)
}
