package snmpbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/lxzan/gws"
	"golang.org/x/crypto/ssh"
)

// wsDeadline is the read deadline of the connection. The hub polls each 60 s
// and pings after a failed poll, so 120 s (the value of the stock agent)
// keeps a live connection open.
const wsDeadline = 120 * time.Second

// closeWait is the longest wait for the hub to answer a close frame.
const closeWait = 3 * time.Second

// Source gives the data of one Target.
type Source interface {
	// Data returns the reply to a GetData request. An error gives an error
	// reply, and the hub then sets the system down.
	Data(opts common.DataRequestOptions) (*system.CombinedData, error)
}

// Client connects one Target to the hub through one WebSocket.
type Client struct {
	HubURL string
	Token  string
	Keys   []ssh.PublicKey
	Target Target
	Source Source
	// Fail, when set, returns a non-empty error text for each action that
	// must get an error reply. The test tool uses it. It is not called for
	// CheckFingerprint.
	Fail   func(action common.WebSocketAction) string
	Logger *slog.Logger

	// once logs each kind of event one time, until the state of the Target
	// changes. It lives as long as the Client, so it spans the calls of Run.
	once *onceLog
	// sess is the session of the last Run call. It holds the served flag.
	sess atomic.Pointer[session]
}

// ErrBadKey is the cause that Run returns when the hub signature does not
// match any key of the Client.
var ErrBadKey = errors.New("invalid signature - check KEY value")

// Served reports whether the last call of Run replied to a GetData request.
// A connection that served data is a good connection.
func (c *Client) Served() bool {
	s := c.sess.Load()
	return s != nil && s.served.Load()
}

// CloseReason returns the reason text of a WebSocket close that the hub sent,
// or an empty string when err is not a close error.
func CloseReason(err error) string {
	var ce *gws.CloseError
	if errors.As(err, &ce) {
		return string(ce.Reason)
	}
	return ""
}

// HandshakeError is the reply of the hub when it refuses the WebSocket
// upgrade, for example "401 Invalid token".
type HandshakeError struct {
	Status int
	Body   string
	Err    error
}

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("hub refused the connection: HTTP %d %q: %v", e.Status, e.Body, e.Err)
}

func (e *HandshakeError) Unwrap() error { return e.Err }

// DialError means that the client could not reach the hub, for example with a
// wrong HUB_URL or a hub that is off.
type DialError struct{ Err error }

func (e *DialError) Error() string { return "cannot reach the hub: " + e.Err.Error() }
func (e *DialError) Unwrap() error { return e.Err }

// Run connects to the hub and serves requests until the connection closes or
// ctx ends. It returns the cause of the close. Run does not connect again:
// the caller decides when to call Run again.
func (c *Client) Run(ctx context.Context) error {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.once == nil {
		c.once = &onceLog{log: c.Logger.With("target", c.Target.ID)}
	}
	addr, err := agentConnectURL(c.HubURL)
	if err != nil {
		return err
	}
	sess := &session{client: c, log: c.Logger.With("target", c.Target.ID), done: make(chan struct{})}
	c.sess.Store(sess)
	conn, resp, err := gws.NewClient(sess, &gws.ClientOption{
		Addr: addr,
		RequestHeader: http.Header{
			"X-Token":  []string{c.Token},
			"X-Beszel": []string{beszel.Version},
		},
	})
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			// gws closes the TCP connection before it returns, so only the
			// part of the body that is already buffered can be read.
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 512))
			if readErr != nil {
				err = errors.Join(err, fmt.Errorf("read handshake body: %w", readErr))
			}
			return &HandshakeError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body)), Err: err}
		}
		return &DialError{Err: err}
	}
	go conn.ReadLoop()

	select {
	case <-ctx.Done():
		_ = conn.WriteClose(1000, nil)
		// why: a hub that does not answer the close frame must not block
		// the shutdown of the bridge.
		select {
		case <-sess.done:
		case <-time.After(closeWait):
			// The hub did not answer the close frame. Close the socket.
			_ = conn.NetConn().Close()
		}
		return ctx.Err()
	case <-sess.done:
		if sess.badKey.Load() {
			return ErrBadKey
		}
		return sess.closeErr
	}
}

// agentConnectURL turns HUB_URL into the WebSocket URL of the hub endpoint,
// in the same way as the stock agent.
func agentConnectURL(hubURL string) (string, error) {
	u, err := url.Parse(hubURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid HUB_URL %q: must include scheme and host", hubURL)
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = path.Join(u.Path, "api/beszel/agent-connect")
	return u.String(), nil
}

// session holds the state of one connection.
type session struct {
	gws.BuiltinEventHandler
	client   *Client
	log      *slog.Logger
	verified atomic.Bool
	badKey   atomic.Bool
	// served is set before the reply to GetData is sent, so Run cannot
	// return before it is set. It belongs to this session only.
	served atomic.Bool
	// dataMu makes calls of Source.Data one at a time. A reader of a Target
	// does not need to be safe for parallel calls.
	dataMu    sync.Mutex
	writeMu   sync.Mutex
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func (s *session) OnOpen(conn *gws.Conn) {
	_ = conn.SetDeadline(time.Now().Add(wsDeadline))
	s.client.once.write(slog.LevelInfo, "connected", "connected to the hub", "name", s.client.Target.Name)
}

func (s *session) OnClose(conn *gws.Conn, err error) {
	s.closeOnce.Do(func() {
		s.closeErr = err
		s.log.Debug("connection closed", "err", err)
		close(s.done)
	})
}

func (s *session) OnPing(conn *gws.Conn, payload []byte) {
	_ = conn.SetDeadline(time.Now().Add(wsDeadline))
	_ = conn.WritePong(payload)
}

func (s *session) OnMessage(conn *gws.Conn, message *gws.Message) {
	defer message.Close()
	_ = conn.SetDeadline(time.Now().Add(wsDeadline))
	if message.Opcode != gws.OpcodeBinary {
		return
	}
	var req common.HubRequest[cbor.RawMessage]
	if err := cbor.Unmarshal(message.Data.Bytes(), &req); err != nil {
		s.log.Error("cannot parse request", "err", err)
		return
	}
	id := "none"
	if req.Id != nil {
		id = fmt.Sprint(*req.Id)
	}
	s.log.Debug("request", "action", req.Action, "name", actionName(req.Action), "id", id)

	if req.Id == nil {
		// Each hub since 0.18 sends an id. A request with no id comes from
		// a hub that the bridge does not support, so it gets no reply.
		s.log.Warn("request has no id, no reply", "action", req.Action)
		return
	}
	// why: a slow Source.Data call must not block the pings and the other
	// requests on the read goroutine of the connection.
	go func() {
		resp := s.handle(&req)
		resp.Id = req.Id
		if req.Action == common.GetData && resp.Error == "" {
			// why: the hub can close the connection as soon as it reads
			// the reply, and Run then returns. Set the flag first.
			s.served.Store(true)
		}
		if err := s.write(conn, resp); err != nil {
			// why: like the stock agent (#1263), close a connection that cannot
			// send, so the caller connects again before the read deadline.
			s.client.once.write(slog.LevelError, "write", "cannot send reply, close the connection", "action", req.Action, "err", err)
			_ = conn.WriteClose(1000, nil)
			return
		}
		if s.badKey.Load() {
			// why: the hub may keep the connection open after a bad
			// signature. Close it so the caller can connect again.
			_ = conn.WriteClose(1000, nil)
		}
	}()
}

func (s *session) handle(req *common.HubRequest[cbor.RawMessage]) common.AgentResponse {
	c := s.client
	if req.Action == common.CheckFingerprint {
		return s.handleFingerprint(req.Data)
	}
	// Like the stock agent, answer nothing but an error before the hub
	// proves that it holds the private key.
	if !s.verified.Load() {
		return common.AgentResponse{Error: "hub not verified"}
	}
	if c.Fail != nil {
		if msg := c.Fail(req.Action); msg != "" {
			s.log.Info("error reply", "action", req.Action, "err", msg)
			return common.AgentResponse{Error: msg}
		}
	}
	if req.Action == common.GetData {
		var opts common.DataRequestOptions
		if len(req.Data) > 0 {
			if err := cbor.Unmarshal(req.Data, &opts); err != nil {
				s.log.Warn("cannot parse GetData options, use defaults", "err", err)
			}
		}
		s.dataMu.Lock()
		data, err := c.Source.Data(opts)
		s.dataMu.Unlock()
		if err != nil {
			return common.AgentResponse{Error: err.Error()}
		}
		return common.AgentResponse{SystemData: data}
	}
	if data, ok := EmptyReply(req.Action); ok {
		return common.AgentResponse{Data: data}
	}
	return common.AgentResponse{Error: errUnsupported(req.Action)}
}

// handleFingerprint checks the signature of the hub and returns the identity
// of the Target. The hub only reads the legacy Fingerprint field for this
// action, not the generic Data field (internal/hub/ws/handlers.go).
func (s *session) handleFingerprint(data cbor.RawMessage) common.AgentResponse {
	var req common.FingerprintRequest
	if err := cbor.Unmarshal(data, &req); err != nil {
		return common.AgentResponse{Error: err.Error()}
	}
	if err := verifySignature(s.client.Keys, s.client.Token, req.Signature); err != nil {
		// Set the flag before the reply: the hub can close the connection
		// as soon as it reads the reply.
		s.badKey.Store(true)
		return common.AgentResponse{Error: err.Error()}
	}
	s.verified.Store(true)
	resp := &common.FingerprintResponse{Fingerprint: Fingerprint(s.client.Target.ID)}
	if req.NeedSysInfo {
		resp.Name = s.client.Target.Name
		resp.Hostname = s.client.Target.ID
	}
	return common.AgentResponse{Fingerprint: resp}
}

func verifySignature(keys []ssh.PublicKey, token string, blob []byte) error {
	for _, key := range keys {
		sig := &ssh.Signature{Format: key.Type(), Blob: blob}
		if key.Verify([]byte(token), sig) == nil {
			return nil
		}
	}
	return ErrBadKey
}

func (s *session) write(conn *gws.Conn, resp common.AgentResponse) error {
	b, err := cbor.Marshal(resp)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return conn.WriteMessage(gws.OpcodeBinary, b)
}

func actionName(a common.WebSocketAction) string {
	switch a {
	case common.GetData:
		return "GetData"
	case common.CheckFingerprint:
		return "CheckFingerprint"
	case common.GetContainerLogs:
		return "GetContainerLogs"
	case common.GetContainerInfo:
		return "GetContainerInfo"
	case common.GetSmartData:
		return "GetSmartData"
	case common.GetSystemdInfo:
		return "GetSystemdInfo"
	case common.GetZfsData:
		return "GetZfsData"
	case common.SyncNetworkMonitors:
		return "SyncNetworkMonitors"
	case common.GetPackageUpdates:
		return "GetPackageUpdates"
	case common.GetSystemdLogs:
		return "GetSystemdLogs"
	}
	return "unknown"
}
