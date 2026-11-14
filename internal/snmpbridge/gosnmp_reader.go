package snmpbridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/gosnmp/gosnmp"
)

// GoSNMPReader reads one Target with gosnmp through SNMPv2c. It opens one
// connection for each call, so it is safe for concurrent use.
type GoSNMPReader struct {
	TargetID  string
	Host      string
	Port      int
	Community string
}

// NewGoSNMPReader returns the reader of a Target.
func NewGoSNMPReader(t TargetConfig) *GoSNMPReader {
	return &GoSNMPReader{TargetID: t.ID, Host: t.Host, Port: t.Port, Community: t.Community}
}

var _ Reader = (*GoSNMPReader)(nil)

func (r *GoSNMPReader) connect(ctx context.Context) (*gosnmp.GoSNMP, error) {
	g := &gosnmp.GoSNMP{
		Context:   ctx,
		Target:    r.Host,
		Port:      uint16(r.Port),
		Community: r.Community,
		Version:   gosnmp.Version2c,
		// why: the context sets the deadline (beszel#008). This is only an
		// upper limit when the context has none.
		Timeout: 10 * time.Second,
		Retries: 0,
	}
	if err := g.Connect(); err != nil {
		return nil, err
	}
	return g, nil
}

// String hides the community, so a format verb cannot print it.
func (r GoSNMPReader) String() string {
	return fmt.Sprintf("{target=%s host=%s port=%d}", r.TargetID, r.Host, r.Port)
}

// GoString hides the community in %#v.
func (r GoSNMPReader) GoString() string { return r.String() }

// LogValue hides the community in a slog line.
func (r GoSNMPReader) LogValue() slog.Value {
	return slog.GroupValue(slog.String("target", r.TargetID), slog.String("host", r.Host), slog.Int("port", r.Port))
}

// fail hides the error of gosnmp: it can hold the community or the target.
// It keeps only a deadline or a cancel as the cause, so the caller can tell
// a timeout from a different error.
func (r *GoSNMPReader) fail(ctx context.Context, cause error, op, oid string) error {
	keep := ctx.Err()
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		keep = context.DeadlineExceeded
	case errors.Is(cause, context.Canceled):
		keep = context.Canceled
	}
	return &ReadError{Target: r.TargetID, Op: op, OID: oid, Cause: keep}
}

// Get implements Reader.
func (r *GoSNMPReader) Get(ctx context.Context, oids []string) ([]Value, error) {
	first := ""
	if len(oids) > 0 {
		first = oids[0]
	}
	g, err := r.connect(ctx)
	if err != nil {
		return nil, r.fail(ctx, err, "get", first)
	}
	defer g.Conn.Close()
	res, err := g.Get(oids)
	if err != nil || res == nil || res.Error != gosnmp.NoError {
		return nil, r.fail(ctx, err, "get", first)
	}
	out := make([]Value, 0, len(res.Variables))
	for _, pdu := range res.Variables {
		out = append(out, convertPDU(pdu))
	}
	return out, nil
}

// Walk implements Reader.
func (r *GoSNMPReader) Walk(ctx context.Context, oid string) ([]Value, error) {
	g, err := r.connect(ctx)
	if err != nil {
		return nil, r.fail(ctx, err, "walk", oid)
	}
	defer g.Conn.Close()
	// why: v2c has GetBulk. It needs far fewer round trips than GetNext.
	pdus, err := g.BulkWalkAll(oid)
	if err != nil {
		return nil, r.fail(ctx, err, "walk", oid)
	}
	out := make([]Value, 0, len(pdus))
	for _, pdu := range pdus {
		out = append(out, convertPDU(pdu))
	}
	return out, nil
}

func convertPDU(pdu gosnmp.SnmpPDU) Value {
	v := Value{OID: normalizeOID(pdu.Name)}
	switch pdu.Type {
	case gosnmp.Integer:
		v.Type, v.Int = TypeInteger, toInt64(pdu.Value)
	case gosnmp.Gauge32, gosnmp.Uinteger32:
		v.Type, v.Int = TypeGauge, toInt64(pdu.Value)
	case gosnmp.Counter32, gosnmp.Counter64:
		v.Type, v.Int = TypeCounter, toInt64(pdu.Value)
	case gosnmp.TimeTicks:
		v.Type, v.Int = TypeTimeTicks, toInt64(pdu.Value)
	case gosnmp.OctetString:
		v.Type = TypeOctetString
		if b, ok := pdu.Value.([]byte); ok {
			v.Bytes = b
		}
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
		v.Type = TypeNoSuch
	default:
		v.Type = TypeOther
	}
	return v
}

func toInt64(x any) int64 {
	b := gosnmp.ToBigInt(x)
	if b == nil || !b.IsInt64() {
		// why: a Counter64 above 2^63 is not a gauge. Keep the low bits.
		if b != nil {
			return new(big.Int).And(b, big.NewInt(1<<63-1)).Int64()
		}
		return 0
	}
	return b.Int64()
}
