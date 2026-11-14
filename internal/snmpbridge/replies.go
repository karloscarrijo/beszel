package snmpbridge

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/monitor"
	"github.com/henrygd/beszel/internal/entities/smart"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/entities/zfs"
)

// EmptyReply returns the reply payload for an action that a Target has no
// data for. The hub decodes the generic Data field into the type of the
// action. A reply with no Data field and no legacy field gives an error on
// the hub ("unsupported action" or "no SMART data in response"), so each
// empty reply carries an encoded zero value of the right type.
//
// ok is false for an action that has no empty form. The caller then replies
// with an error, so the hub does not wait for its request timeout.
func EmptyReply(action common.WebSocketAction) (data cbor.RawMessage, ok bool) {
	var v any
	switch action {
	case common.GetSmartData:
		v = smart.SmartDataResponse{Data: map[string]smart.SmartData{}, Complete: true}
	case common.GetZfsData:
		v = zfs.ZfsData{Complete: true}
	case common.SyncNetworkMonitors:
		v = monitor.SyncResponse{}
	case common.GetPackageUpdates:
		v = system.PackageUpdates{Packages: []system.PackageUpdate{}}
	default:
		return nil, false
	}
	b, err := cbor.Marshal(v)
	if err != nil {
		return nil, false
	}
	return b, true
}

// errUnsupported is the error text for an action with no empty form, for
// example container logs or an action that a newer hub adds.
func errUnsupported(action common.WebSocketAction) string {
	return fmt.Sprintf("action %d is not supported by the SNMP bridge", action)
}
