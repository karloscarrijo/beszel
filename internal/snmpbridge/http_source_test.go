package snmpbridge

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func jsonServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fileServer(t *testing.T, path string) *httptest.Server {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return jsonServer(t, string(b))
}

var unifiProfile = HTTPProfile{CPU: "cpuUtilizationPct", MemoryPercent: "memoryUtilizationPct", Uptime: "uptimeSec"}

func httpSource(url string, p HTTPProfile) *HTTPSource {
	return &HTTPSource{
		Target:  TargetConfig{Target: Target{ID: "usw-flex-mini", Name: "USW Flex Mini"}, URL: url},
		Profile: p,
		Client:  newHTTPClient(),
	}
}

func TestHTTPReadRecordedReplies(t *testing.T) {
	srv := fileServer(t, "testdata/http-device.json")
	data, err := httpSource(srv.URL, unifiProfile).Read(context.Background(), common.DataRequestOptions{IncludeDetails: true})
	require.NoError(t, err)
	assert.Equal(t, 11.2, data.Stats.Cpu)
	assert.Equal(t, 83.6, data.Stats.MemPct)
	assert.Equal(t, [4]uint64{}, data.Stats.MemBytes, "the byte fields stay 0")
	assert.Equal(t, 11.2, data.Info.Cpu)
	assert.Equal(t, 83.6, data.Info.MemPct)
	assert.Equal(t, uint64(168902), data.Info.Uptime)
	assert.Equal(t, "USW Flex Mini", data.Details.Hostname)

	// the old API: strings, some with spaces, and nested paths with a hyphen
	old := fileServer(t, "testdata/http-device-old.json")
	data, err = httpSource(old.URL, HTTPProfile{CPU: "system-stats.cpu", MemoryPercent: "system-stats.mem", Uptime: "uptime"}).
		Read(context.Background(), common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2.6, data.Stats.Cpu)
	assert.Equal(t, 80.3, data.Stats.MemPct)
	assert.Nil(t, data.Details)

	// a nested path
	data, err = httpSource(srv.URL, HTTPProfile{Uptime: "uplink.rxRateBps"}).Read(context.Background(), common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Equal(t, uint64(3400), data.Info.Uptime)
}

// Guard (failure mode 5): a bad value gives no data point, never a false 0.
func TestHTTPReadValues(t *testing.T) {
	p := HTTPProfile{CPU: "cpu", MemoryPercent: "mem", Uptime: "up"}
	tests := []struct {
		name       string
		body       string
		cpu, mem   float64
		up         uint64
		wantErr    bool
		wantNoCPU  bool
		wantNoMem  bool
		wantNoData bool
	}{
		{name: "numbers", body: `{"cpu":12.345,"mem":50,"up":100}`, cpu: 12.35, mem: 50, up: 100},
		{name: "string with spaces", body: `{"cpu":"  2.6","mem":" 3 ","up":"7"}`, cpu: 2.6, mem: 3, up: 7},
		{name: "both ends of a percent", body: `{"cpu":0,"mem":100,"up":0}`, cpu: 0, mem: 100, up: 0},
		{name: "missing cpu", body: `{"mem":50,"up":1}`, mem: 50, up: 1, wantNoCPU: true},
		{name: "null cpu", body: `{"cpu":null,"mem":50,"up":1}`, mem: 50, up: 1, wantNoCPU: true},
		{name: "text cpu", body: `{"cpu":"high","mem":50,"up":1}`, mem: 50, up: 1, wantNoCPU: true},
		{name: "text with inf", body: `{"cpu":"Inf","mem":"NaN","up":1}`, up: 1, wantNoCPU: true, wantNoMem: true},
		{name: "hex string", body: `{"cpu":"0x10","mem":50,"up":1}`, mem: 50, up: 1, wantNoCPU: true},
		{name: "boolean cpu", body: `{"cpu":true,"mem":50,"up":1}`, mem: 50, up: 1, wantNoCPU: true},
		{name: "percent above 100", body: `{"cpu":100.5,"mem":50,"up":1}`, mem: 50, up: 1, wantNoCPU: true},
		{name: "percent below 0", body: `{"cpu":-1,"mem":50,"up":1}`, mem: 50, up: 1, wantNoCPU: true},
		{name: "path through a number", body: `{"cpu":{"a":1},"mem":5}`, cpu: 0, mem: 5, wantNoCPU: true},
		{name: "path through an array", body: `{"cpu":[1],"mem":5}`, mem: 5, wantNoCPU: true},
		{name: "fraction of uptime", body: `{"cpu":1,"up":99.9}`, cpu: 1, up: 99, wantNoMem: true},
		{name: "negative uptime", body: `{"cpu":1,"up":-5}`, cpu: 1, wantNoMem: true},
		{name: "uptime above the range", body: `{"cpu":1,"up":1e30}`, cpu: 1, wantNoMem: true},
		{name: "number above float range", body: `{"cpu":1e999,"mem":4}`, mem: 4, wantNoCPU: true},
		{name: "nothing left", body: `{"cpu":null,"mem":"x","up":-1}`, wantErr: true},
		{name: "array reply", body: `[1,2]`, wantErr: true},
		{name: "not JSON", body: `<html>`, wantErr: true},
		{name: "empty reply", body: ``, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := jsonServer(t, tc.body)
			data, err := httpSource(srv.URL, p).Read(context.Background(), common.DataRequestOptions{})
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, data)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.cpu, data.Stats.Cpu)
			assert.Equal(t, tc.mem, data.Stats.MemPct)
			assert.Equal(t, tc.up, data.Info.Uptime)
		})
	}
}

func TestHTTPReadNoDataError(t *testing.T) {
	srv := jsonServer(t, `{}`)
	_, err := httpSource(srv.URL, unifiProfile).Read(context.Background(), common.DataRequestOptions{})
	assert.ErrorContains(t, err, "no path of the profile gives a value")
}

// Guard (failure mode 3): a status that is not 2xx fails the read, and the
// error names the Target and the status.
func TestHTTPReadStatus(t *testing.T) {
	for _, code := range []int{401, 404, 500, 302} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://127.0.0.1:1/")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"cpuUtilizationPct":5}`))
		}))
		_, err := httpSource(srv.URL, unifiProfile).Read(context.Background(), common.DataRequestOptions{})
		srv.Close()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `http get of target "usw-flex-mini" failed: status `+strconv.Itoa(code))
	}
}

// Guard: the header goes in the request, and no redirect goes to a different host.
func TestHTTPNoRedirectAndHeader(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		assert.Empty(t, r.Header.Get("X-API-KEY"))
	}))
	defer other.Close()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-API-KEY")
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	s := httpSource(srv.URL, unifiProfile)
	s.Target.Header, s.Target.HeaderValue = "X-API-KEY", "header-secret"
	_, err := s.Read(context.Background(), common.DataRequestOptions{})
	require.ErrorContains(t, err, "status 302")
	assert.Equal(t, "header-secret", got)
	assert.Zero(t, hits.Load(), "the redirect target gets no request")
}

// Guard: the proxy variables of the environment are not used.
func TestHTTPNoProxy(t *testing.T) {
	var hits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"cpuUtilizationPct":5}`))
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	s := httpSource("http://device.example.invalid/stats", unifiProfile)
	_, err := s.Read(context.Background(), common.DataRequestOptions{})
	require.Error(t, err)
	assert.Zero(t, hits.Load(), "the request does not go to the proxy")
}

// Guard: a reply above 1 MiB fails the read, and 1 MiB is still valid.
func TestHTTPReplyLimit(t *testing.T) {
	pad := func(n int) string { return `{"cpuUtilizationPct":5,"pad":"` + strings.Repeat("x", n) + `"}` }
	over := jsonServer(t, pad(MaxHTTPReply))
	_, err := httpSource(over.URL, unifiProfile).Read(context.Background(), common.DataRequestOptions{})
	require.ErrorContains(t, err, "1 MiB")

	body := pad(0)
	exact := jsonServer(t, pad(MaxHTTPReply-len(body)))
	data, err := httpSource(exact.URL, unifiProfile).Read(context.Background(), common.DataRequestOptions{})
	require.NoError(t, err)
	assert.Equal(t, 5.0, data.Stats.Cpu)
}

// Guard: no error text holds the query of the URL or the header value, also
// when the connection fails.
func TestHTTPErrorsHideSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	url := srv.URL + "/stats?token=query-secret"
	for _, closed := range []bool{false, true} {
		if closed {
			srv.Close()
		}
		s := httpSource(url, unifiProfile)
		s.Target.Header, s.Target.HeaderValue = "X-API-KEY", "header-secret"
		_, err := s.Read(context.Background(), common.DataRequestOptions{})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "query-secret")
		assert.NotContains(t, err.Error(), "header-secret")
	}
	// a timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	_, err := httpSource(slow.URL+"/?token=query-secret", unifiProfile).Read(ctx, common.DataRequestOptions{})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "query-secret")
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Guard (failure mode 2): while the API host is stopped, each Target is down
// with one log line each. When it starts again, each Target is up with no action.
func TestHTTPBridgeServerRestart(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"cpuUtilizationPct":7,"memoryUtilizationPct":8,"uptimeSec":9}`))
	})
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)

	text := strings.NewReplacer("ADDR", addr).Replace(`
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
    url: http://ADDR/one?token=query-secret
  - id: sw-2
    name: Switch 2
    profile: device
    url: http://ADDR/two?token=query-secret
`)
	cfg, err := ParseConfig([]byte(text))
	require.NoError(t, err)
	var logs syncBuf
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	b := NewBridge(cfg, &Settings{}, nil, logger)
	var sources []*Scheduler
	for _, tg := range b.Targets {
		s := b.Source(tg).(*Scheduler)
		s.MinGap = 0
		sources = append(sources, s)
	}
	read := func() {
		for _, s := range sources {
			_, _ = s.Data(common.DataRequestOptions{})
		}
	}
	read()
	for _, s := range sources {
		assert.False(t, s.Failed())
	}

	require.NoError(t, srv.Close())
	for range 3 {
		read()
	}
	for i, s := range sources {
		assert.True(t, s.Failed(), "target %d is down", i)
		_, err := s.Data(common.DataRequestOptions{})
		assert.Error(t, err)
	}
	for _, id := range []string{"sw-1", "sw-2"} {
		assert.Equal(t, 1, strings.Count(logs.String(), `msg="read failed" target=`+id), "one line for %s:\n%s", id, logs.String())
	}
	assert.NotContains(t, logs.String(), "query-secret")

	ln, err = net.Listen("tcp", addr)
	require.NoError(t, err)
	srv = &http.Server{Handler: handler}
	go srv.Serve(ln)
	defer srv.Close()
	read()
	for i, s := range sources {
		assert.False(t, s.Failed(), "target %d is up again", i)
		data, err := s.Data(common.DataRequestOptions{})
		require.NoError(t, err)
		assert.Equal(t, 7.0, data.Stats.Cpu)
	}
	assert.Equal(t, 2, strings.Count(logs.String(), "read is good again"))
}

// Guard (failure mode 3): the log line of a failed read names the Target and the status.
func TestHTTPBridgeStatusLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	cfg, err := ParseConfig([]byte(`
profiles:
  device:
    http:
      cpu: cpuUtilizationPct
targets:
  - id: usw-flex-mini
    name: USW Flex Mini
    profile: device
    url: ` + srv.URL + "\n"))
	require.NoError(t, err)
	var logs syncBuf
	b := NewBridge(cfg, &Settings{}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	s := b.Source(b.Targets[0]).(*Scheduler)
	_, err = s.Data(common.DataRequestOptions{})
	require.Error(t, err)
	assert.True(t, s.Failed())
	assert.Contains(t, logs.String(), `msg="read failed" target=usw-flex-mini`)
	assert.Contains(t, logs.String(), `http get of target \"usw-flex-mini\" failed: status 401`)
}
