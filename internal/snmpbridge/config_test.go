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

const validConfig = `
profiles:
  switch:
    cpu: ucd
    memory: ucd
    tables:
      - oid: .1.3.6.1.4.1.99.1.1
        name_column: 2
        value_column: 5
        kind: fan
        scale: 10
        match: '^Fan'
        exclude: 'spare$'
  plain:
    tables:
      - oid: 1.3.6.1.4.1.99.2.1
        name_column: 1
        value_column: 3
        kind: temperature
targets:
  - id: sw-1
    name: Switch 1
    host: 192.0.2.10
    profile: switch
    community_env: TEST_COMMUNITY
  - id: sw-2
    name: Switch 2
    host: 192.0.2.11
    port: 1161
    version: 2c
    profile: plain
    community_env: TEST_COMMUNITY
`

func loadFrom(t *testing.T, yamlText string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bridge.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yamlText), 0o600))
	t.Setenv(ConfigEnv, path)
	return LoadConfig()
}

func TestLoadConfigValid(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "s3cret-community")
	cfg, err := loadFrom(t, validConfig)
	require.NoError(t, err)
	require.Len(t, cfg.Targets, 2)
	t1 := cfg.Targets[0]
	assert.Equal(t, "sw-1", t1.ID)
	assert.Equal(t, 161, t1.Port)
	assert.Equal(t, "2c", t1.Version)
	assert.Equal(t, "s3cret-community", t1.Community)
	assert.Equal(t, 1161, cfg.Targets[1].Port)

	p := cfg.Profiles["switch"]
	assert.Equal(t, "ucd", p.CPU)
	assert.Equal(t, "ucd", p.Memory)
	require.Len(t, p.Tables, 1)
	rule := p.Tables[0]
	assert.Equal(t, ".1.3.6.1.4.1.99.1.1", rule.OID)
	assert.Equal(t, 2, rule.NameColumn)
	assert.Equal(t, 5, rule.ValueColumn)
	assert.Equal(t, KindFan, rule.Kind)
	assert.Equal(t, 10.0, rule.Scale)
	assert.True(t, rule.Match.MatchString("Fan 1"))
	assert.True(t, rule.Exclude.MatchString("Fan spare"))

	plain := cfg.Profiles["plain"].Tables[0]
	assert.Equal(t, ".1.3.6.1.4.1.99.2.1", plain.OID, "a leading dot is added")
	assert.Equal(t, 1.0, plain.Scale, "the default scale is 1")
	assert.Nil(t, plain.Match)
}

func TestLoadConfigErrors(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "s3cret-community")
	// each case changes the valid config and names the text that the error holds
	tests := []struct {
		name    string
		replace [2]string
		want    string
	}{
		{"unknown top-level key", [2]string{"\nprofiles:", "\nbogus_top: 1\nprofiles:"}, "bogus_top"},
		{"unknown target key", [2]string{"    host: 192.0.2.10", "    host: 192.0.2.10\n    hots: x"}, "hots"},
		{"unknown table key", [2]string{"        kind: fan", "        kind: fan\n        sacle: 2"}, "sacle"},
		{"unknown profile key", [2]string{"    cpu: ucd\n", "    cpu: ucd\n    gpu: ucd\n"}, "gpu"},
		{"invalid regexp", [2]string{"match: '^Fan'", "match: '(['"}, `profile "switch"`},
		{"missing oid", [2]string{"      - oid: .1.3.6.1.4.1.99.1.1\n        name_column: 2", "      - name_column: 2"}, "oid"},
		{"missing name_column", [2]string{"        name_column: 2\n        value_column: 5", "        value_column: 5"}, "name_column"},
		{"missing kind", [2]string{"        kind: fan\n", ""}, "kind"},
		{"bad kind", [2]string{"kind: fan", "kind: voltage"}, "kind"},
		{"zero scale", [2]string{"scale: 10", "scale: 0"}, "scale"},
		{"bad cpu value", [2]string{"cpu: ucd", "cpu: other"}, `profile "switch"`},
		{"bad memory value", [2]string{"memory: ucd", "memory: other"}, `profile "switch"`},
		{"missing target id", [2]string{"  - id: sw-1\n", "  - "}, "id"},
		{"bad id characters", [2]string{"id: sw-1", "id: SW_1"}, "SW_1"},
		{"missing name", [2]string{"    name: Switch 1\n", ""}, "name"},
		{"missing host", [2]string{"    host: 192.0.2.10\n", ""}, "host"},
		{"missing profile field", [2]string{"    profile: switch\n", ""}, "profile"},
		{"version 3", [2]string{"version: 2c", "version: 3"}, "version"},
		{"unknown profile", [2]string{"profile: plain", "profile: nope"}, "nope"},
		{"no community", [2]string{"    profile: switch\n    community_env: TEST_COMMUNITY\n", "    profile: switch\n"}, "community"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Replace(validConfig, tc.replace[0], tc.replace[1], 1)
			require.NotEqual(t, validConfig, text, "the replace must change the config")
			_, err := loadFrom(t, text)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), "s3cret-community")
		})
	}
}

func TestLoadConfigStartErrors(t *testing.T) {
	t.Setenv(ConfigEnv, "")
	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), ConfigEnv)

	t.Setenv(ConfigEnv, filepath.Join(t.TempDir(), "missing.yaml"))
	_, err = LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), ConfigEnv)
	assert.Contains(t, err.Error(), "missing.yaml")

	_, err = loadFrom(t, "")
	require.Error(t, err)

	_, err = loadFrom(t, "profiles:\n  a:\n    cpu: ucd\ntargets: []\n")
	require.Error(t, err)
}

func TestLoadConfigDuplicateID(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "c")
	text := strings.Replace(validConfig, "id: sw-2", "id: sw-1", 1)
	_, err := loadFrom(t, text)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sw-1")
}

func TestLoadConfigDuplicateIDBeforeOtherError(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "c")
	text := strings.Replace(validConfig, "id: sw-2", "id: sw-1", 1)
	text = strings.Replace(text, "    port: 1161\n    version: 2c\n    profile: plain\n    community_env: TEST_COMMUNITY", "    profile: plain\n    community_file: /nonexistent/community", 1)
	_, err := loadFrom(t, text)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not unique")
}

func TestLoadConfigStrictInput(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "c")
	_, err := loadFrom(t, validConfig+"---\ntargets: []\n")
	require.Error(t, err, "a second document is not dropped")

	_, err = loadFrom(t, strings.Replace(validConfig, "scale: 10", "scale: .inf", 1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scale")
}

func TestLoadConfigErrorIsStable(t *testing.T) {
	text := "profiles:\n  b:\n    cpu: foo\n  a:\n    cpu: bar\ntargets: []\n"
	for range 20 {
		_, err := loadFrom(t, text)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `profile "a"`)
	}
}

func TestCommunitySources(t *testing.T) {
	const secret = "s3cret-community"
	dir := t.TempDir()
	file := filepath.Join(dir, "community")
	require.NoError(t, os.WriteFile(file, []byte(secret+"\n"), 0o600))
	text := func(target string) string {
		return "profiles:\n  p:\n    cpu: ucd\ntargets:\n  - id: a\n    name: A\n    host: 192.0.2.1\n    profile: p\n" + target
	}

	t.Run("file", func(t *testing.T) {
		cfg, err := loadFrom(t, text("    community_file: "+file+"\n"))
		require.NoError(t, err)
		assert.Equal(t, secret, cfg.Targets[0].Community)
	})
	t.Run("env", func(t *testing.T) {
		t.Setenv("TEST_COMMUNITY", secret)
		cfg, err := loadFrom(t, text("    community_env: TEST_COMMUNITY\n"))
		require.NoError(t, err)
		assert.Equal(t, secret, cfg.Targets[0].Community)
	})
	t.Run("empty env stops the start", func(t *testing.T) {
		t.Setenv("TEST_COMMUNITY", "")
		_, err := loadFrom(t, text("    community_env: TEST_COMMUNITY\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "TEST_COMMUNITY")
	})
	t.Run("missing file stops the start", func(t *testing.T) {
		_, err := loadFrom(t, text("    community_file: "+filepath.Join(dir, "none")+"\n"))
		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
	})
	t.Run("empty file stops the start", func(t *testing.T) {
		empty := filepath.Join(dir, "empty")
		require.NoError(t, os.WriteFile(empty, []byte("\n"), 0o600))
		_, err := loadFrom(t, text("    community_file: "+empty+"\n"))
		require.Error(t, err)
	})
	t.Run("both set", func(t *testing.T) {
		t.Setenv("TEST_COMMUNITY", secret)
		_, err := loadFrom(t, text("    community_env: TEST_COMMUNITY\n    community_file: "+file+"\n"))
		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
	})
	t.Run("a format verb and a log line hide it", func(t *testing.T) {
		t.Setenv("TEST_COMMUNITY", secret)
		cfg, err := loadFrom(t, text("    community_env: TEST_COMMUNITY\n"))
		require.NoError(t, err)
		tc := cfg.Targets[0]
		for _, s := range []string{fmt.Sprintf("%v", tc), fmt.Sprintf("%+v", tc), fmt.Sprintf("%#v", tc), fmt.Sprint(&tc)} {
			assert.NotContains(t, s, secret)
		}
		var buf strings.Builder
		slog.New(slog.NewTextHandler(&buf, nil)).Info("target", "t", tc)
		assert.NotContains(t, buf.String(), secret)
	})
}
