package snmpbridge

import (
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

func init() {
	checkHTTPProfile = checkHTTPProfileKeys
	checkHTTPTarget = checkHTTPTargetKeys
}

var (
	jsonPathPattern   = regexp.MustCompile(`^[^.]+(\.[^.]+)*$`)
	headerNamePattern = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")
)

func checkHTTPProfileKeys(name string, h rawHTTP) (*HTTPProfile, error) {
	if h.CPU == "" && h.MemoryPercent == "" && h.Uptime == "" {
		return nil, fmt.Errorf("profile %q: http needs one or more of cpu, memory_percent and uptime", name)
	}
	for _, f := range []struct{ key, path string }{
		{"cpu", h.CPU}, {"memory_percent", h.MemoryPercent}, {"uptime", h.Uptime},
	} {
		if f.path != "" && !jsonPathPattern.MatchString(f.path) {
			return nil, fmt.Errorf("profile %q: http %s %q is not a dotted path of keys", name, f.key, f.path)
		}
	}
	p := &HTTPProfile{CPU: h.CPU, MemoryPercent: h.MemoryPercent, Uptime: h.Uptime}
	// why: a key with no value (YAML null) must still be checked, so Fresh is a yaml.Node.
	if h.Fresh.Kind != 0 {
		var f rawFresh
		if err := h.Fresh.Decode(&f); err != nil {
			return nil, fmt.Errorf("profile %q: http fresh must have path and max_age", name)
		}
		if f.Path == "" || !jsonPathPattern.MatchString(f.Path) {
			return nil, fmt.Errorf("profile %q: http fresh needs path, a dotted path of keys", name)
		}
		secs, ok := wholeAbove0(f.MaxAge)
		if !ok || secs > math.MaxInt64/uint64(time.Second) {
			return nil, fmt.Errorf("profile %q: http fresh max_age must be a whole number of seconds above 0", name)
		}
		p.Fresh = &HTTPFresh{Path: f.Path, MaxAge: time.Duration(secs) * time.Second}
	}
	return p, nil
}

// why: MiB times 1048576 must stay inside the range of a uint64 and of a float64 count of bytes.
const maxMemoryMiB = 1 << 40

// wholeAbove0 gives v as a whole number above 0. A YAML number is an int, a
// uint64 or a float64.
func wholeAbove0(v any) (uint64, bool) {
	switch n := v.(type) {
	case int:
		return uint64(n), n > 0
	case uint64:
		return n, n > 0
	case float64:
		return uint64(n), n >= 1 && n == math.Trunc(n) && n < 1<<63
	}
	return 0, false
}

// loadCAPool reads a PEM file. It ignores a block that is not a certificate.
func loadCAPool(file string) (*x509.CertPool, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read ca_file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("ca_file %s has no valid certificate", file)
	}
	return pool, nil
}

// checkHTTPTargetKeys checks url, header, the header value, ca_file,
// tls_server_name and memory_total_mib, and sets them in t. An error never
// holds the query of the URL or the header value.
func checkHTTPTargetKeys(rt rawTarget, p *HTTPProfile, t *TargetConfig) error {
	// why: the error of url.Parse holds the full URL, with the query.
	u, err := url.Parse(rt.URL)
	if err != nil || u.Hostname() == "" {
		return errors.New("url is not valid: it needs a scheme and a host")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https, not %q", u.Scheme)
	}
	if u.User != nil {
		return errors.New("url must have no user and no password: use header")
	}
	t.URL = rt.URL
	if (rt.CAFile != "" || rt.TLSServerName != "") && u.Scheme != "https" {
		return errors.New("ca_file and tls_server_name need an https url")
	}
	if rt.CAFile != "" {
		pool, err := loadCAPool(rt.CAFile)
		if err != nil {
			return err
		}
		t.CAPool = pool
	}
	t.TLSServerName = rt.TLSServerName
	if rt.MemoryTotalMiB.Kind != 0 {
		var raw any
		mib, ok := uint64(0), false
		if rt.MemoryTotalMiB.Decode(&raw) == nil {
			mib, ok = wholeAbove0(raw)
		}
		if !ok || mib > maxMemoryMiB {
			return fmt.Errorf("memory_total_mib must be a whole number of MiB from 1 to %d", uint64(maxMemoryMiB))
		}
		if p.MemoryPercent == "" {
			return errors.New("memory_total_mib needs a Profile with memory_percent")
		}
		t.MemoryTotalMiB = mib
	}
	if rt.Header == "" {
		if rt.HeaderValueEnv != "" || rt.HeaderValueFile != "" {
			return errors.New("header_value_env and header_value_file need header")
		}
		return nil
	}
	if !headerNamePattern.MatchString(rt.Header) {
		return fmt.Errorf("header %q is not a valid HTTP header name", rt.Header)
	}
	// why: Go's client takes the Host header from req.Host, so Header.Set would not send it.
	if strings.EqualFold(rt.Header, "Host") {
		return errors.New("header Host is not supported: the client sends the host of the url")
	}
	v, err := loadHeaderValue(rt)
	if err != nil {
		return err
	}
	t.Header, t.HeaderValue = rt.Header, v
	return nil
}

// loadHeaderValue reads the header value. An error names the variable or the
// file, never the value.
func loadHeaderValue(rt rawTarget) (string, error) {
	var v, from string
	switch {
	case rt.HeaderValueEnv != "" && rt.HeaderValueFile != "":
		return "", errors.New("set only one of header_value_env and header_value_file")
	case rt.HeaderValueEnv != "":
		v, from = os.Getenv(rt.HeaderValueEnv), "header_value_env "+rt.HeaderValueEnv
	case rt.HeaderValueFile != "":
		b, err := os.ReadFile(rt.HeaderValueFile)
		if err != nil {
			return "", fmt.Errorf("read header_value_file: %w", err)
		}
		v, from = string(b), "header_value_file "+rt.HeaderValueFile
	default:
		return "", errors.New("set one of header_value_env and header_value_file")
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("%s is empty or not set", from)
	}
	// why: the Go transport rejects each control character except tab, so the Target would be down for good.
	if strings.ContainsFunc(v, func(r rune) bool { return (r < 0x20 && r != '\t') || r == 0x7f }) {
		return "", fmt.Errorf("%s has a line break or a control character", from)
	}
	return v, nil
}
