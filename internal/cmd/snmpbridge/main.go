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
// Each Target reports its own data: the table rules of its Profile, the CPU,
// the memory and the uptime.
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
	bridge := snmpbridge.NewBridge(cfg, settings, func(t snmpbridge.TargetConfig) snmpbridge.Reader {
		return snmpbridge.NewGoSNMPReader(t)
	}, logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	bridge.Run(ctx)
}
