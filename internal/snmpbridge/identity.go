// Package snmpbridge speaks the Beszel agent protocol to a stock hub on
// behalf of one or more Targets. Each Target shows in the hub as one system.
package snmpbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Target is one device that the bridge reports to the hub.
type Target struct {
	// why: the fingerprint comes from ID, so a change of ID makes a new system in the hub.
	ID   string
	Name string
}

// Fingerprint returns the fingerprint that the hub stores for the Target.
// It uses the same encoding as the stock agent (agent/fingerprint.go):
// the hex of the first 24 bytes of a SHA-256 sum.
func Fingerprint(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:24])
}

// ParseKeys parses the hub public keys from the KEY value. The value holds
// one key in the authorized_keys format on each line. The bridge does not
// import the agent package for this, so the bridge binary stays small.
func ParseKeys(input string) ([]ssh.PublicKey, error) {
	var keys []ssh.PublicKey
	for line := range strings.Lines(input) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("parse KEY: %w", err)
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("KEY holds no public key")
	}
	return keys, nil
}
