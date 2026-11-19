package snmpbridge

import (
	"context"
	"log/slog"

	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/system"
)

// why: the http*.go files set newHTTPRead in init(), and the bridge builds without them.
var newHTTPRead func(tc TargetConfig, p *HTTPProfile) ReadFunc

// NewBridge joins the parts for the command: for each Target of the config,
// the reader, the table rules and sources of its Profile, and the scheduler
// of the read timing. newReader makes the Reader of a Target, so a test can
// give a fake reader. The Scheduler is the source and the read gate of the
// Target.
func NewBridge(cfg *Config, settings *Settings, newReader func(TargetConfig) Reader, logger *slog.Logger) *Bridge {
	targets := make([]Target, 0, len(cfg.Targets))
	byID := make(map[string]TargetConfig, len(cfg.Targets))
	for _, t := range cfg.Targets {
		targets = append(targets, t.Target)
		byID[t.ID] = t
	}
	return &Bridge{
		HubURL:  settings.HubURL,
		Token:   settings.Token,
		Keys:    settings.Keys,
		Targets: targets,
		Source: func(t Target) Source {
			tc := byID[t.ID]
			var read ReadFunc
			switch {
			case tc.URL == "":
				read = (&TableSource{
					Target:  t,
					Profile: cfg.Profiles[tc.Profile],
					Reader:  newReader(tc),
					Logger:  logger,
				}).Read
			case newHTTPRead != nil:
				read = newHTTPRead(tc, cfg.Profiles[tc.Profile].HTTP)
			default:
				read = func(context.Context, common.DataRequestOptions) (*system.CombinedData, error) {
					return nil, errNoHTTPSource
				}
			}
			sched := NewScheduler(read)
			// why: like Bridge.Run, use the default logger when none is given.
			l := logger
			if l == nil {
				l = slog.Default()
			}
			sched.SetLogger(l.With("target", t.ID))
			return sched
		},
		Logger: logger,
	}
}
