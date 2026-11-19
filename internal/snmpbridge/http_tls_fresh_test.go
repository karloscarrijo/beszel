package snmpbridge

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testCert makes a self-signed certificate for 127.0.0.1 and api.example.test.
func testCert(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "test api"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"api.example.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// tlsServer starts a TLS server that gives the certificate in *cert.
func tlsServer(t *testing.T, cert *atomic.Pointer[tls.Certificate], body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	srv.Listener = tls.NewListener(srv.Listener, &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert.Load(), nil },
	})
	srv.Start()
	srv.URL = strings.Replace(srv.URL, "http://", "https://", 1)
	t.Cleanup(srv.Close)
	return srv
}

func writeFile(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, b, 0o600))
	return p
}

const tlsBody = `{"cpuUtilizationPct":7,"memoryUtilizationPct":8,"uptimeSec":9}`

func tlsConfigText(url, extra string) string {
	return `
profiles:
  device:
    http:
      cpu: cpuUtilizationPct
      memory_percent: memoryUtilizationPct
      uptime: uptimeSec
targets:
  - id: sw-1
    name: Switch 1
    profile: device
    url: ` + url + "\n" + extra
}

// bridgeSource makes the Scheduler of the first Target of a config text, and its logs.
func bridgeSource(t *testing.T, text string) (*Scheduler, *syncBuf) {
	t.Helper()
	cfg, err := ParseConfig([]byte(text))
	require.NoError(t, err)
	logs := &syncBuf{}
	b := NewBridge(cfg, &Settings{}, nil, slog.New(slog.NewTextHandler(logs, nil)))
	s := b.Source(b.Targets[0]).(*Scheduler)
	s.MinGap = 0
	return s, logs
}

func readTimes(s *Scheduler, n int) {
	for range n {
		_, _ = s.Data(common.DataRequestOptions{})
	}
}

// Guard: a private certificate is good with ca_file, and fails without it.
func TestHTTPCAFile(t *testing.T) {
	cert, certPEM := testCert(t)
	var cur atomic.Pointer[tls.Certificate]
	cur.Store(&cert)
	srv := tlsServer(t, &cur, tlsBody)
	caFile := writeFile(t, "ca.pem", append([]byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"), certPEM...))
	read := func(extra string) (*Scheduler, *syncBuf) {
		return bridgeSource(t, tlsConfigText(srv.URL, extra))
	}

	s, _ := read("    ca_file: " + caFile + "\n")
	data, err := s.Data(common.DataRequestOptions{})
	require.NoError(t, err, "ca_file, and a block that is not a certificate is ignored")
	assert.Equal(t, 7.0, data.Stats.Cpu)

	s, _ = read("    ca_file: " + caFile + "\n    tls_server_name: api.example.test\n")
	_, err = s.Data(common.DataRequestOptions{})
	require.NoError(t, err, "ca_file and tls_server_name with an address in the URL")

	s, logs := read("")
	_, err = s.Data(common.DataRequestOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "certificate")
	readTimes(s, 3)
	assert.True(t, s.Failed())
	assert.Contains(t, logs.String(), "certificate")

	s, _ = read("    tls_server_name: api.example.test\n")
	_, err = s.Data(common.DataRequestOptions{})
	require.Error(t, err, "tls_server_name with no ca_file keeps the check on")
}

// Guard (failure mode 4): a certificate that ca_file does not hold fails the read, and the log has "certificate".
func TestHTTPCertificateChange(t *testing.T) {
	certA, pemA := testCert(t)
	certB, _ := testCert(t)
	var cur atomic.Pointer[tls.Certificate]
	cur.Store(&certA)
	srv := tlsServer(t, &cur, tlsBody)
	s, logs := bridgeSource(t, tlsConfigText(srv.URL, "    ca_file: "+writeFile(t, "ca.pem", pemA)+"\n"))
	readTimes(s, 1)
	require.False(t, s.Failed())

	cur.Store(&certB)
	// why: a kept connection does not do a new handshake.
	srv.CloseClientConnections()
	readTimes(s, 3)
	assert.True(t, s.Failed())
	assert.Contains(t, logs.String(), `msg="read failed" target=sw-1`)
	assert.Contains(t, logs.String(), "certificate")
}

func freshBody(at string) string {
	return fmt.Sprintf(`{"time":%q,"cpuUtilizationPct":7,"memoryUtilizationPct":8,"uptimeSec":9}`, at)
}

const freshProfile = "      fresh:\n        path: time\n        max_age: 90\n"

func freshConfig(url string) string {
	return strings.Replace(tlsConfigText(url, ""), "      uptime: uptimeSec\n", "      uptime: uptimeSec\n"+freshProfile, 1)
}

// Guard (failure mode 1): with fresh, an old time fails the read, also when the reply has all values.
func TestHTTPFresh(t *testing.T) {
	now := time.Now()
	zone := time.FixedZone("x", 5*3600+1800)
	good := []string{
		now.UTC().Format(time.RFC3339),
		now.In(zone).Format(time.RFC3339Nano),
		now.Add(-30 * time.Second).UTC().Format(time.RFC3339Nano),
		now.Add(30 * time.Second).UTC().Format(time.RFC3339),
	}
	for _, at := range good {
		srv := jsonServer(t, freshBody(at))
		s, _ := bridgeSource(t, freshConfig(srv.URL))
		_, err := s.Data(common.DataRequestOptions{})
		assert.NoError(t, err, at)
	}
	bad := map[string]string{
		"old":             freshBody(now.Add(-10 * time.Minute).UTC().Format(time.RFC3339)),
		"future":          freshBody(now.Add(10 * time.Minute).UTC().Format(time.RFC3339)),
		"old with offset": freshBody(now.Add(-10 * time.Minute).In(zone).Format(time.RFC3339)),
		"missing":         tlsBody,
		"not a time":      freshBody("yesterday"),
		"date only":       freshBody(now.UTC().Format("2006-01-02")),
		"number":          `{"time":1700000000,"cpuUtilizationPct":7}`,
		"null":            `{"time":null,"cpuUtilizationPct":7}`,
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			srv := jsonServer(t, body)
			s, logs := bridgeSource(t, freshConfig(srv.URL))
			_, err := s.Data(common.DataRequestOptions{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), `"sw-1"`)
			assert.Contains(t, err.Error(), `"time"`)
			assert.NotContains(t, err.Error(), "yesterday")
			readTimes(s, 3)
			assert.True(t, s.Failed())
			assert.NotContains(t, logs.String(), "yesterday")
		})
	}
}

// Guard: memory_total_mib gives the memory bytes by the formula, and buffer plus cached stays 0.
func TestHTTPMemoryBytes(t *testing.T) {
	read := func(body string, mib uint64) *HTTPSource {
		srv := jsonServer(t, body)
		s := httpSource(srv.URL, unifiProfile)
		s.Target.MemoryTotalMiB = mib
		return s
	}
	data, err := read(tlsBody, 2048).Read(context.Background(), common.DataRequestOptions{})
	require.NoError(t, err)
	total := uint64(2048) << 20
	assert.Equal(t, [4]uint64{total, uint64(math.Round(float64(total) * 8 / 100)), 0, 0}, data.Stats.MemBytes)
	assert.Equal(t, 8.0, data.Stats.MemPct)

	data, err = read(`{"cpuUtilizationPct":1,"memoryUtilizationPct":33.33}`, 1).Read(context.Background(), common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Equal(t, [4]uint64{1048576, 349490, 0, 0}, data.Stats.MemBytes, "round(1048576 * 33.33 / 100)")

	data, err = read(`{"cpuUtilizationPct":1}`, 2048).Read(context.Background(), common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Equal(t, [4]uint64{}, data.Stats.MemBytes, "no memory percent gives no bytes")

	data, err = read(tlsBody, 0).Read(context.Background(), common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Equal(t, [4]uint64{}, data.Stats.MemBytes, "no key gives only the percent")
	assert.Equal(t, 8.0, data.Stats.MemPct)
}

// Guard: the config checks of ca_file, tls_server_name, fresh and memory_total_mib name the item.
func TestParseConfigHTTPTLSFreshMemory(t *testing.T) {
	_, certPEM := testCert(t)
	good := writeFile(t, "ca.pem", certPEM)
	junk := writeFile(t, "junk.pem", []byte("not a pem file"))
	notCert := writeFile(t, "key.pem", []byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"))
	t.Setenv("TEST_HEADER_VALUE", "header-secret")
	t.Setenv("TEST_COMMUNITY", "community-secret")

	cfg, err := ParseConfig([]byte(strings.Replace(httpConfig, "    header: X-API-KEY\n",
		"    ca_file: "+good+"\n    tls_server_name: api.example.test\n    memory_total_mib: 512\n    header: X-API-KEY\n", 1)))
	require.NoError(t, err)
	assert.NotNil(t, cfg.Targets[0].CAPool)
	assert.Equal(t, "api.example.test", cfg.Targets[0].TLSServerName)
	assert.Equal(t, uint64(512), cfg.Targets[0].MemoryTotalMiB)

	cfg, err = ParseConfig([]byte(strings.Replace(httpConfig, "      uptime: uptimeSec\n", "      uptime: uptimeSec\n      fresh:\n        path: a.b\n        max_age: 30\n", 1)))
	require.NoError(t, err)
	assert.Equal(t, &HTTPFresh{Path: "a.b", MaxAge: 30 * time.Second}, cfg.Profiles["device"].HTTP.Fresh)

	const hdr = "    header: X-API-KEY\n"
	const upt = "      uptime: uptimeSec\n"
	target := func(extra string) [2]string { return [2]string{hdr, extra + hdr} }
	profile := func(extra string) [2]string { return [2]string{upt, upt + extra} }
	tests := []struct {
		name    string
		replace [2]string
		want    []string
	}{
		{"ca_file that cannot be read", target("    ca_file: /nonexistent/ca.pem\n"), []string{"ca_file", "/nonexistent/ca.pem"}},
		{"ca_file with no PEM", target("    ca_file: " + junk + "\n"), []string{"ca_file", "no valid certificate"}},
		{"ca_file with a key only", target("    ca_file: " + notCert + "\n"), []string{"ca_file", "no valid certificate"}},
		{"ca_file with an http url", target("    ca_file: " + good + "\n"), []string{"ca_file", "https"}},
		{"tls_server_name with an http url", target("    tls_server_name: a\n"), []string{"tls_server_name", "https"}},
		{"fresh with no path", profile("      fresh:\n        max_age: 30\n"), []string{`profile "device"`, "fresh", "path"}},
		{"fresh with no max_age", profile("      fresh:\n        path: t\n"), []string{`profile "device"`, "max_age"}},
		{"fresh with no value", profile("      fresh:\n"), []string{`profile "device"`, "fresh"}},
		{"max_age 0", profile("      fresh:\n        path: t\n        max_age: 0\n"), []string{"max_age"}},
		{"max_age negative", profile("      fresh:\n        path: t\n        max_age: -5\n"), []string{"max_age"}},
		{"max_age with a fraction", profile("      fresh:\n        path: t\n        max_age: 1.5\n"), []string{"max_age"}},
		{"max_age text", profile("      fresh:\n        path: t\n        max_age: '30'\n"), []string{"max_age"}},
		{"max_age too large", profile("      fresh:\n        path: t\n        max_age: 99999999999999\n"), []string{"max_age"}},
		{"memory_total_mib 0", target("    memory_total_mib: 0\n"), []string{"memory_total_mib"}},
		{"memory_total_mib with a fraction", target("    memory_total_mib: 1.5\n"), []string{"memory_total_mib"}},
		{"memory_total_mib text", target("    memory_total_mib: lots\n"), []string{"memory_total_mib"}},
		{"memory_total_mib too large", target("    memory_total_mib: 99999999999999999\n"), []string{"memory_total_mib"}},
		{"memory_total_mib with no memory_percent", target("    memory_total_mib: 512\n"), []string{"memory_total_mib", "memory_percent", `"usw-flex-mini"`}},
		{"ca_file on a host Target", [2]string{"    community_env: TEST_COMMUNITY\n", "    community_env: TEST_COMMUNITY\n    ca_file: " + good + "\n"}, []string{"ca_file", "need url"}},
		{"tls_server_name on a host Target", [2]string{"    community_env: TEST_COMMUNITY\n", "    community_env: TEST_COMMUNITY\n    tls_server_name: a\n"}, []string{"tls_server_name", "need url"}},
		{"memory_total_mib with no value", target("    memory_total_mib:\n"), []string{"memory_total_mib", "whole number"}},
		{"memory_total_mib with no value on a host Target", [2]string{"    community_env: TEST_COMMUNITY\n", "    community_env: TEST_COMMUNITY\n    memory_total_mib:\n"}, []string{"memory_total_mib", "need url"}},
		{"memory_total_mib on a host Target", [2]string{"    community_env: TEST_COMMUNITY\n", "    community_env: TEST_COMMUNITY\n    memory_total_mib: 5\n"}, []string{"memory_total_mib", "need url"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Replace(httpConfig, tc.replace[0], tc.replace[1], 1)
			switch tc.name {
			case "ca_file with an http url", "tls_server_name with an http url":
				text = strings.Replace(text, "https://192.0.2.10", "http://192.0.2.10", 1)
			case "memory_total_mib with no memory_percent":
				text = strings.Replace(text, "      memory_percent: memoryUtilizationPct\n", "", 1)
			}
			require.NotEqual(t, httpConfig, text)
			_, err := ParseConfig([]byte(text))
			require.Error(t, err)
			for _, w := range tc.want {
				assert.Contains(t, err.Error(), w)
			}
			assert.NotContains(t, err.Error(), "header-secret")
			assert.NotContains(t, err.Error(), "query-secret")
		})
	}
}
