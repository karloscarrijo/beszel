package snmpbridge

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const httpConfig = `
profiles:
  device:
    http:
      cpu: cpuUtilizationPct
      memory_percent: memoryUtilizationPct
      uptime: uptimeSec
  snmp:
    cpu: ucd
targets:
  - id: usw-flex-mini
    name: USW Flex Mini
    profile: device
    url: https://192.0.2.10/stats/latest?token=query-secret
    header: X-API-KEY
    header_value_env: TEST_HEADER_VALUE
  - id: nas
    name: NAS
    host: 192.0.2.11
    profile: snmp
    community_env: TEST_COMMUNITY
`

func TestParseConfigHTTP(t *testing.T) {
	t.Setenv("TEST_HEADER_VALUE", "  header-secret  ")
	t.Setenv("TEST_COMMUNITY", "community-secret")
	cfg, err := ParseConfig([]byte(httpConfig))
	require.NoError(t, err)
	assert.Equal(t, &HTTPProfile{CPU: "cpuUtilizationPct", MemoryPercent: "memoryUtilizationPct", Uptime: "uptimeSec"}, cfg.Profiles["device"].HTTP)
	assert.Nil(t, cfg.Profiles["snmp"].HTTP)
	tc := cfg.Targets[0]
	assert.Equal(t, "X-API-KEY", tc.Header)
	assert.Equal(t, "header-secret", tc.HeaderValue, "the spaces at the start and the end are removed")
	assert.Equal(t, "https://192.0.2.10/stats/latest?token=query-secret", tc.URL)
	assert.Empty(t, cfg.Targets[1].URL)
}

func TestParseConfigHTTPErrors(t *testing.T) {
	t.Setenv("TEST_HEADER_VALUE", "header-secret")
	t.Setenv("TEST_COMMUNITY", "community-secret")
	t.Setenv("TEST_EMPTY", "  ")
	t.Setenv("TEST_LINES", "a\nb")
	t.Setenv("TEST_CTRL", "a\x01b")
	const urlLine = "    url: https://192.0.2.10/stats/latest?token=query-secret\n"
	const hdr = "    header_value_env: TEST_HEADER_VALUE\n"
	tests := []struct {
		name    string
		replace [2]string
		want    string
	}{
		{"host and url", [2]string{urlLine, urlLine + "    host: 192.0.2.10\n"}, "only one of host and url"},
		{"no host and no url", [2]string{urlLine, ""}, "host or url"},
		{"url Target with a Profile with no http", [2]string{"    profile: device\n    url", "    profile: snmp\n    url"}, `profile "snmp" has no http`},
		{"host Target with an http Profile", [2]string{"    profile: snmp\n    community_env", "    profile: device\n    community_env"}, `profile "device" has http`},
		{"url Target with a community", [2]string{hdr, hdr + "    community_env: TEST_COMMUNITY\n"}, "community_env"},
		{"http with no path", [2]string{"      cpu: cpuUtilizationPct\n      memory_percent: memoryUtilizationPct\n      uptime: uptimeSec\n", "      cpu: ''\n"}, "one or more of cpu"},
		{"http with a path with an empty key", [2]string{"cpu: cpuUtilizationPct", "cpu: a..b"}, "dotted path"},
		{"http with tables", [2]string{"      uptime: uptimeSec\n", "      uptime: uptimeSec\n    tables:\n      - oid: .1.3.6.1.4.1.99.1.1\n        name_column: 1\n        value_column: 2\n        kind: fan\n"}, "tables"},
		{"http with a top-level cpu", [2]string{"  device:\n", "  device:\n    cpu: ucd\n"}, "cpu"},
		{"http with a top-level memory", [2]string{"  device:\n", "  device:\n    memory: ucd\n"}, "memory"},
		{"unknown http key", [2]string{"      uptime: uptimeSec\n", "      uptime: uptimeSec\n      cpu_pct: x\n"}, "cpu_pct"},
		{"scheme ftp", [2]string{"https://192.0.2.10", "ftp://192.0.2.10"}, "http or https"},
		{"no scheme", [2]string{"https://192.0.2.10", "192.0.2.10"}, "url"},
		{"user and password", [2]string{"https://192.0.2.10", "https://user:pass@192.0.2.10"}, "use header"},
		{"bad header name", [2]string{"X-API-KEY", "X API KEY"}, "header"},
		{"no value key", [2]string{hdr, ""}, "one of header_value_env"},
		{"2 value keys", [2]string{hdr, hdr + "    header_value_file: /nonexistent\n"}, "only one of header_value_env"},
		{"value key with no header", [2]string{"    header: X-API-KEY\n", ""}, "need header"},
		{"empty value", [2]string{"TEST_HEADER_VALUE", "TEST_EMPTY"}, "TEST_EMPTY is empty"},
		{"unset value", [2]string{"TEST_HEADER_VALUE", "TEST_UNSET"}, "TEST_UNSET is empty"},
		{"value with a line break", [2]string{"TEST_HEADER_VALUE", "TEST_LINES"}, "line break"},
		{"value with a control character", [2]string{"TEST_HEADER_VALUE", "TEST_CTRL"}, "control character"},
		{"header on a host Target", [2]string{"    community_env: TEST_COMMUNITY\n", "    community_env: TEST_COMMUNITY\n    header: X-A\n"}, "need url"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Replace(httpConfig, tc.replace[0], tc.replace[1], 1)
			require.NotEqual(t, httpConfig, text, "the replace must change the config")
			_, err := ParseConfig([]byte(text))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), "query-secret")
			assert.NotContains(t, err.Error(), "header-secret")
			assert.NotContains(t, err.Error(), "user:pass")
		})
	}
}

func TestHeaderValueFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(path, []byte("file-secret\n"), 0o600))
	text := strings.Replace(httpConfig, "header_value_env: TEST_HEADER_VALUE", "header_value_file: "+path, 1)
	t.Setenv("TEST_COMMUNITY", "community-secret")
	cfg, err := ParseConfig([]byte(text))
	require.NoError(t, err)
	assert.Equal(t, "file-secret", cfg.Targets[0].HeaderValue)

	require.NoError(t, os.WriteFile(path, []byte(" \n"), 0o600))
	_, err = ParseConfig([]byte(text))
	require.ErrorContains(t, err, "is empty")
}

// Guard: no print form of TargetConfig shows the header value or the query.
func TestTargetConfigHidesSecrets(t *testing.T) {
	t.Setenv("TEST_HEADER_VALUE", "header-secret")
	t.Setenv("TEST_COMMUNITY", "community-secret")
	cfg, err := ParseConfig([]byte(httpConfig))
	require.NoError(t, err)
	tc := cfg.Targets[0]
	var logs strings.Builder
	slog.New(slog.NewTextHandler(&logs, nil)).Info("x", "target", tc)
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("x", "target", tc)
	for _, out := range []string{
		tc.String(), tc.GoString(), logs.String(),
		fmt.Sprintf("%v|%+v|%#v|%s|%q", tc, tc, tc, tc, tc),
		fmt.Sprintf("%v|%+v|%#v", &tc, &tc, &tc),
	} {
		assert.NotContains(t, out, "header-secret")
		assert.NotContains(t, out, "query-secret")
		assert.NotContains(t, out, "token=")
		assert.Contains(t, out, "https://192.0.2.10/stats/latest")
	}
}
