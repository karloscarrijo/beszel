package snmpbridge

import (
	"log/slog"
)

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
			ts := &TableSource{
				Target:  t,
				Profile: cfg.Profiles[tc.Profile],
				Reader:  newReader(tc),
				Logger:  logger,
			}
			return NewScheduler(ts.Read)
		},
		Logger: logger,
	}
}
