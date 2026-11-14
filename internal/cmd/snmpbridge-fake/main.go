// Command snmpbridge-fake is a test tool. It connects one fake Target with
// fixed fans and temperatures to a stock hub, through the same protocol code
// as the SNMP bridge. It reads no SNMP device.
//
// Use it to test the hub with a system that has fans, for example for the
// FanLow alert on a machine that reports no fans.
//
// Environment:
//
//	HUB_URL     URL of the hub, for example http://localhost:8090 (required)
//	TOKEN       a permanent universal token of the hub (required)
//	KEY         the public key of the hub, from the "Add system" dialog (required)
//	FAKE_ID     the Target id; the fingerprint comes from it (default spike-1)
//	FAKE_NAME   the system name (default "Spike Target")
//	FAKE_FANS   fan speeds in RPM, as name=value pairs (default FAN1=1200,FAN2=900)
//	FAKE_TEMPS  temperatures in °C, as name=value pairs (default CPU=45.5,System=38)
//	FAKE_FAIL   actions that get an error reply, as names or numbers
//	            (for example GetData,GetSmartData or 0,4); default none
//	FAKE_FAIL_FILE  a file with a FAKE_FAIL value. The tool reads it at each
//	            request, so you can change the error mode while it runs.
//	            A missing file means no error. It replaces FAKE_FAIL.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/snmpbridge"
)

// retryDelay is the fixed wait before the next connect. A backoff comes with
// the real bridge client.
const retryDelay = 5 * time.Second

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("stop", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	hubURL := os.Getenv("HUB_URL")
	token := os.Getenv("TOKEN")
	if hubURL == "" || token == "" {
		return errors.New("set HUB_URL and TOKEN")
	}
	keys, err := snmpbridge.ParseKeys(os.Getenv("KEY"))
	if err != nil {
		return err
	}
	fans, err := parseFans(envOr("FAKE_FANS", "FAN1=1200,FAN2=900"))
	if err != nil {
		return fmt.Errorf("FAKE_FANS: %w", err)
	}
	temps, err := parseTemps(envOr("FAKE_TEMPS", "CPU=45.5,System=38"))
	if err != nil {
		return fmt.Errorf("FAKE_TEMPS: %w", err)
	}
	fail, err := parseActions(os.Getenv("FAKE_FAIL"))
	if err != nil {
		return fmt.Errorf("FAKE_FAIL: %w", err)
	}
	failFile := os.Getenv("FAKE_FAIL_FILE")
	failList := func() []common.WebSocketAction {
		if failFile == "" {
			return fail
		}
		b, err := os.ReadFile(failFile)
		if err != nil {
			return nil
		}
		list, err := parseActions(strings.TrimSpace(string(b)))
		if err != nil {
			logger.Error("FAKE_FAIL_FILE", "err", err)
			return nil
		}
		return list
	}

	target := snmpbridge.Target{ID: envOr("FAKE_ID", "spike-1"), Name: envOr("FAKE_NAME", "Spike Target")}
	client := &snmpbridge.Client{
		HubURL: hubURL,
		Token:  token,
		Keys:   keys,
		Target: target,
		Source: fixedSource{name: target.Name, fans: fans, temps: temps},
		Fail: func(a common.WebSocketAction) string {
			if slices.Contains(failList(), a) {
				return fmt.Sprintf("fake error for action %d", a)
			}
			return ""
		},
		Logger: logger,
	}
	failInfo := fmt.Sprint(toInts(fail))
	if failFile != "" {
		failInfo = "read from " + failFile + " at each request"
	}
	logger.Info("start", "id", target.ID, "name", target.Name, "fingerprint", snmpbridge.Fingerprint(target.ID), "fail", failInfo)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		err := client.Run(ctx)
		if ctx.Err() != nil {
			return nil
		}
		logger.Warn("disconnected, connect again", "err", err, "in", retryDelay)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(retryDelay):
		}
	}
}

// fixedSource returns the same data at each request.
type fixedSource struct {
	name  string
	fans  map[string]uint16
	temps map[string]float64
}

func (s fixedSource) Data(opts common.DataRequestOptions) (*system.CombinedData, error) {
	data := &system.CombinedData{
		Stats: system.Stats{
			Fans:         s.fans,
			Temperatures: s.temps,
		},
		Info: system.Info{
			AgentVersion: beszel.Version,
		},
	}
	// why: the hub asks for Details until it gets them, and it sends
	// GetSmartData and GetZfsData only after it has them. The hostname is the
	// system name, because a hub with SYNC_SYSTEM_NAMES=true renames the
	// system to the hostname.
	if opts.IncludeDetails {
		data.Details = &system.Details{Hostname: s.name}
	}
	return data, nil
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func pairs(s string) ([][2]string, error) {
	var out [][2]string
	for item := range strings.SplitSeq(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, value, ok := strings.Cut(item, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("%q is not name=value", item)
		}
		out = append(out, [2]string{name, strings.TrimSpace(value)})
	}
	return out, nil
}

func parseFans(s string) (map[string]uint16, error) {
	p, err := pairs(s)
	if err != nil {
		return nil, err
	}
	fans := make(map[string]uint16, len(p))
	for _, kv := range p {
		v, err := strconv.ParseUint(kv[1], 10, 16)
		if err != nil {
			return nil, fmt.Errorf("fan %s: %w", kv[0], err)
		}
		fans[kv[0]] = uint16(v)
	}
	return fans, nil
}

func parseTemps(s string) (map[string]float64, error) {
	p, err := pairs(s)
	if err != nil {
		return nil, err
	}
	temps := make(map[string]float64, len(p))
	for _, kv := range p {
		v, err := strconv.ParseFloat(kv[1], 64)
		if err != nil {
			return nil, fmt.Errorf("temperature %s: %w", kv[0], err)
		}
		temps[kv[0]] = v
	}
	return temps, nil
}

var actionNames = map[string]common.WebSocketAction{
	"GetData":             common.GetData,
	"CheckFingerprint":    common.CheckFingerprint,
	"GetContainerLogs":    common.GetContainerLogs,
	"GetContainerInfo":    common.GetContainerInfo,
	"GetSmartData":        common.GetSmartData,
	"GetSystemdInfo":      common.GetSystemdInfo,
	"GetZfsData":          common.GetZfsData,
	"SyncNetworkMonitors": common.SyncNetworkMonitors,
	"GetPackageUpdates":   common.GetPackageUpdates,
	"GetSystemdLogs":      common.GetSystemdLogs,
}

// toInts makes the action list print as numbers, not as a byte string.
func toInts(actions []common.WebSocketAction) []int {
	out := make([]int, len(actions))
	for i, a := range actions {
		out[i] = int(a)
	}
	return out
}

func parseActions(s string) ([]common.WebSocketAction, error) {
	var out []common.WebSocketAction
	for item := range strings.SplitSeq(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if a, ok := actionNames[item]; ok {
			out = append(out, a)
			continue
		}
		n, err := strconv.ParseUint(item, 10, 8)
		if err != nil {
			return nil, fmt.Errorf("unknown action %q", item)
		}
		out = append(out, common.WebSocketAction(n))
	}
	return out, nil
}
