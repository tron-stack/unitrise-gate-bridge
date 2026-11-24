package main

// ⚠ THE DASHBOARD CAN ROTATE A KEY, NOT MOVE THE MACHINE (2026-11-20).
// The local dashboard's /api/pair has no sign-in: any account on the gate PC,
// or any program one runs, can post to it. A paired machine used to accept a
// re-pair there that kept the facility id (not a secret: it is printed on the
// console card and in the setup email) and changed the API endpoint and the
// save folder. Point the agent at your own server and it ran your consume
// command as SYSTEM, and the self-update installed whatever binary that
// server named. So:
//
//   - on a PAIRED machine the dashboard may replace only the access key and
//     the access secret, proven live against the server it is already paired
//     with. A different facility, endpoint or save folder needs an
//     Administrator: `unitrise-gate pair` from an elevated prompt, which
//     writes the protected config.json itself (planDashboardPair);
//   - on an UNPAIRED machine the dashboard pairs only with UnitRise's own
//     server (config.DefaultAPIEndpoint), or with the server an Administrator
//     already wrote into config.json, unless the agent itself runs with
//     UNITRISE_GATE_DEV=1 (only an Administrator can set the service's
//     environment);
//   - the CLI chooses its route by what the change touches and what this
//     prompt can do (choosePairRoute).

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/config"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/ui"
)

// devMode: the agent process was started with UNITRISE_GATE_DEV=1, the
// developer setting that lets the dashboard pair an unpaired machine with a
// server other than UnitRise's own.
func devMode() bool { return os.Getenv("UNITRISE_GATE_DEV") == "1" }

// samePath compares two folder paths the way the OS does. fold = Windows
// rules: either slash is a separator, case is ignored, a trailing separator
// doesn't count.
func samePath(a, b string, fold bool) bool {
	if fold {
		norm := func(p string) string { return path.Clean(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")) }
		return strings.EqualFold(norm(a), norm(b))
	}
	return filepath.Clean(strings.TrimSpace(a)) == filepath.Clean(strings.TrimSpace(b))
}

func endpointOf(c *config.Config) string {
	if c == nil || strings.TrimSpace(c.APIEndpoint) == "" {
		return config.DefaultAPIEndpoint
	}
	return config.NormalizeEndpoint(c.APIEndpoint)
}

const adminPairHint = "from an Administrator prompt, run: unitrise-gate pair --key ... --secret ... --facility ... --save ... [--endpoint ...]"

// planDashboardPair builds the config a dashboard pairing would save, or
// refuses it. old is the config on disk (nil when there is none). It
// validates the shape only; pairFromDashboard then proves it live.
func planDashboardPair(old *config.Config, req ui.PairRequest, dev, fold bool) (*config.Config, error) {
	key := strings.TrimSpace(req.AccessKey)
	secret := strings.TrimSpace(req.AccessSecret)
	facility := strings.TrimSpace(req.FacilityID)
	save := strings.TrimSpace(req.SavePath)
	endpoint := config.NormalizeEndpoint(req.APIEndpoint)

	c := &config.Config{APIEndpoint: config.DefaultAPIEndpoint}
	if old != nil {
		cp := *old // keep UIPort/LogFile/poll/file-name overrides
		c = &cp
	}
	paired := old != nil && (old.FacilityID != "" || old.AccessKey != "")
	if paired {
		// A paired machine accepts an UNAUTHENTICATED re-pair only to rotate
		// ITS OWN credentials (audit 2026-09-07 H2, 2026-11-20).
		if old.FacilityID != "" && facility != "" && facility != old.FacilityID {
			return nil, fmt.Errorf("this machine is paired to a different facility, and changing facilities needs an Administrator: stop the service (unitrise-gate service stop), then %s", adminPairHint)
		}
		if save != "" && !samePath(save, old.SavePath, fold) {
			return nil, fmt.Errorf("the save folder can't be changed from this page, because anyone signed in to this PC can reach it. To use %s instead of %s, %s", save, old.SavePath, adminPairHint)
		}
		if endpoint != "" && endpoint != endpointOf(old) {
			return nil, fmt.Errorf("the UnitRise server address can't be changed from this page, because anyone signed in to this PC can reach it. To use %s instead of %s, %s", endpoint, endpointOf(old), adminPairHint)
		}
		c.AccessKey, c.AccessSecret = key, secret
		if c.FacilityID == "" {
			c.FacilityID = facility
		}
		c.APIEndpoint = endpointOf(old)
	} else {
		allowed := endpointOf(old) // the default, or what an Administrator wrote
		if endpoint != "" && endpoint != allowed && !dev {
			return nil, fmt.Errorf("this page only pairs with UnitRise's own server (%s). To use %s, %s", allowed, endpoint, adminPairHint)
		}
		if endpoint == "" {
			endpoint = allowed
		}
		c.AccessKey, c.AccessSecret, c.FacilityID = key, secret, facility
		if save != "" {
			c.SavePath = save
		}
		c.APIEndpoint = endpoint
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// pairRoute is how the CLI applies a pairing.
type pairRoute int

const (
	routeAgent     pairRoute = iota // through the running agent's /api/pair (live-checked, saved by the service)
	routeDirect                     // write config.json here (needs an Administrator), then restart the service
	routeNeedAdmin                  // neither works from this prompt
)

// choosePairRoute picks the CLI's route. cfgReadable: this prompt can read
// the protected config (it is an Administrator); cfgMissing: there is no
// config yet; moves: the change touches the facility, endpoint or save folder,
// which the dashboard refuses.
func choosePairRoute(cfgReadable, cfgMissing, elevated, agentUp, moves bool) pairRoute {
	admin := cfgReadable || (cfgMissing && elevated)
	switch {
	case agentUp && !moves:
		return routeAgent
	case admin:
		return routeDirect
	case agentUp:
		return routeAgent // the agent answers with the refusal and the Administrator step
	default:
		return routeNeedAdmin
	}
}

// pairMoves: the requested change touches what only an Administrator may
// change (compared with the config on disk, nil when there is none).
func pairMoves(old *config.Config, facility, endpoint, save string, fold bool) bool {
	ep := config.NormalizeEndpoint(endpoint)
	if old == nil {
		return ep != "" && ep != config.DefaultAPIEndpoint
	}
	if strings.TrimSpace(facility) != "" && old.FacilityID != "" && strings.TrimSpace(facility) != old.FacilityID {
		return true
	}
	if ep != "" && ep != endpointOf(old) {
		return true
	}
	if strings.TrimSpace(save) != "" && old.SavePath != "" && !samePath(save, old.SavePath, fold) {
		return true
	}
	return false
}

// readSecretFlag: `--secret -` reads the secret from standard input (its
// first line), so it never sits in the shell's history; anything else is the
// secret itself.
func readSecretFlag(v string, stdin io.Reader) (string, error) {
	if v != "-" {
		return v, nil
	}
	if stdin == nil {
		return "", errors.New("--secret - reads the secret from standard input, but there is none")
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("reading the secret from standard input: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("--secret - found no secret on standard input (pipe it in, for example: type secret.txt | unitrise-gate pair --secret - ...)")
	}
	return line, nil
}

// startupConfig is the config the agent starts with. With Administrator (or
// SYSTEM) rights it first makes the config folder trustworthy (config.Prepare:
// on a fresh machine it creates and limits it before anything else can; a
// folder somebody else could have written is quarantined). A config.json that
// still can't be trusted is refused, and the agent runs UNPAIRED, waiting for
// a real pairing. notes are what to log (and show) about it.
func startupConfig(privileged bool, prepare func() (config.PrepareResult, error), load func() (*config.Config, error)) (*config.Config, []string, error) {
	var notes []string
	if privileged {
		res, err := prepare()
		if res.Problem != "" {
			notes = append(notes, res.Summary())
		}
		if err != nil {
			notes = append(notes, "config folder: "+err.Error())
		}
	}
	cfg, err := load()
	if errors.Is(err, config.ErrUntrusted) {
		notes = append(notes, err.Error()+". The Bridge is running unpaired: pair this machine again from the Gate Bridge window, or with unitrise-gate pair from an Administrator prompt.")
		return nil, notes, err
	}
	if err == nil {
		err = cfg.Validate()
	}
	return cfg, notes, err
}
