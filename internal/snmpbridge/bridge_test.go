//go:build testing

package snmpbridge

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/system"
	beszelTests "github.com/henrygd/beszel/internal/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

const permToken = "perm-token-1"

var (
	alpha = Target{ID: "t-alpha", Name: "Alpha"}
	beta  = Target{ID: "t-beta", Name: "Beta"}
)

// testHub is a real hub (the real WebSocket handler) on a test HTTP server.
type testHub struct {
	t    *testing.T
	hub  *beszelTests.TestHub
	mux  http.Handler
	addr string
	srv  *httptest.Server
	keys []ssh.PublicKey
	user *core.Record
}

func newTestHub(t *testing.T) *testHub {
	t.Helper()
	hub, err := beszelTests.NewTestHub(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(hub.Cleanup)
	// why: StartHub binds the routes to OnServe, then fails because the test
	// app is not a PocketBase server. The error is expected.
	_ = hub.StartHub()
	user, err := beszelTests.CreateUser(hub, "bridge@example.com", "password123")
	require.NoError(t, err)

	router, err := apis.NewRouter(hub)
	require.NoError(t, err)
	h := &testHub{t: t, hub: hub, user: user}
	err = hub.OnServe().Trigger(&core.ServeEvent{App: hub, Router: router}, func(e *core.ServeEvent) error {
		mux, err := e.Router.BuildMux()
		h.mux = mux
		return err
	})
	require.NoError(t, err)

	signer, err := hub.Hub.GetSSHKey("")
	require.NoError(t, err)
	h.keys = []ssh.PublicKey{signer.PublicKey()}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	h.addr = ln.Addr().String()
	h.serve(ln)
	t.Cleanup(func() { h.srv.Close() })

	_, err = beszelTests.CreateRecord(hub, "universal_tokens", map[string]any{"token": permToken, "user": user.Id})
	require.NoError(t, err)
	return h
}

func (h *testHub) serve(ln net.Listener) {
	h.srv = httptest.NewUnstartedServer(h.mux)
	h.srv.Listener = ln
	h.srv.Start()
}

func (h *testHub) url() string { return "http://" + h.addr }

// stop closes the HTTP server and each WebSocket, like a stopped hub.
func (h *testHub) stop() {
	h.srv.Close()
	systems, _ := h.hub.FindAllRecords("systems")
	for _, rec := range systems {
		if sys, err := h.hub.GetSystemManager().GetSystem(rec.Id); err == nil && sys.WsConn != nil {
			sys.WsConn.Close(nil)
		}
	}
}

func (h *testHub) restart() {
	ln, err := net.Listen("tcp", h.addr)
	require.NoError(h.t, err)
	h.serve(ln)
}

func (h *testHub) systemNames() []string {
	recs, _ := h.hub.FindAllRecords("systems")
	var names []string
	for _, r := range recs {
		names = append(names, r.GetString("name"))
	}
	return names
}

func (h *testHub) count(collection string) int {
	n, _ := h.hub.CountRecords(collection)
	return int(n)
}

// countSource is a stub that counts the GetData calls of each Target.
type countSource struct {
	StubSource
	calls *atomic.Int64
}

func (s countSource) Data(opts common.DataRequestOptions) (*system.CombinedData, error) {
	s.calls.Add(1)
	return s.StubSource.Data(opts)
}

// testBridge builds a Bridge for the hub, with short backoff values.
type testBridge struct {
	*Bridge
	calls map[string]*atomic.Int64
	logs  *logCapture
}

func (h *testHub) bridge(token string, targets ...Target) *testBridge {
	tb := &testBridge{calls: map[string]*atomic.Int64{}, logs: &logCapture{}}
	for _, tg := range targets {
		tb.calls[tg.ID] = &atomic.Int64{}
	}
	tb.Bridge = &Bridge{
		HubURL:  h.url(),
		Token:   token,
		Keys:    h.keys,
		Targets: targets,
		Source: func(tg Target) Source {
			return countSource{StubSource: StubSource{Name: tg.Name}, calls: tb.calls[tg.ID]}
		},
		Backoff: Backoff{Initial: 20 * time.Millisecond, Max: 80 * time.Millisecond},
		Logger:  slog.New(tb.logs),
	}
	return tb
}

// start runs the bridge. The returned function stops it and waits.
func (tb *testBridge) start(t *testing.T) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tb.Run(ctx); close(done) }()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the bridge did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

// logCapture is a slog handler that keeps each record.
type logCapture struct {
	mu   sync.Mutex
	recs []logLine
}

type logLine struct {
	Level  slog.Level
	Msg    string
	Target string
	Text   string
}

func (l *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (l *logCapture) WithGroup(string) slog.Handler            { return l }
func (l *logCapture) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &logAttrs{parent: l, attrs: attrs}
}
func (l *logCapture) Handle(_ context.Context, r slog.Record) error {
	return l.add(r, nil)
}
func (l *logCapture) add(r slog.Record, pre []slog.Attr) error {
	line := logLine{Level: r.Level, Msg: r.Message}
	text := []string{r.Message}
	visit := func(a slog.Attr) {
		if a.Key == "target" {
			line.Target = a.Value.String()
		}
		text = append(text, a.Key+"="+a.Value.String())
	}
	for _, a := range pre {
		visit(a)
	}
	r.Attrs(func(a slog.Attr) bool { visit(a); return true })
	line.Text = strings.Join(text, " ")
	l.mu.Lock()
	l.recs = append(l.recs, line)
	l.mu.Unlock()
	return nil
}

type logAttrs struct {
	parent *logCapture
	attrs  []slog.Attr
}

func (l *logAttrs) Enabled(context.Context, slog.Level) bool { return true }
func (l *logAttrs) WithGroup(string) slog.Handler            { return l }
func (l *logAttrs) WithAttrs(a []slog.Attr) slog.Handler {
	return &logAttrs{parent: l.parent, attrs: append(append([]slog.Attr{}, l.attrs...), a...)}
}
func (l *logAttrs) Handle(_ context.Context, r slog.Record) error { return l.parent.add(r, l.attrs) }

// problems returns the lines of a Target at level WARN or above.
func (l *logCapture) problems(target string) []logLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []logLine
	for _, r := range l.recs {
		if r.Target == target && r.Level >= slog.LevelWarn {
			out = append(out, r)
		}
	}
	return out
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 15*time.Second, 25*time.Millisecond, what)
}

// Guard: a Target that does not show as a system, or a reply that stays out,
// means that the bridge no longer speaks the agent protocol.
func TestTwoTargetsGetOwnSystemsAndAnswerEachRequest(t *testing.T) {
	h := newTestHub(t)
	tb := h.bridge(permToken, alpha, beta)
	tb.start(t)

	eventually(t, "2 systems with the Target names", func() bool {
		return h.count("systems") == 2 && h.count("system_stats") >= 2
	})
	assert.ElementsMatch(t, []string{"Alpha", "Beta"}, h.systemNames())
	assert.GreaterOrEqual(t, tb.calls[alpha.ID].Load(), int64(1))
	assert.GreaterOrEqual(t, tb.calls[beta.ID].Load(), int64(1))

	// GetData returns the stub data: the hub saved the fans and the temperatures.
	stats, err := h.hub.FindAllRecords("system_stats")
	require.NoError(t, err)
	for _, s := range stats {
		raw := s.GetString("stats")
		assert.Contains(t, raw, `"FAN1":1200`)
		assert.Contains(t, raw, `"CPU":45.5`)
	}

	// Each other request, also an unknown number, gets a reply in < 1 s.
	systems, err := h.hub.FindAllRecords("systems")
	require.NoError(t, err)
	for _, rec := range systems {
		sys, err := h.hub.GetSystemManager().GetSystem(rec.Id)
		require.NoError(t, err)
		require.NotNil(t, sys.WsConn)
		for _, action := range []common.WebSocketAction{
			common.GetContainerLogs, common.GetContainerInfo, common.GetSmartData, common.GetSystemdInfo,
			common.GetZfsData, common.SyncNetworkMonitors, common.GetPackageUpdates, common.GetSystemdLogs, 200,
		} {
			start := time.Now()
			req, err := sys.WsConn.SendRequest(context.Background(), action, nil)
			require.NoError(t, err)
			select {
			case msg := <-req.ResponseCh:
				var resp common.AgentResponse
				require.NoError(t, cbor.Unmarshal(msg.Data.Bytes(), &resp))
				msg.Close()
				assert.NotNil(t, resp.Id, "action %d", action)
			case <-time.After(time.Second):
				t.Fatalf("no reply to action %d for %s in 1 s", action, rec.GetString("name"))
			}
			assert.Less(t, time.Since(start), time.Second)
			req.Cancel()
		}
	}
}

// Guard: a restart that makes a duplicate system loses the history of each
// Target and doubles the systems.
func TestRestartKeepsTheSystems(t *testing.T) {
	h := newTestHub(t)
	tb := h.bridge(permToken, alpha, beta)
	stop := tb.start(t)
	eventually(t, "2 systems", func() bool { return h.count("systems") == 2 && h.count("system_stats") >= 2 })
	stop()

	tb2 := h.bridge(permToken, alpha, beta)
	tb2.start(t)
	eventually(t, "each Target served again", func() bool {
		return tb2.calls[alpha.ID].Load() >= 1 && tb2.calls[beta.ID].Load() >= 1
	})
	assert.Equal(t, 2, h.count("systems"))
	assert.Equal(t, 2, h.count("fingerprints"))
	assert.ElementsMatch(t, []string{"Alpha", "Beta"}, h.systemNames())
}

// attempts counts the connect attempts of one Target, with the gate.
func attempts(tb *testBridge) *atomic.Int64 {
	var n atomic.Int64
	tb.Gate = func(Target) bool { n.Add(1); return true }
	return &n
}

// Guard: a log flood or a crash loop on a wrong KEY or a wrong HUB_URL.
func TestWrongKeyAndWrongURLLogOnceAndRetry(t *testing.T) {
	t.Run("wrong KEY", func(t *testing.T) {
		h := newTestHub(t)
		_, priv, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		other, err := ssh.NewPublicKey(priv.Public())
		require.NoError(t, err)
		tb := h.bridge(permToken, alpha, beta)
		tb.Keys = []ssh.PublicKey{other}
		n := attempts(tb)
		tb.start(t)
		eventually(t, "at least 6 connect attempts", func() bool { return n.Load() >= 6 })
		for _, tg := range []Target{alpha, beta} {
			lines := tb.logs.problems(tg.ID)
			require.Len(t, lines, 1, "%v", lines)
			assert.Contains(t, lines[0].Msg, "KEY")
		}
		assert.Equal(t, 0, h.count("systems"))
	})
	t.Run("wrong TOKEN", func(t *testing.T) {
		h := newTestHub(t)
		tb := h.bridge("unknown-token", alpha)
		n := attempts(tb)
		tb.start(t)
		eventually(t, "at least 4 connect attempts", func() bool { return n.Load() >= 4 })
		lines := tb.logs.problems(alpha.ID)
		require.Len(t, lines, 1, "%v", lines)
		assert.Contains(t, lines[0].Text, "the hub refused the token")
	})
	t.Run("handshake refused", func(t *testing.T) {
		h := newTestHub(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		tb := h.bridge(permToken, alpha)
		tb.HubURL = srv.URL
		n := attempts(tb)
		tb.start(t)
		eventually(t, "at least 4 connect attempts", func() bool { return n.Load() >= 4 })
		lines := tb.logs.problems(alpha.ID)
		require.Len(t, lines, 1, "%v", lines)
		assert.Contains(t, lines[0].Text, "the hub refused the connection")
	})
	t.Run("wrong HUB_URL", func(t *testing.T) {
		h := newTestHub(t)
		tb := h.bridge(permToken, alpha, beta)
		// A port that was open a moment ago refuses at once.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		tb.HubURL = "http://" + ln.Addr().String()
		require.NoError(t, ln.Close())
		n := attempts(tb)
		tb.start(t)
		eventually(t, "at least 6 connect attempts", func() bool { return n.Load() >= 6 })
		for _, tg := range []Target{alpha, beta} {
			lines := tb.logs.problems(tg.ID)
			require.Len(t, lines, 1, "%v", lines)
			assert.Contains(t, lines[0].Msg, "cannot connect")
		}
	})
}

// Guard: a backoff with no cap makes a Target wait too long after a hub restart, so a Status alert fires (beszel#026).
func TestBackoffStopsAtMax(t *testing.T) {
	h := newTestHub(t)
	tb := h.bridge(permToken, alpha)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tb.HubURL = "http://" + ln.Addr().String()
	require.NoError(t, ln.Close())
	var mu sync.Mutex
	var times []time.Time
	tb.Gate = func(Target) bool {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		return true
	}
	tb.start(t)
	eventually(t, "6 connect attempts", func() bool { mu.Lock(); defer mu.Unlock(); return len(times) >= 6 })
	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, times[1].Sub(times[0]), tb.Backoff.Initial-5*time.Millisecond, "first wait")
	for i := 4; i < len(times); i++ {
		gap := times[i].Sub(times[i-1])
		assert.GreaterOrEqual(t, gap, tb.Backoff.Max-5*time.Millisecond, "attempt %d", i)
		assert.Less(t, gap, tb.Backoff.Max*3, "wait after attempt %d must stop at Backoff.Max", i)
	}
}

// Guard: after a hub restart, a Target that stays off needs a restart of the
// bridge, and a log flood hides the cause.
func TestHubRestartConnectsAgain(t *testing.T) {
	h := newTestHub(t)
	tb := h.bridge(permToken, alpha, beta)
	n := attempts(tb)
	tb.start(t)
	eventually(t, "2 systems", func() bool { return h.count("systems") == 2 && h.count("system_stats") >= 2 })

	h.stop()
	before := n.Load()
	eventually(t, "more attempts while the hub is off", func() bool { return n.Load() >= before+8 })
	for _, tg := range []Target{alpha, beta} {
		lines := tb.logs.problems(tg.ID)
		require.Len(t, lines, 2, "one line for the close and one for the connect error: %v", lines)
		assert.Contains(t, lines[0].Msg, "closed")
		assert.Contains(t, lines[1].Msg, "cannot connect")
	}

	calls := map[string]int64{alpha.ID: tb.calls[alpha.ID].Load(), beta.ID: tb.calls[beta.ID].Load()}
	h.restart()
	eventually(t, "each Target served after the restart", func() bool {
		return tb.calls[alpha.ID].Load() > calls[alpha.ID] && tb.calls[beta.ID].Load() > calls[beta.ID]
	})
	assert.Equal(t, 2, h.count("systems"))
}

// Guard: a hub with the universal token off must not drop a known Target,
// and a new Target must get a log line that tells the fix.
func TestUniversalTokenOffOrChanged(t *testing.T) {
	cases := map[string]func(t *testing.T, h *testHub){
		"off": func(t *testing.T, h *testHub) {
			rec, err := h.hub.FindFirstRecordByFilter("universal_tokens", "token = {:t}", map[string]any{"t": permToken})
			require.NoError(t, err)
			require.NoError(t, h.hub.Delete(rec))
		},
		"changed": func(t *testing.T, h *testHub) {
			rec, err := h.hub.FindFirstRecordByFilter("universal_tokens", "token = {:t}", map[string]any{"t": permToken})
			require.NoError(t, err)
			rec.Set("token", "perm-token-2")
			require.NoError(t, h.hub.Save(rec))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			h := newTestHub(t)
			tb := h.bridge(permToken, alpha)
			stop := tb.start(t)
			eventually(t, "1 system", func() bool { return h.count("systems") == 1 && h.count("system_stats") >= 1 })
			stop()

			change(t, h)

			// The known Target and a new Target connect with the old token.
			tb2 := h.bridge(permToken, alpha, beta)
			tb2.start(t)
			eventually(t, "known Target served", func() bool { return tb2.calls[alpha.ID].Load() >= 1 })
			eventually(t, "new Target logged", func() bool { return len(tb2.logs.problems(beta.ID)) >= 1 })
			time.Sleep(300 * time.Millisecond)

			assert.Equal(t, []string{"Alpha"}, h.systemNames(), "the new Target must not get a system")
			assert.Equal(t, int64(0), tb2.calls[beta.ID].Load())
			lines := tb2.logs.problems(beta.ID)
			require.Len(t, lines, 1, "%v", lines)
			assert.Contains(t, lines[0].Text, "turn on the permanent universal token")
			assert.Contains(t, lines[0].Text, "target=t-beta")
			assert.Empty(t, tb2.logs.problems(alpha.ID))
		})
	}
	t.Run("token that no system uses", func(t *testing.T) {
		h := newTestHub(t)
		tb := h.bridge("unknown-token", alpha)
		tb.start(t)
		eventually(t, "log line", func() bool { return len(tb.logs.problems(alpha.ID)) >= 1 })
		time.Sleep(200 * time.Millisecond)
		lines := tb.logs.problems(alpha.ID)
		require.Len(t, lines, 1, "%v", lines)
		assert.Contains(t, lines[0].Text, "turn on the permanent universal token")
		assert.Equal(t, 0, h.count("systems"))
	})
}

// Guard for the text of docs/snmpbridge.md: a changed TOKEN gives a duplicate
// system for each Target, and a change of the token of each fingerprint record
// repairs it.
func TestChangedTokenDuplicatesAndRepair(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(fmt.Sprintf("repair=%v", repair), func(t *testing.T) {
			h := newTestHub(t)
			tb := h.bridge(permToken, alpha, beta)
			stop := tb.start(t)
			eventually(t, "2 systems", func() bool { return h.count("systems") == 2 && h.count("system_stats") >= 2 })
			stop()

			// The hub makes a new token value (the permanent token is turned off and on).
			rec, err := h.hub.FindFirstRecordByFilter("universal_tokens", "token = {:t}", map[string]any{"t": permToken})
			require.NoError(t, err)
			rec.Set("token", "perm-token-new")
			require.NoError(t, h.hub.Save(rec))
			if repair {
				fps, err := h.hub.FindAllRecords("fingerprints")
				require.NoError(t, err)
				for _, fp := range fps {
					fp.Set("token", "perm-token-new")
					// Save validates, as the PocketBase page does.
					require.NoError(t, h.hub.Save(fp))
				}
			}

			tb2 := h.bridge("perm-token-new", alpha, beta)
			tb2.start(t)
			eventually(t, "each Target served", func() bool {
				return tb2.calls[alpha.ID].Load() >= 1 && tb2.calls[beta.ID].Load() >= 1
			})
			if repair {
				assert.Equal(t, 2, h.count("systems"))
			} else {
				eventually(t, "4 systems", func() bool { return h.count("systems") == 4 })
			}
		})
	}
}

// Guard: a Target that the gate refuses must not connect. beszel#008 gives the
// real gate: a Target that is off must not connect again and again.
func TestGateRefusesConnect(t *testing.T) {
	h := newTestHub(t)
	tb := h.bridge(permToken, alpha)
	var asked atomic.Int64
	tb.Gate = func(Target) bool { asked.Add(1); return false }
	tb.start(t)
	eventually(t, "gate asked 3 times", func() bool { return asked.Load() >= 3 })
	assert.Equal(t, 0, h.count("systems"))
	assert.Equal(t, int64(0), tb.calls[alpha.ID].Load())
}

// Guard: a bridge with no token or no key must stop at the start and name the
// variable, not run and fail at each connect.
func TestLoadSettings(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	pub, err := ssh.NewPublicKey(priv.Public())
	require.NoError(t, err)
	keyLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("# comment\n\nfile-token\n"), 0o600))
	keyFile := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(keyFile, []byte(keyLine+"\n"), 0o600))

	set := func(t *testing.T, env map[string]string) {
		for _, k := range []string{"HUB_URL", "TOKEN", "TOKEN_FILE", "KEY", "KEY_FILE"} {
			t.Setenv(k, "")
			os.Unsetenv(k)
			t.Setenv("BESZEL_AGENT_"+k, "")
			os.Unsetenv("BESZEL_AGENT_" + k)
		}
		for k, v := range env {
			t.Setenv(k, v)
		}
	}
	base := map[string]string{"HUB_URL": "http://hub.example.com:8090", "TOKEN": "tok", "KEY": keyLine}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range base {
			m[a] = b
		}
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}

	set(t, base)
	s, err := LoadSettings()
	require.NoError(t, err)
	assert.Equal(t, "tok", s.Token)
	assert.Len(t, s.Keys, 1)

	set(t, with("TOKEN", ""))
	_, err = LoadSettings()
	assert.ErrorContains(t, err, "TOKEN")

	set(t, with("KEY", ""))
	_, err = LoadSettings()
	assert.ErrorContains(t, err, "KEY")

	set(t, with("HUB_URL", ""))
	_, err = LoadSettings()
	assert.ErrorContains(t, err, "HUB_URL")

	files := with("TOKEN", "")
	files["TOKEN_FILE"] = tokenFile
	delete(files, "KEY")
	files["KEY_FILE"] = keyFile
	set(t, files)
	s, err = LoadSettings()
	require.NoError(t, err)
	assert.Equal(t, "file-token", s.Token)

	// The BESZEL_AGENT_ prefix works, as in the stock agent.
	set(t, map[string]string{"BESZEL_AGENT_HUB_URL": base["HUB_URL"], "BESZEL_AGENT_TOKEN": "p-tok", "BESZEL_AGENT_KEY": keyLine})
	s, err = LoadSettings()
	require.NoError(t, err)
	assert.Equal(t, "p-tok", s.Token)
}
