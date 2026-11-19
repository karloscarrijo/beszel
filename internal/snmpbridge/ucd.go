package snmpbridge

import "context"

// The UCD-SNMP OIDs of the built-in CPU and memory sources.
const (
	oidSsCpuIdle    = ".1.3.6.1.4.1.2021.11.11.0"
	oidMemTotalReal = ".1.3.6.1.4.1.2021.4.5.0"
	oidMemAvailReal = ".1.3.6.1.4.1.2021.4.6.0"
	oidMemBuffer    = ".1.3.6.1.4.1.2021.4.14.0"
	oidMemCached    = ".1.3.6.1.4.1.2021.4.15.0"
	oidSysUpTime    = ".1.3.6.1.2.1.1.3.0"

	oidHrProcessorLoad = ".1.3.6.1.2.1.25.3.3.1.2"
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

// CPUHR gives the CPU use in percent: the mean of the numeric hrProcessorLoad
// rows, limited to 0 to 100. ok is false when the Target gives no numeric row.
func CPUHR(ctx context.Context, r Reader) (percent float64, ok bool, err error) {
	rows, err := r.Walk(ctx, oidHrProcessorLoad)
	if err != nil {
		return 0, false, err
	}
	var sum, n int64
	for i := range rows {
		if v, isNum := numeric(rows, i); isNum {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0, false, nil
	}
	return min(max(float64(sum)/float64(n), 0), 100), true, nil
}

// Memory is the memory use of a Target. The byte values are the UCD values
// in kB, times 1024.
type Memory struct {
	Percent   float64
	Total     uint64
	Used      uint64
	BuffCache uint64
}

// MemoryUCD gives the memory use:
// (memTotalReal - memAvailReal - memBuffer - memCached) / memTotalReal, in
// percent, and the total, the used value and buffer + cached in bytes.
// ok is false when memTotalReal is 0 or missing, when memAvailReal is
// missing, or when the used memory is below 0. A missing memBuffer or
// memCached counts as 0, because some devices have no such object.
func MemoryUCD(ctx context.Context, r Reader) (mem Memory, ok bool, err error) {
	vals, err := r.Get(ctx, []string{oidMemTotalReal, oidMemAvailReal, oidMemBuffer, oidMemCached})
	if err != nil {
		return Memory{}, false, err
	}
	total, okTotal := numeric(vals, 0)
	avail, okAvail := numeric(vals, 1)
	buffer, _ := numeric(vals, 2)
	cached, _ := numeric(vals, 3)
	// why: a negative value is a device error. Count it as 0, so the percent
	// and the bytes agree.
	buffer, cached = max(buffer, 0), max(cached, 0)
	if !okTotal || total <= 0 || !okAvail {
		return Memory{}, false, nil
	}
	used := total - avail - buffer - cached
	if used < 0 {
		return Memory{}, false, nil
	}
	return Memory{
		Percent:   float64(used) / float64(total) * 100,
		Total:     uint64(total) * 1024,
		Used:      uint64(used) * 1024,
		BuffCache: uint64(buffer+cached) * 1024,
	}, true, nil
}

// Uptime gives sysUpTime in seconds. The value is in 1/100 s. It is the
// uptime of the SNMP service of the Target. A missing value or a failed read
// gives 0: the uptime is not a rule, so it never fails a read.
func Uptime(ctx context.Context, r Reader) uint64 {
	vals, err := r.Get(ctx, []string{oidSysUpTime})
	if err != nil || len(vals) == 0 {
		return 0
	}
	switch vals[0].Type {
	case TypeTimeTicks, TypeInteger, TypeGauge, TypeCounter:
		return uint64(max(vals[0].Int, 0) / 100)
	}
	return 0
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
