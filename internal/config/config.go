// Package config owns the agent's on-disk configuration.
//
// Location (per-OS, machine-scoped so the Windows service and the interactive
// UI read the same file):
//
//	Windows: %ProgramData%\UnitRiseGateBridge\config.json
//	macOS:   /Library/Application Support/UnitRiseGateBridge/config.json
//	other:   /etc/unitrise-gate-bridge/config.json
//
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
	// Pairing (three-part, storEDGE-familiar - minted in the UnitRise console).
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
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	// The access secret lives in this directory. On Windows, Go file modes are
	// ignored and %ProgramData% ACLs default to world-readable - lock the
	// directory to SYSTEM + Administrators (no-op elsewhere). Best-effort: a
	// pairing must not fail because ACL surgery did, but say so.
	if err := secureDir(Dir()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't restrict %s permissions: %v\n", Dir(), err)
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
		return errors.New("config missing: " + strings.Join(missing, ", ") + " - run `unitrise-gate pair`")
	}
	// The HMAC headers ride every request; over plain HTTP they'd cross the
	// wire replayable-in-the-clear. Loopback stays allowed for local testing.
	if ep := c.APIEndpoint; ep != "" && !strings.HasPrefix(ep, "https://") && !isLoopbackEndpoint(ep) {
		return fmt.Errorf("apiEndpoint must use https:// (got %s) - plain http is only allowed for 127.0.0.1/localhost testing", ep)
	}
	return nil
}

func isLoopbackEndpoint(ep string) bool {
	rest := strings.TrimPrefix(ep, "http://")
	if rest == ep {
		return false // not http:// at all (some other scheme) - let it fail above
	}
	host := rest
	if strings.HasPrefix(host, "[") {
		// Bracketed IPv6 literal: the host ends at "]", not at the port colon.
		if i := strings.Index(host, "]"); i >= 0 {
			host = host[:i+1]
		}
	} else if i := strings.IndexAny(host, ":/"); i >= 0 {
		host = host[:i]
	}
	return host == "localhost" || host == "127.0.0.1" || strings.HasPrefix(host, "127.") || host == "[::1]" || host == "::1"
}
