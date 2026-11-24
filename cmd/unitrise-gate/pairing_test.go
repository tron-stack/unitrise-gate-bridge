package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/config"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/ui"
)

func pairedConfig() *config.Config {
	return &config.Config{AccessKey: "urgk_old", AccessSecret: "urgs_old", FacilityID: "fac1", SavePath: `C:\PTI`, APIEndpoint: config.DefaultAPIEndpoint, UIPort: 47811}
}

// The 2026-11-20 CRITICAL: the facility id is not a secret, so a re-pair
// that keeps it must not be able to move the machine to another server or
// another folder.
func TestADashboardRepairCanOnlyRotateTheKeyAndSecret(t *testing.T) {
	for name, req := range map[string]ui.PairRequest{
		"another server":    {AccessKey: "k", AccessSecret: "s", FacilityID: "fac1", SavePath: `C:\PTI`, APIEndpoint: "https://evil.example"},
		"another folder":    {AccessKey: "k", AccessSecret: "s", FacilityID: "fac1", SavePath: `C:\Users\Public\evil`},
		"a loopback server": {AccessKey: "k", AccessSecret: "s", FacilityID: "fac1", APIEndpoint: "http://127.0.0.1:9999"},
		"another facility":  {AccessKey: "k", AccessSecret: "s", FacilityID: "fac2"},
	} {
		if c, err := planDashboardPair(pairedConfig(), req, false, true); err == nil {
			t.Errorf("%s: accepted (endpoint %s, save %s)", name, c.APIEndpoint, c.SavePath)
		} else if !strings.Contains(err.Error(), "Administrator") {
			t.Errorf("%s: the refusal must name the Administrator route: %v", name, err)
		}
	}
	// Even the developer setting doesn't relax it on a paired machine.
	if _, err := planDashboardPair(pairedConfig(), ui.PairRequest{AccessKey: "k", AccessSecret: "s", APIEndpoint: "https://evil.example"}, true, true); err == nil {
		t.Error("the developer setting moved a paired machine to another server")
	}
}

func TestADashboardRepairRotatesTheCredentialsInPlace(t *testing.T) {
	// The dashboard sends the folder it shows (any case, a trailing slash)
	// and a blank endpoint.
	c, err := planDashboardPair(pairedConfig(), ui.PairRequest{AccessKey: " urgk_new ", AccessSecret: "urgs_new", FacilityID: "fac1", SavePath: `c:\pti\`}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessKey != "urgk_new" || c.AccessSecret != "urgs_new" {
		t.Fatalf("the key and secret are replaced: %+v", c)
	}
	if c.SavePath != `C:\PTI` || c.APIEndpoint != config.DefaultAPIEndpoint || c.FacilityID != "fac1" || c.UIPort != 47811 {
		t.Fatalf("everything else is kept as stored: %+v", c)
	}
	// The same server spelled the way a person types it is the same server.
	if _, err := planDashboardPair(pairedConfig(), ui.PairRequest{AccessKey: "k", AccessSecret: "s", APIEndpoint: "unitrise.com"}, false, true); err != nil {
		t.Fatalf("CONTROL: the stored server's own name is not a change: %v", err)
	}
}

func TestAFirstDashboardPairingIsWithUnitRiseOwnServer(t *testing.T) {
	req := ui.PairRequest{AccessKey: "k", AccessSecret: "s", FacilityID: "f", SavePath: `C:\PTI`, APIEndpoint: "https://evil.example"}
	if _, err := planDashboardPair(nil, req, false, true); err == nil || !strings.Contains(err.Error(), config.DefaultAPIEndpoint) {
		t.Fatalf("an unpaired machine pairs only with UnitRise's own server: %v", err)
	}
	// With UNITRISE_GATE_DEV=1 on the agent, a developer can.
	if c, err := planDashboardPair(nil, req, true, true); err != nil || c.APIEndpoint != "https://evil.example" {
		t.Fatalf("CONTROL: the developer setting allows another server: %v", err)
	}
	// A blank endpoint is UnitRise's.
	req.APIEndpoint = ""
	if c, err := planDashboardPair(nil, req, false, true); err != nil || c.APIEndpoint != config.DefaultAPIEndpoint || c.SavePath != `C:\PTI` {
		t.Fatalf("CONTROL: a first pairing with no endpoint is the default server: %+v %v", c, err)
	}
	// A server an Administrator already wrote into an unpaired config stands.
	admin := &config.Config{APIEndpoint: "https://staging.example"}
	req.APIEndpoint = "https://staging.example"
	if c, err := planDashboardPair(admin, req, false, true); err != nil || c.APIEndpoint != "https://staging.example" {
		t.Fatalf("CONTROL: the Administrator's stored server is allowed: %v", err)
	}
}

func TestTheCLIPicksARouteThatCanWork(t *testing.T) {
	cases := []struct {
		name                                        string
		readable, missing, elevated, agentUp, moves bool
		want                                        pairRoute
	}{
		{"rotation through the running agent", false, false, false, true, false, routeAgent},
		{"an Administrator moving the folder writes the config", true, false, true, true, true, routeDirect},
		{"an Administrator rotating keys uses the agent", true, false, true, true, false, routeAgent},
		{"an Administrator on a fresh machine with a custom server", false, true, true, true, true, routeDirect},
		{"an ordinary prompt moving the server goes to the agent, which refuses", false, false, false, true, true, routeAgent},
		{"an ordinary prompt with no agent can't", false, false, false, false, false, routeNeedAdmin},
		{"an ordinary prompt on a fresh machine with no agent can't", false, true, false, false, false, routeNeedAdmin},
		{"an Administrator with no agent writes the config", true, false, true, false, false, routeDirect},
	}
	for _, c := range cases {
		if got := choosePairRoute(c.readable, c.missing, c.elevated, c.agentUp, c.moves); got != c.want {
			t.Errorf("%s: route %d, want %d", c.name, got, c.want)
		}
	}
}

func TestPairMoves(t *testing.T) {
	old := pairedConfig()
	if pairMoves(old, "fac1", "", `c:\PTI\`, true) {
		t.Error("the same facility and folder is not a move")
	}
	if !pairMoves(old, "", "https://evil.example", "", true) || !pairMoves(old, "", "", `D:\Gate`, true) || !pairMoves(old, "fac2", "", "", true) {
		t.Error("a different server, folder or facility is a move")
	}
	if pairMoves(nil, "f", "", `C:\PTI`, true) || !pairMoves(nil, "f", "https://evil.example", "", true) {
		t.Error("on a fresh machine only a non-default server is a move")
	}
}

// `--secret -` reads the first line of stdin; anything else is the secret.
func TestTheSecretCanComeFromStandardInput(t *testing.T) {
	if got, err := readSecretFlag("-", strings.NewReader("urgs_from_pipe\r\nignored\n")); err != nil || got != "urgs_from_pipe" {
		t.Fatalf("--secret - = %q, %v", got, err)
	}
	if got, _ := readSecretFlag("urgs_plain", strings.NewReader("ignored")); got != "urgs_plain" {
		t.Fatalf("CONTROL: a plain --secret is used as given: %q", got)
	}
	if _, err := readSecretFlag("-", strings.NewReader("")); err == nil {
		t.Fatal("an empty standard input is refused, not paired with an empty secret")
	}
}

// The service starts by making the config folder trustworthy, and never
// trusts a config.json planted where only SYSTEM and Administrators should
// write: it runs unpaired and says why.
func TestTheServiceRunsUnpairedOnAPlantedConfig(t *testing.T) {
	prepared := 0
	prepare := func() (config.PrepareResult, error) {
		prepared++
		return config.PrepareResult{Dir: `C:\ProgramData\UnitRiseGateBridge`, Problem: `The config folder (C:\ProgramData\UnitRiseGateBridge) is owned by PC\dana, not by SYSTEM or Administrators`, Removed: []string{"config.json", "own-files.json"}}, nil
	}
	gone := func() (*config.Config, error) { return nil, os.ErrNotExist }
	cfg, notes, err := startupConfig(true, prepare, gone)
	if prepared != 1 || cfg != nil || !os.IsNotExist(err) {
		t.Fatalf("startupConfig = %v, %v (prepare ran %d time(s))", cfg, err, prepared)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], `owned by PC\dana`) || !strings.Contains(notes[0], "deleted config.json, own-files.json") {
		t.Fatalf("the quarantine is logged in words: %q", notes)
	}

	// A refusal from Load itself (the folder couldn't be cleaned): unpaired.
	refused := func() (*config.Config, error) {
		return nil, &config.UntrustedError{Problem: `The file config.json (C:\ProgramData\UnitRiseGateBridge\config.json) is owned by PC\dana, not by SYSTEM or Administrators`}
	}
	cfg, notes, err = startupConfig(true, func() (config.PrepareResult, error) { return config.PrepareResult{}, nil }, refused)
	if cfg != nil || !errors.Is(err, config.ErrUntrusted) {
		t.Fatalf("an untrusted config must not be used: %v %v", cfg, err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "running unpaired") {
		t.Fatalf("the agent says it is running unpaired: %q", notes)
	}

	// CONTROL: a trusted config loads, and an unprivileged run doesn't try
	// to re-own the folder.
	good := func() (*config.Config, error) { return pairedConfig(), nil }
	prepared = 0
	cfg, notes, err = startupConfig(false, prepare, good)
	if err != nil || cfg == nil || prepared != 0 || len(notes) != 0 {
		t.Fatalf("CONTROL: %v %v, prepare %d, notes %q", cfg, err, prepared, notes)
	}
}
