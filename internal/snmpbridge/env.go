package snmpbridge

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/henrygd/beszel/agent/utils"
	"golang.org/x/crypto/ssh"
)

// Settings are the values that all Targets share, from the environment.
type Settings struct {
	HubURL string
	Token  string
	Keys   []ssh.PublicKey
}

// LoadSettings reads HUB_URL, TOKEN or TOKEN_FILE, and KEY or KEY_FILE, with
// the same rules as the stock agent (a BESZEL_AGENT_ prefix also works). The
// error names the variable that is missing or wrong.
func LoadSettings() (*Settings, error) {
	var s Settings
	hubURL, ok := utils.GetEnv("HUB_URL")
	if !ok || hubURL == "" {
		return nil, errors.New("must set HUB_URL")
	}
	if _, err := agentConnectURL(hubURL); err != nil {
		return nil, err
	}
	s.HubURL = hubURL

	token, err := loadToken()
	if err != nil {
		return nil, err
	}
	s.Token = token

	s.Keys, err = loadKeys()
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func loadToken() (string, error) {
	if token, _ := utils.GetEnv("TOKEN"); token != "" {
		return token, nil
	}
	file, _ := utils.GetEnv("TOKEN_FILE")
	if file == "" {
		return "", errors.New("must set TOKEN or TOKEN_FILE")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read TOKEN_FILE: %w", err)
	}
	var token string
	for line := range strings.Lines(string(b)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if token != "" {
			return "", fmt.Errorf("TOKEN_FILE %s must contain a single token", file)
		}
		token = line
	}
	if token == "" {
		return "", fmt.Errorf("TOKEN_FILE %s holds no token", file)
	}
	return token, nil
}

func loadKeys() ([]ssh.PublicKey, error) {
	if key, _ := utils.GetEnv("KEY"); key != "" {
		return ParseKeys(key)
	}
	file, _ := utils.GetEnv("KEY_FILE")
	if file == "" {
		return nil, errors.New("must set KEY or KEY_FILE")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read KEY_FILE: %w", err)
	}
	return ParseKeys(string(b))
}
