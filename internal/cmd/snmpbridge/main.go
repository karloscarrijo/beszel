// Command snmpbridge reports Targets to a Beszel hub as systems.
//
// Environment (the same rules as the stock agent):
//
//	HUB_URL     URL of the hub, for example http://localhost:8090 (required)
//	TOKEN       a permanent universal token of the hub, or TOKEN_FILE
//	KEY         the public key of the hub, or KEY_FILE
//
// Until the config file exists (beszel#006), the command starts one stub
// Target with fixed data. TARGET_ID and TARGET_NAME set its identity.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/henrygd/beszel/internal/snmpbridge"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	settings, err := snmpbridge.LoadSettings()
	if err != nil {
		logger.Error("stop", "err", err)
		os.Exit(1)
	}
	target := snmpbridge.Target{ID: envOr("TARGET_ID", "stub-1"), Name: envOr("TARGET_NAME", "Stub Target")}
	bridge := &snmpbridge.Bridge{
		HubURL:  settings.HubURL,
		Token:   settings.Token,
		Keys:    settings.Keys,
		Targets: []snmpbridge.Target{target},
		Source:  func(t snmpbridge.Target) snmpbridge.Source { return snmpbridge.StubSource{Name: t.Name} },
		Logger:  logger,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	bridge.Run(ctx)
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
