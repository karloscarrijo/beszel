package snmpbridge

import (
	"bytes"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Table kinds of a table rule.
const (
	KindTemperature = "temperature"
	KindFan         = "fan"
)

// SourceUCD is a valid value of `cpu` and the only valid value of `memory`.
const SourceUCD = "ucd"

// SourceHR is a valid value of `cpu`: the mean of hrProcessorLoad.
const SourceHR = "hr"

// ConfigEnv is the variable that holds the path of the config file.
const ConfigEnv = "SNMPBRIDGE_CONFIG"

// Default values of a Target.
const (
	DefaultPort    = 161
	DefaultVersion = "2c"
)

// Config is the checked content of the config file.
type Config struct {
	Profiles map[string]Profile
	Targets  []TargetConfig
}

// Profile is a named set of rules.
type Profile struct {
	Tables []TableRule
	CPU    string // "", SourceUCD or SourceHR
	Memory string // "" or SourceUCD
	// HTTP is set for a Profile of a Target with a url, and nil otherwise.
	HTTP *HTTPProfile
}

// HTTPProfile names the JSON fields of an HTTP source. Each value is a dotted
// path of object keys, or "" for a field that the Profile does not read.
type HTTPProfile struct {
	CPU           string
	MemoryPercent string
	Uptime        string
	// Fresh is nil when the Profile has no freshness check.
	Fresh *HTTPFresh
}

// HTTPFresh is the freshness check of an HTTP Profile.
type HTTPFresh struct {
	Path   string
	MaxAge time.Duration
}

// why: the http*.go files set these hooks in init(), and the bridge builds without them (docs/adr/0001).
var (
	checkHTTPProfile func(name string, h rawHTTP) (*HTTPProfile, error)
	checkHTTPTarget  func(rt rawTarget, p *HTTPProfile, t *TargetConfig) error
)

var errNoHTTPSource = errors.New("this build has no HTTP source")

// TableRule tells the reader which table to read. The table reads are in
// beszel#007.
type TableRule struct {
	OID         string // table entry OID, dotted, with a leading dot
	NameColumn  int
	ValueColumn int
	Kind        string  // KindTemperature or KindFan
	Scale       float64 // divides the value, > 0
	Match       *regexp.Regexp
	Exclude     *regexp.Regexp
}

// TargetConfig is one Target and how to read it.
type TargetConfig struct {
	Target
	Host    string
	Port    int
	Version string
	Profile string
	// Community is the SNMP community. It never goes to a log or an error.
	Community string
	// URL can hold a query, so print it only through SafeURL.
	URL string
	// HeaderValue never goes to a log or an error.
	Header      string
	HeaderValue string
	// CAPool replaces the system pool when ca_file is set, and is nil otherwise.
	CAPool        *x509.CertPool
	TLSServerName string
	// MemoryTotalMiB is 0 when the Target gives no memory bytes.
	MemoryTotalMiB uint64
}

// SafeURL gives the scheme, the host and the path of the URL, and never the
// user, the query or the fragment. A URL that does not parse gives "".
func (t TargetConfig) SafeURL() string {
	u, err := url.Parse(t.URL)
	if err != nil {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

// String hides the community, the header value and the query of the URL, so a
// format verb cannot print them.
func (t TargetConfig) String() string {
	if t.URL != "" {
		return fmt.Sprintf("{id=%s name=%s url=%s profile=%s}", t.ID, t.Name, t.SafeURL(), t.Profile)
	}
	return fmt.Sprintf("{id=%s name=%s host=%s port=%d version=%s profile=%s}", t.ID, t.Name, t.Host, t.Port, t.Version, t.Profile)
}

// GoString hides the secrets in %#v.
func (t TargetConfig) GoString() string { return t.String() }

// LogValue hides the secrets in a slog line.
func (t TargetConfig) LogValue() slog.Value {
	if t.URL != "" {
		return slog.GroupValue(
			slog.String("id", t.ID), slog.String("name", t.Name),
			slog.String("url", t.SafeURL()), slog.String("profile", t.Profile),
		)
	}
	return slog.GroupValue(
		slog.String("id", t.ID), slog.String("name", t.Name),
		slog.String("host", t.Host), slog.Int("port", t.Port),
		slog.String("version", t.Version), slog.String("profile", t.Profile),
	)
}

// The raw types hold the file as it is, so a missing field is visible.
type rawConfig struct {
	Profiles map[string]rawProfile `yaml:"profiles"`
	Targets  []rawTarget           `yaml:"targets"`
}

type rawProfile struct {
	Tables []rawTable `yaml:"tables"`
	CPU    string     `yaml:"cpu"`
	Memory string     `yaml:"memory"`
	HTTP   *rawHTTP   `yaml:"http"`
}

type rawHTTP struct {
	CPU           string    `yaml:"cpu"`
	MemoryPercent string    `yaml:"memory_percent"`
	Uptime        string    `yaml:"uptime"`
	Fresh         yaml.Node `yaml:"fresh"`
}

type rawFresh struct {
	Path   string `yaml:"path"`
	MaxAge any    `yaml:"max_age"`
}

type rawTable struct {
	OID         string   `yaml:"oid"`
	NameColumn  *int     `yaml:"name_column"`
	ValueColumn *int     `yaml:"value_column"`
	Kind        string   `yaml:"kind"`
	Scale       *float64 `yaml:"scale"`
	Match       string   `yaml:"match"`
	Exclude     string   `yaml:"exclude"`
}

type rawTarget struct {
	ID              string `yaml:"id"`
	Name            string `yaml:"name"`
	Host            string `yaml:"host"`
	Port            *int   `yaml:"port"`
	Version         string `yaml:"version"`
	Profile         string `yaml:"profile"`
	CommunityEnv    string `yaml:"community_env"`
	CommunityFile   string `yaml:"community_file"`
	URL             string `yaml:"url"`
	Header          string `yaml:"header"`
	HeaderValueEnv  string `yaml:"header_value_env"`
	HeaderValueFile string `yaml:"header_value_file"`
	CAFile          string `yaml:"ca_file"`
	TLSServerName   string `yaml:"tls_server_name"`
	MemoryTotalMiB  any    `yaml:"memory_total_mib"`
}

var (
	idPattern  = regexp.MustCompile(`^[a-z0-9-]+$`)
	oidPattern = regexp.MustCompile(`^\.?[0-9]+(\.[0-9]+)*$`)
)

// LoadConfig reads and checks the file that SNMPBRIDGE_CONFIG names. The
// error names the item that is wrong. It never holds a community.
func LoadConfig() (*Config, error) {
	path := os.Getenv(ConfigEnv)
	if path == "" {
		return nil, fmt.Errorf("must set %s", ConfigEnv)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ConfigEnv, err)
	}
	return ParseConfig(data)
}

// ParseConfig checks the YAML text of a config file. A community comes from
// the environment or from a file, as each Target says.
func ParseConfig(data []byte) (*Config, error) {
	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("config is empty")
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// why: a second YAML document would be dropped with no error.
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("config must hold one YAML document")
	}
	cfg := &Config{Profiles: make(map[string]Profile, len(raw.Profiles))}
	// why: a sorted order gives the same error at each start.
	for _, name := range slices.Sorted(maps.Keys(raw.Profiles)) {
		rp := raw.Profiles[name]
		p, err := checkProfile(name, rp)
		if err != nil {
			return nil, err
		}
		cfg.Profiles[name] = p
	}
	if len(raw.Targets) == 0 {
		return nil, errors.New("config has no targets")
	}
	seen := map[string]bool{}
	for i, rt := range raw.Targets {
		// why: check the id first, so a duplicate id is not hidden by a
		// different error of the second Target.
		if seen[rt.ID] {
			return nil, fmt.Errorf("target id %q is not unique", rt.ID)
		}
		seen[rt.ID] = true
		t, err := checkTarget(i, rt, cfg.Profiles)
		if err != nil {
			return nil, err
		}
		cfg.Targets = append(cfg.Targets, t)
	}
	return cfg, nil
}

func checkProfile(name string, rp rawProfile) (Profile, error) {
	p := Profile{CPU: rp.CPU, Memory: rp.Memory}
	if name == "" {
		return p, errors.New("a Profile has an empty name")
	}
	if rp.HTTP != nil {
		switch {
		case checkHTTPProfile == nil:
			return p, fmt.Errorf("profile %q: %w", name, errNoHTTPSource)
		case len(rp.Tables) > 0 || rp.CPU != "" || rp.Memory != "":
			return p, fmt.Errorf("profile %q: http cannot go with tables, cpu or memory", name)
		}
		h, err := checkHTTPProfile(name, *rp.HTTP)
		if err != nil {
			return p, err
		}
		p.HTTP = h
		return p, nil
	}
	if rp.CPU != "" && rp.CPU != SourceUCD && rp.CPU != SourceHR {
		return p, fmt.Errorf("profile %q: cpu must be %q or %q, not %q", name, SourceUCD, SourceHR, rp.CPU)
	}
	if rp.Memory != "" && rp.Memory != SourceUCD {
		return p, fmt.Errorf("profile %q: memory must be %q, not %q", name, SourceUCD, rp.Memory)
	}
	if len(rp.Tables) == 0 && rp.CPU == "" && rp.Memory == "" {
		return p, fmt.Errorf("profile %q has no rules", name)
	}
	for i, rt := range rp.Tables {
		rule, err := checkTable(rt)
		if err != nil {
			return p, fmt.Errorf("profile %q: table %d: %w", name, i+1, err)
		}
		p.Tables = append(p.Tables, rule)
	}
	return p, nil
}

func checkTable(rt rawTable) (TableRule, error) {
	r := TableRule{Scale: 1}
	if rt.OID == "" {
		return r, errors.New("missing field oid")
	}
	if !oidPattern.MatchString(rt.OID) {
		return r, fmt.Errorf("oid %q is not a dotted OID", rt.OID)
	}
	r.OID = "." + strings.TrimPrefix(rt.OID, ".")
	if rt.NameColumn == nil {
		return r, errors.New("missing field name_column")
	}
	if rt.ValueColumn == nil {
		return r, errors.New("missing field value_column")
	}
	if *rt.NameColumn < 1 || *rt.ValueColumn < 1 {
		return r, errors.New("name_column and value_column must be 1 or more")
	}
	r.NameColumn, r.ValueColumn = *rt.NameColumn, *rt.ValueColumn
	switch rt.Kind {
	case "":
		return r, errors.New("missing field kind")
	case KindTemperature, KindFan:
		r.Kind = rt.Kind
	default:
		return r, fmt.Errorf("kind must be %q or %q, not %q", KindTemperature, KindFan, rt.Kind)
	}
	if rt.Scale != nil {
		if !(*rt.Scale > 0) || math.IsInf(*rt.Scale, 0) {
			return r, errors.New("scale must be more than 0")
		}
		r.Scale = *rt.Scale
	}
	var err error
	if rt.Match != "" {
		if r.Match, err = regexp.Compile(rt.Match); err != nil {
			return r, fmt.Errorf("invalid regexp in match: %w", err)
		}
	}
	if rt.Exclude != "" {
		if r.Exclude, err = regexp.Compile(rt.Exclude); err != nil {
			return r, fmt.Errorf("invalid regexp in exclude: %w", err)
		}
	}
	return r, nil
}

// checkHTTPTargetConfig checks a Target with a url.
func checkHTTPTargetConfig(where string, rt rawTarget, profiles map[string]Profile) (TargetConfig, error) {
	t := TargetConfig{}
	if checkHTTPTarget == nil {
		return t, fmt.Errorf("%s: %w", where, errNoHTTPSource)
	}
	if rt.Port != nil || rt.Version != "" || rt.CommunityEnv != "" || rt.CommunityFile != "" {
		return t, fmt.Errorf("%s: port, version, community_env and community_file need host, not url", where)
	}
	if rt.Profile == "" {
		return t, fmt.Errorf("%s: missing field profile", where)
	}
	p, ok := profiles[rt.Profile]
	if !ok {
		return t, fmt.Errorf("%s: profile %q does not exist", where, rt.Profile)
	}
	if p.HTTP == nil {
		return t, fmt.Errorf("%s: profile %q has no http, so the Target needs host, not url", where, rt.Profile)
	}
	t.ID, t.Name, t.Profile = rt.ID, rt.Name, rt.Profile
	if err := checkHTTPTarget(rt, p.HTTP, &t); err != nil {
		return t, fmt.Errorf("%s: %w", where, err)
	}
	return t, nil
}

func checkTarget(i int, rt rawTarget, profiles map[string]Profile) (TargetConfig, error) {
	t := TargetConfig{Port: DefaultPort, Version: DefaultVersion}
	where := fmt.Sprintf("target %d", i+1)
	if rt.ID == "" {
		return t, fmt.Errorf("%s: missing field id", where)
	}
	if !idPattern.MatchString(rt.ID) {
		return t, fmt.Errorf("target id %q: only a-z, 0-9 and - are valid", rt.ID)
	}
	where = fmt.Sprintf("target %q", rt.ID)
	if rt.Name == "" {
		return t, fmt.Errorf("%s: missing field name", where)
	}
	switch {
	case rt.Host != "" && rt.URL != "":
		return t, fmt.Errorf("%s: set only one of host and url", where)
	case rt.Host == "" && rt.URL == "":
		return t, fmt.Errorf("%s: missing field host or url", where)
	case rt.URL != "":
		return checkHTTPTargetConfig(where, rt, profiles)
	}
	if rt.Header != "" || rt.HeaderValueEnv != "" || rt.HeaderValueFile != "" {
		return t, fmt.Errorf("%s: header, header_value_env and header_value_file need url, not host", where)
	}
	if rt.CAFile != "" || rt.TLSServerName != "" || rt.MemoryTotalMiB != nil {
		return t, fmt.Errorf("%s: ca_file, tls_server_name and memory_total_mib need url, not host", where)
	}
	if rt.Port != nil {
		if *rt.Port < 1 || *rt.Port > 65535 {
			return t, fmt.Errorf("%s: port must be 1 to 65535", where)
		}
		t.Port = *rt.Port
	}
	if rt.Version != "" {
		t.Version = rt.Version
	}
	if t.Version != DefaultVersion {
		return t, fmt.Errorf("%s: version must be %q, not %q", where, DefaultVersion, t.Version)
	}
	if rt.Profile == "" {
		return t, fmt.Errorf("%s: missing field profile", where)
	}
	if _, ok := profiles[rt.Profile]; !ok {
		return t, fmt.Errorf("%s: profile %q does not exist", where, rt.Profile)
	}
	if profiles[rt.Profile].HTTP != nil {
		return t, fmt.Errorf("%s: profile %q has http, so the Target needs url, not host", where, rt.Profile)
	}
	community, err := loadCommunity(rt)
	if err != nil {
		return t, fmt.Errorf("%s: %w", where, err)
	}
	t.ID, t.Name, t.Host, t.Profile, t.Community = rt.ID, rt.Name, rt.Host, rt.Profile, community
	return t, nil
}

// loadCommunity reads the community. An error names the variable or the file,
// never the value.
func loadCommunity(rt rawTarget) (string, error) {
	switch {
	case rt.CommunityEnv != "" && rt.CommunityFile != "":
		return "", errors.New("set only one of community_env and community_file")
	case rt.CommunityEnv != "":
		v := strings.TrimSpace(os.Getenv(rt.CommunityEnv))
		if v == "" {
			return "", fmt.Errorf("community_env %s is empty or not set", rt.CommunityEnv)
		}
		return v, nil
	case rt.CommunityFile != "":
		b, err := os.ReadFile(rt.CommunityFile)
		if err != nil {
			return "", fmt.Errorf("read community_file: %w", err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("community_file %s is empty", rt.CommunityFile)
		}
		return v, nil
	}
	return "", errors.New("set one of community_env and community_file")
}
