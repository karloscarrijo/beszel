package snmpbridge

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
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
	return &HTTPProfile{CPU: h.CPU, MemoryPercent: h.MemoryPercent, Uptime: h.Uptime}, nil
}

// checkHTTPTargetKeys checks url, header and the header value, and sets them
// in t. An error never holds the query of the URL or the header value.
func checkHTTPTargetKeys(rt rawTarget, t *TargetConfig) error {
	// why: the error of url.Parse holds the full URL, with the query.
	u, err := url.Parse(rt.URL)
	if err != nil || u.Host == "" {
		return errors.New("url is not valid: it needs a scheme and a host")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https, not %q", u.Scheme)
	}
	if u.User != nil {
		return errors.New("url must have no user and no password: use header")
	}
	t.URL = rt.URL
	if rt.Header == "" {
		if rt.HeaderValueEnv != "" || rt.HeaderValueFile != "" {
			return errors.New("header_value_env and header_value_file need header")
		}
		return nil
	}
	if !headerNamePattern.MatchString(rt.Header) {
		return fmt.Errorf("header %q is not a valid HTTP header name", rt.Header)
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
