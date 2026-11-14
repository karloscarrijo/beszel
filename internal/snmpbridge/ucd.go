package snmpbridge

import "context"

// The UCD-SNMP OIDs of the built-in CPU and memory sources.
const (
	oidSsCpuIdle    = ".1.3.6.1.4.1.2021.11.11.0"
	oidMemTotalReal = ".1.3.6.1.4.1.2021.4.5.0"
	oidMemAvailReal = ".1.3.6.1.4.1.2021.4.6.0"
	oidMemBuffer    = ".1.3.6.1.4.1.2021.4.14.0"
	oidMemCached    = ".1.3.6.1.4.1.2021.4.15.0"
)

// CPUUCD gives the CPU use in percent: 100 - ssCpuIdle, limited to 0 to 100.
// ok is false when the Target gives no ssCpuIdle.
func CPUUCD(ctx context.Context, r Reader) (percent float64, ok bool, err error) {
	vals, err := r.Get(ctx, []string{oidSsCpuIdle})
	if err != nil {
		return 0, false, err
	}
	idle, ok := numeric(vals, 0)
	if !ok {
		return 0, false, nil
	}
	return min(max(100-float64(idle), 0), 100), true, nil
}

// MemoryUCD gives the memory use in percent:
// (memTotalReal - memAvailReal - memBuffer - memCached) / memTotalReal.
// ok is false when memTotalReal is 0 or missing, when memAvailReal is
// missing, or when the used memory is below 0. A missing memBuffer or
// memCached counts as 0, because some devices have no such object.
func MemoryUCD(ctx context.Context, r Reader) (percent float64, ok bool, err error) {
	vals, err := r.Get(ctx, []string{oidMemTotalReal, oidMemAvailReal, oidMemBuffer, oidMemCached})
	if err != nil {
		return 0, false, err
	}
	total, okTotal := numeric(vals, 0)
	avail, okAvail := numeric(vals, 1)
	buffer, _ := numeric(vals, 2)
	cached, _ := numeric(vals, 3)
	if !okTotal || total <= 0 || !okAvail {
		return 0, false, nil
	}
	used := total - avail - buffer - cached
	if used < 0 {
		return 0, false, nil
	}
	return float64(used) / float64(total) * 100, true, nil
}

// numeric returns the number at index i. ok is false for a missing value or
// a value that is not a number.
func numeric(vals []Value, i int) (int64, bool) {
	if i >= len(vals) {
		return 0, false
	}
	switch vals[i].Type {
	case TypeInteger, TypeGauge, TypeCounter:
		return vals[i].Int, true
	}
	return 0, false
}
