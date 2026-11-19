package snmpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/system"
)

// MaxHTTPReply is the largest reply that a read accepts.
const MaxHTTPReply = 1 << 20

func init() {
	newHTTPRead = func(tc TargetConfig, p *HTTPProfile) ReadFunc {
		return (&HTTPSource{Target: tc, Profile: *p, Client: newHTTPClient()}).Read
	}
}

// newHTTPClient makes a client that follows no redirect and uses no proxy,
// so the header value cannot go to a different host.
func newHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// HTTPSource reads one Target with a GET and the JSON paths of its Profile.
type HTTPSource struct {
	Target  TargetConfig
	Profile HTTPProfile
	Client  *http.Client
}

var _ ReadFunc = (*HTTPSource)(nil).Read

// Read is a ReadFunc. The error names the Target and never holds the header
// value or the query of the URL. It fails when no value of the Profile is
// left.
func (s *HTTPSource) Read(ctx context.Context, opts common.DataRequestOptions) (*system.CombinedData, error) {
	doc, err := s.get(ctx)
	if err != nil {
		return nil, fmt.Errorf("http get of target %q failed: %w", s.Target.ID, err)
	}
	stats := system.Stats{}
	info := system.Info{AgentVersion: beszel.Version}
	gave := false
	if v, ok := percentAt(doc, s.Profile.CPU); ok {
		stats.Cpu, gave = twoDecimals(v), true
		info.Cpu = stats.Cpu
	}
	if v, ok := percentAt(doc, s.Profile.MemoryPercent); ok {
		stats.MemPct, gave = twoDecimals(v), true
		info.MemPct = stats.MemPct
	}
	if v, ok := numberAt(doc, s.Profile.Uptime); ok && v >= 0 && v < math.Ldexp(1, 64) {
		info.Uptime, gave = uint64(math.Trunc(v)), true
	}
	if !gave {
		return nil, fmt.Errorf("http get of target %q failed: no path of the profile gives a value", s.Target.ID)
	}
	data := &system.CombinedData{Stats: stats, Info: info}
	if opts.IncludeDetails {
		data.Details = &system.Details{Hostname: s.Target.Name}
	}
	return data, nil
}

// get makes the request and decodes the JSON reply.
func (s *HTTPSource) get(ctx context.Context) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Target.URL, nil)
	if err != nil {
		return nil, errors.New("cannot make the request")
	}
	if s.Target.Header != "" {
		req.Header.Set(s.Target.Header, s.Target.HeaderValue)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		// why: a *url.Error holds the full URL, with the query.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxHTTPReply+1))
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("read of the reply: %w", err)
	}
	if len(body) > MaxHTTPReply {
		return nil, errors.New("reply is larger than 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, errors.New("reply is not JSON")
	}
	return doc, nil
}

var decimalPattern = regexp.MustCompile(`^[+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)$`)

// numberAt gives the number at a dotted path. ok is false for an empty path,
// a missing field, a path through a value that is not an object, and a value
// that is not a finite number or a string with a decimal number.
func numberAt(doc any, path string) (float64, bool) {
	if path == "" {
		return 0, false
	}
	cur := doc
	for key := range strings.SplitSeq(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return 0, false
		}
		if cur, ok = obj[key]; !ok {
			return 0, false
		}
	}
	var text string
	switch v := cur.(type) {
	case json.Number:
		text = v.String()
	case string:
		text = strings.TrimSpace(v)
		if !decimalPattern.MatchString(text) {
			return 0, false
		}
	default:
		return 0, false
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}

// percentAt is numberAt, and ok is false for a value outside 0 to 100.
func percentAt(doc any, path string) (float64, bool) {
	v, ok := numberAt(doc, path)
	if !ok || v < 0 || v > 100 {
		return 0, false
	}
	return v, true
}
