package snmpbridge

import (
	"context"
	"fmt"
	"strings"
)

// ValueType is the SNMP type of a Value.
type ValueType int

// The types that the reader gives.
const (
	TypeNoSuch ValueType = iota // the OID does not exist on the Target
	TypeInteger
	TypeOctetString
	TypeGauge
	TypeCounter
	TypeTimeTicks
	TypeOther // a type that the bridge does not use
)

// Value is one raw value from a Target.
type Value struct {
	OID  string // dotted, with a leading dot
	Type ValueType
	// Int holds the number of TypeInteger, TypeGauge, TypeCounter and
	// TypeTimeTicks.
	Int int64
	// Bytes holds the content of TypeOctetString.
	Bytes []byte
}

// String gives the octet string as text.
func (v Value) String() string { return string(v.Bytes) }

// Reader reads raw values from one Target. It holds no formulas and no table
// logic. The deadline comes from the context.
type Reader interface {
	// Get reads each OID. An OID that does not exist gives a Value of
	// TypeNoSuch. The values come in the order of the OIDs.
	Get(ctx context.Context, oids []string) ([]Value, error)
	// Walk reads each value below a table OID, in OID order.
	Walk(ctx context.Context, oid string) ([]Value, error)
}

// ReadError is an SNMP error. Its text is fixed: it names the Target and the
// OID, and never holds the community, the packet or the target string of
// gosnmp.
type ReadError struct {
	Target string // the Target id
	Op     string // "get" or "walk"
	OID    string
	// Cause is only set when the context ended. It is the context error.
	Cause error
}

func (e *ReadError) Error() string {
	s := fmt.Sprintf("snmp %s of target %q at %s failed", e.Op, e.Target, e.OID)
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

func (e *ReadError) Unwrap() error { return e.Cause }

// normalizeOID gives the dotted form with one leading dot.
func normalizeOID(oid string) string { return "." + strings.TrimPrefix(oid, ".") }
