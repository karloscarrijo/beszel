// Command snmpbridge reports Targets to a Beszel hub as systems.
//
// Environment (the same rules as the stock agent):
//
//	HUB_URL     URL of the hub, for example http://localhost:8090 (required)
//	TOKEN       a permanent universal token of the hub, or TOKEN_FILE
//	KEY         the public key of the hub, or KEY_FILE
//	SNMPBRIDGE_CONFIG  the path of the YAML config file (required)
//
// The command loads the config before it opens a WebSocket. On a config
// error it writes the error to stderr and exits with code 1.
//
// Until the SNMP reads exist (beszel#007, beszel#008), each Target reports
// the fixed data of the stub source.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/henrygd/beszel/internal/snmpbridge"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := snmpbridge.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "snmpbridge:", err)
		os.Exit(1)
	}
	settings, err := snmpbridge.LoadSettings()
	if err != nil {
		logger.Error("stop", "err", err)
		os.Exit(1)
	}
	targets := make([]snmpbridge.Target, 0, len(cfg.Targets))
	for _, t := range cfg.Targets {
		targets = append(targets, t.Target)
	}
	bridge := &snmpbridge.Bridge{
		HubURL:  settings.HubURL,
		Token:   settings.Token,
		Keys:    settings.Keys,
		Targets: targets,
		Source:  func(t snmpbridge.Target) snmpbridge.Source { return snmpbridge.StubSource{Name: t.Name} },
		Logger:  logger,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	bridge.Run(ctx)
}
