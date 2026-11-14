package snmpbridge

import (
	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/system"
)

// StubSource is a Source with fixed data. The SNMP reader replaces it.
type StubSource struct {
	// Name is the hostname that the hub gets in the details of the system.
	Name string
}

// Data returns 2 fans and 2 temperatures, the same at each call.
func (s StubSource) Data(opts common.DataRequestOptions) (*system.CombinedData, error) {
	data := &system.CombinedData{
		Stats: system.Stats{
			Fans:         map[string]uint16{"FAN1": 1200, "FAN2": 900},
			Temperatures: map[string]float64{"CPU": 45.5, "System": 38},
		},
		Info: system.Info{AgentVersion: beszel.Version},
	}
	// why: the hub asks for the details until it gets them. The hostname is
	// the name, because SYNC_SYSTEM_NAMES=true renames a system to it.
	if opts.IncludeDetails {
		data.Details = &system.Details{Hostname: s.Name}
	}
	return data, nil
}
