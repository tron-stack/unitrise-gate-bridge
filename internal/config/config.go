// Package config owns the agent's on-disk configuration.
//
// Location (per-OS, machine-scoped so the Windows service and the interactive
// UI read the same file):
//   Windows: %ProgramData%\UnitRiseGateBridge\config.json
//   macOS:   /Library/Application Support/UnitRiseGateBridge/config.json
//   other:   /etc/unitrise-gate-bridge/config.json
// Override with UNITRISE_GATE_CONFIG for tests/dev.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Config struct {
	// Pairing (three-part, storEDGE-familiar — minted in the UnitRise console).
	AccessKey    string `json:"accessKey"`
	AccessSecret string `json:"accessSecret"`
	FacilityID   string `json:"facilityId"`

	// Where the cloud lives. Only changed when support says so (the
	// storEDGE "Advanced Setup" pattern).
	APIEndpoint string `json:"apiEndpoint"`

	// Local gate-software handoff. SavePath is the folder the gate software
	// watches (e.g. C:\PTI). File name + consume command normally come from
	// the SERVER state response so support can fix them without touching the
	// site machine; these local values are fallbacks/overrides.
	SavePath          string `json:"savePath"`
	GeneratedFileName string `json:"generatedFileName,omitempty"`
	ConsumeCommand    string `json:"consumeCommand,omitempty"`

	// Poll cadence override, seconds. 0 = server-provided (default 300).
	PollSeconds int `json:"pollSeconds,omitempty"`

	// Log file path; empty = <config dir>/agent.log.
	LogFile string `json:"logFile,omitempty"`

	// Local dashboard port (127.0.0.1). 0 = default 47810; -1 disables.
	UIPort int `json:"uiPort,omitempty"`
}

const DefaultAPIEndpoint = "https://app.mytruckyards.com"

func Dir() string {
	if v := os.Getenv("UNITRISE_GATE_CONFIG"); v != "" {
		return filepath.Dir(v)
	}
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "UnitRiseGateBridge")
	case "darwin":
		return "/Library/Application Support/UnitRiseGateBridge"
	default:
		return "/etc/unitrise-gate-bridge"
	}
}

func Path() string {
	if v := os.Getenv("UNITRISE_GATE_CONFIG"); v != "" {
		return v
	}
	return filepath.Join(Dir(), "config.json")
}

func Load() (*Config, error) {
	b, err := os.ReadFile(Path())
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("config %s is not valid JSON: %w", Path(), err)
	}
	if c.APIEndpoint == "" {
		c.APIEndpoint = DefaultAPIEndpoint
	}
	c.APIEndpoint = strings.TrimRight(c.APIEndpoint, "/")
	return &c, nil
}

func (c *Config) Save() error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// Atomic: temp + rename, same discipline as the gate file itself.
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

func (c *Config) Validate() error {
	var missing []string
	if c.AccessKey == "" {
		missing = append(missing, "accessKey")
	}
	if c.AccessSecret == "" {
		missing = append(missing, "accessSecret")
	}
	if c.FacilityID == "" {
		missing = append(missing, "facilityId")
	}
	if c.SavePath == "" {
		missing = append(missing, "savePath")
	}
	if len(missing) > 0 {
		return errors.New("config missing: " + strings.Join(missing, ", ") + " — run `unitrise-gate pair`")
	}
	return nil
}
