// unitrise-gate - the UnitRise Gate Bridge agent.
//
// A deliberately small courier (the storEDGE Gate shape, proven for a decade):
// it pulls the facility's full gate-code list from UnitRise, writes the
// vendor-format file into the folder the on-site gate software watches, runs
// the vendor's consume command, and heartbeats. All policy - who gets access,
// suspensions, code generation - lives in the cloud; this binary is dumb on
// purpose.
//
//	unitrise-gate pair        connect this machine (flags; routes via the running agent)
//	unitrise-gate test        verify credentials + write a probe file
//	unitrise-gate run         foreground loop (also the service entrypoint)
//	unitrise-gate force       one full update now, then exit
//	unitrise-gate ui          open the local dashboard in a browser
//	unitrise-gate update      download + swap in the latest published agent
//	unitrise-gate service …   install | uninstall | start | stop
//	unitrise-gate help | version
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/config"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/lock"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/logging"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/service"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/status"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/syncer"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/ui"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/update"
)

// check is the success marker: "✓" where terminals render it, plain "OK" on
// Windows (legacy conhost code pages turn the checkmark into mojibake, and
// `unitrise-gate test`'s output is the first thing a site installer reads).
func check() string {
	if runtime.GOOS == "windows" {
		return "OK"
	}
	return "✓"
}

func usage(w *os.File) {
	fmt.Fprintf(w, `UnitRise Gate Bridge %s - syncs gate codes from UnitRise to your gate software

Usage: unitrise-gate <command>

Setup (on the gate computer - or just double-click the exe on Windows):
  install    install on this machine: service + tray icon + setup page (Windows)
  pair       connect this machine: pair --key K --secret S --facility F --save C:\PTI
             (values from the console's Gate hardware card; the window's
             pairing form is the same thing with a UI)
  test       verify the connection (asks the running service when one is up)
  service    install | uninstall | start | stop   (Windows service / launchd)

Day to day:
  window     open the control window (status, codes, service start/stop)
  ui         open the local dashboard in a browser (http://127.0.0.1:%d)
  tray       run the tray icon (installed to start at login on Windows)
  force      push one full update right now, then exit
  run        run in the foreground (the service runs this for you)
  update     download and install the latest published agent
  version    print the agent version
  uninstall  remove the service, tray, and program (Windows; pairing is kept)

Docs & help: https://unitrise.com/help
`, api.AgentVersion, ui.DefaultPort)
}

// hasConsole: whether this process is attached to a terminal. On Windows the
// exe is a GUI-subsystem image (one binary is installer + agent + tray), so
// this comes from AttachConsole - false means Explorer double-click / UAC
// relaunch, where messages must be MessageBoxes, not prints nobody sees.
var hasConsole bool

func main() {
	hasConsole = attachParentConsole()
	// No command: from a terminal, show help; double-clicked, be the
	// installer (fresh machine) or open the dashboard (installed). Never
	// default into `run` - the service always passes "run" explicitly (SCM
	// args / launchd ProgramArguments), so bare launches are humans.
	if len(os.Args) < 2 {
		if !hasConsole {
			guiEntry()
			return
		}
		usage(os.Stdout)
		return
	}
	cmd := os.Args[1]
	var err error
	switch cmd {
	case "pair":
		err = pair()
	case "test":
		err = test()
	case "run":
		err = run(false)
	case "force":
		err = run(true)
	case "service":
		err = serviceCmd()
	case "tray":
		err = trayCmd()
	case "window":
		err = windowCmd()
	case "install":
		err = installCmd()
	case "uninstall":
		err = uninstallCmd()
	case "ui":
		err = openUI()
	case "update":
		err = updateCmd()
	case "help", "-h", "--help":
		usage(os.Stdout)
	case "version", "--version", "-v":
		fmt.Printf("unitrise-gate %s (contract %s, %s/%s)\n", api.AgentVersion, api.ContractVersion, runtime.GOOS, runtime.GOARCH)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil && err != errQuiet {
		report(err.Error(), true)
		os.Exit(1)
	}
}

func newLogger(c *config.Config) (*logging.Logger, error) {
	path := c.LogFile
	if path == "" {
		path = filepath.Join(config.Dir(), "agent.log")
	}
	return logging.New(path)
}

// pairFromDashboard is the dashboard's pairing hook: build a candidate
// config (preserving local-only knobs from any existing file), refuse it
// unless the credentials AND the save folder prove out LIVE - the same bar
// as `pair` + `test` - and only then persist it. Nothing unproven is ever
// saved, and every (re)pair is logged loudly.
func pairFromDashboard(req ui.PairRequest, log *logging.Logger) (ui.PairResult, *config.Config, error) {
	c := &config.Config{APIEndpoint: config.DefaultAPIEndpoint}
	if old, err := config.Load(); err == nil {
		c = old // keep UIPort/LogFile/poll/file-name overrides
	}
	c.AccessKey = strings.TrimSpace(req.AccessKey)
	c.AccessSecret = strings.TrimSpace(req.AccessSecret)
	c.FacilityID = strings.TrimSpace(req.FacilityID)
	if p := strings.TrimSpace(req.SavePath); p != "" {
		c.SavePath = p
	}
	if e := config.NormalizeEndpoint(req.APIEndpoint); e != "" {
		c.APIEndpoint = e
	} else {
		c.APIEndpoint = config.NormalizeEndpoint(c.APIEndpoint)
	}
	if err := c.Validate(); err != nil {
		return ui.PairResult{}, nil, err
	}
	st, _, err := api.New(c).GetState("")
	if err != nil {
		if strings.Contains(err.Error(), "no such host") {
			return ui.PairResult{}, nil, fmt.Errorf("can't reach %s (%v) - check this machine's internet, or set the API endpoint under Advanced", c.APIEndpoint, err)
		}
		return ui.PairResult{}, nil, fmt.Errorf("credentials check failed: %v", err)
	}
	if err := os.MkdirAll(c.SavePath, 0o755); err != nil {
		return ui.PairResult{}, nil, fmt.Errorf("save path: %v", err)
	}
	probe := filepath.Join(c.SavePath, "unitrise-bridge-probe.tmp")
	if err := os.WriteFile(probe, []byte("unitrise gate bridge write probe\n"), 0o644); err != nil {
		return ui.PairResult{}, nil, fmt.Errorf("cannot write into %s: %v", c.SavePath, err)
	}
	os.Remove(probe)
	if err := c.Save(); err != nil {
		return ui.PairResult{}, nil, fmt.Errorf("saving the pairing: %v", err)
	}
	log.Infof("paired from the dashboard: facility %q (%s), save path %s", st.Facility.Name, st.Provider, c.SavePath)
	return ui.PairResult{FacilityName: st.Facility.Name, Provider: st.Provider, CodeCount: len(st.Credentials)}, c, nil
}

func run(forceOnce bool) error {
	// ONE agent per machine, ever: a second copy (the service already running,
	// plus a tech's console `run`) would double-write the vendor file and
	// corrupt delta rosters. `force` counts too - it runs a real cycle.
	// The lock comes FIRST - even an unpaired agent serves the dashboard, and
	// two setup agents would fight over the pairing.
	release, err := lock.Acquire()
	if err != nil {
		return err
	}
	defer release()

	// SETUP MODE: a missing/invalid config no longer exits - the agent stays
	// up serving the dashboard, whose pairing form is now the way in (the
	// tray points at it). This also stops an unpaired service from crash-
	// looping under SCM recovery. `force` still demands a pairing: it exists
	// to push a real cycle.
	cfg, cfgErr := config.Load()
	if cfgErr == nil {
		cfgErr = cfg.Validate()
	}
	if cfgErr != nil {
		if forceOnce {
			return fmt.Errorf("no valid config (%v) - pair first (dashboard or `unitrise-gate pair`)", cfgErr)
		}
		cfg = nil
	}

	logCfg := cfg
	if logCfg == nil {
		logCfg = &config.Config{}
	}
	log, err := newLogger(logCfg)
	if err != nil {
		return err
	}
	defer log.Close()
	status.Init(api.AgentVersion)
	logging.Hook = status.AppendLog

	// The dashboard serves across pairings: force routes to the CURRENT
	// syncer, and a successful pair lands on pairCh to (re)start the loop.
	var current atomic.Pointer[syncer.Syncer]
	var currentCfg atomic.Pointer[config.Config]
	pairCh := make(chan *config.Config, 1)
	hooks := ui.Hooks{
		OnForce: func() {
			if s := current.Load(); s != nil {
				s.ForceFullUpdate()
			}
		},
		// One-click self-update from the dashboard/tray. After a successful
		// swap the process exits nonzero ON PURPOSE: under the SCM (or
		// launchd) the recovery actions relaunch it - onto the NEW binary.
		OnUpdate: func() (string, error) {
			c := currentCfg.Load()
			if c == nil {
				return "", fmt.Errorf("pair first - updates are fetched with the paired credentials")
			}
			client := api.New(c)
			info, newer, err := update.CheckOnly(client)
			if err != nil {
				return "", err
			}
			if !newer {
				return fmt.Sprintf("Up to date - running %s (latest published is %s).", api.AgentVersion, info.LatestVersion), nil
			}
			msg, err := update.SelfUpdate(client)
			if err != nil {
				return "", err
			}
			log.Infof("self-update installed %s - restarting the agent to run it", info.LatestVersion)
			go func() {
				time.Sleep(1500 * time.Millisecond)
				os.Exit(1)
			}()
			return msg + " - the agent restarts onto the new version in a few seconds.", nil
		},
		OnPair: func(req ui.PairRequest) (ui.PairResult, error) {
			res, newCfg, perr := pairFromDashboard(req, log)
			if perr != nil {
				log.Errorf("dashboard pairing refused: %v", perr)
				return res, perr
			}
			// Replace any queued (older) pairing with this one.
			select {
			case <-pairCh:
			default:
			}
			pairCh <- newCfg
			return res, nil
		},
	}
	uiPort := 0
	if cfg != nil {
		uiPort = cfg.UIPort
	}
	if uiPort >= 0 {
		if addr, uerr := ui.Serve(uiPort, hooks); uerr != nil {
			log.Errorf("dashboard: %v", uerr)
		} else {
			log.Infof("dashboard at %s", addr)
		}
	}

	loop := func(ctx context.Context) {
		for {
			if cfg == nil {
				status.Update(func(v *status.Snapshot) {
					v.Paired, v.State, v.OK = false, "setup", true
					v.Detail = "waiting for credentials"
				})
				log.Infof("setup mode: not paired yet - enter the credentials on the local dashboard")
				select {
				case <-ctx.Done():
					return
				case cfg = <-pairCh:
				}
			}
			status.Update(func(v *status.Snapshot) { v.Paired, v.State = true, "running" })
			s := syncer.New(cfg, log)
			current.Store(s)
			currentCfg.Store(cfg)
			cctx, cancel := context.WithCancel(ctx)
			// Log when a newer agent is published (ctx-tied; retries hourly
			// until the first successful check, then daily). LOG-ONLY - the
			// swap is always a person running `unitrise-gate update` (the
			// console shows the same "update available" chip from heartbeats).
			go update.Watch(cctx, api.New(cfg), log.Infof)
			if forceOnce {
				// One forced cycle, then leave - used by installers and support.
				s.ForceFullUpdate()
			}
			done := make(chan struct{})
			go func() { s.Run(cctx); close(done) }()
			select {
			case <-ctx.Done():
				cancel()
				<-done
				return
			case newCfg := <-pairCh:
				// Credentials updated from the dashboard: restart the sync
				// loop on the new pairing without dropping the process.
				log.Infof("pairing changed - restarting sync with the new credentials")
				cancel()
				<-done
				cfg = newCfg
			}
		}
	}

	if service.IsWindowsService() {
		return service.RunAsService(loop)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if forceOnce {
		// Foreground force: run exactly one cycle worth of time.
		s := syncer.New(cfg, log)
		current.Store(s)
		s.ForceFullUpdate()
		one, cancel := context.WithCancel(ctx)
		go func() { s.Run(one) }()
		// Give the forced cycle a moment to complete, then exit.
		waitForEnter("Force update dispatched - press Enter after the log shows it applied…")
		cancel()
		return nil
	}
	loop(ctx)
	return nil
}

// updateCmd is the person-run binary swap (internal/update). Requires a
// paired config for the signed update-check; a running service keeps working
// on the old mapped image until it's restarted.
func updateCmd() error {
	// The running agent updates ITSELF: it downloads, verifies the checksum,
	// swaps the binary, and the service restarts onto the new version. That
	// works from any prompt, elevated or not, paired via window or CLI.
	if base, _ := findLocalAgent(); base != "" {
		fmt.Println("asking the running agent to update itself…")
		msg, err := agentPost(base, "/api/update", map[string]string{}, 3*time.Minute)
		if err != nil {
			return err
		}
		if msg == "" {
			msg = "installed - the service is restarting onto the new version"
		}
		fmt.Println(msg)
		return nil
	}
	// No agent running: do it in this process (needs the config, which is
	// readable only from an Administrator prompt).
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("no running agent found, and the config isn't readable from this prompt (%v)\nstart the service (unitrise-gate service start) and re-run, or use an Administrator prompt", err)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	msg, err := update.SelfUpdate(api.New(cfg))
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

func serviceCmd() error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: unitrise-gate service [install|uninstall|start|stop]")
	}
	switch os.Args[2] {
	case "install":
		if err := service.Install(); err != nil {
			return err
		}
		fmt.Println("installed. start with: unitrise-gate service start")
		return nil
	case "uninstall":
		return service.Uninstall()
	case "start":
		return service.Start()
	case "stop":
		return service.Stop()
	default:
		return fmt.Errorf("unknown service action %q", os.Args[2])
	}
}

// ── the CLI talks to the running agent, not to the config file ──────────────
// The config directory is locked to SYSTEM + Administrators on purpose (the
// access secret lives there), which means an ordinary command prompt can
// neither read nor write it. So every CLI action that used to need the config
// first looks for the RUNNING agent on loopback and works through its API -
// the same one the dashboard and window use. The direct-config path survives
// only as the fallback for a machine where the service isn't running (and
// then needs an Administrator prompt).

// agentProbe is the slice of /api/status the CLI needs, decoded loosely so an
// older agent with fewer fields still answers.
type agentProbe struct {
	AgentVersion string `json:"agentVersion"`
	FacilityName string `json:"facilityName"`
	Provider     string `json:"provider"`
	CodeCount    int    `json:"codeCount"`
	OK           bool   `json:"ok"`
	Detail       string `json:"detail"`
	// nil on agents that predate setup mode = "paired" (they wouldn't run otherwise).
	Paired *bool `json:"paired"`
}

func (p *agentProbe) isPaired() bool { return p.Paired == nil || *p.Paired }

// findLocalAgent probes the dashboard's port window for a running agent.
func findLocalAgent() (string, *agentProbe) {
	c := &http.Client{Timeout: 2 * time.Second}
	for p := ui.DefaultPort; p < ui.DefaultPort+5; p++ {
		base := fmt.Sprintf("http://127.0.0.1:%d", p)
		resp, err := c.Get(base + "/api/status")
		if err != nil {
			continue
		}
		var s agentProbe
		err = json.NewDecoder(resp.Body).Decode(&s)
		resp.Body.Close()
		if err == nil {
			return base, &s
		}
	}
	return "", nil
}

// agentPost sends a JSON POST to the running agent (satisfying its browser
// guard: loopback host + application/json) and returns the response message.
func agentPost(base, path string, body any, timeout time.Duration) (string, error) {
	b, _ := json.Marshal(body)
	c := &http.Client{Timeout: timeout}
	resp, err := c.Post(base+path, "application/json", strings.NewReader(string(b)))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out) //nolint:errcheck - tolerate empty bodies
	msg := func(k string) string {
		if v, ok := out[k].(string); ok {
			return v
		}
		return ""
	}
	if resp.StatusCode >= 300 {
		if e := msg("error"); e != "" {
			return "", fmt.Errorf("%s", e)
		}
		return "", fmt.Errorf("agent answered %s", resp.Status)
	}
	return msg("message"), nil
}

// pair connects this machine to a UnitRise facility. Flags only - NO
// interactive prompts: the Windows exe is a GUI-subsystem image, and cmd.exe
// doesn't wait for those, so stdin prompts race the shell's own prompt (the
// on-site symptom 2026-09-07: every typed answer became a "not recognized"
// cmd error). When the agent is running, pairing routes through its /api/pair
// - live-validated, saved by the service itself, no Administrator needed.
func pair() error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	key := fs.String("key", "", "access key from the console's Gate hardware card")
	secret := fs.String("secret", "", "access secret")
	facility := fs.String("facility", "", "facility ID")
	save := fs.String("save", "", `folder the gate software watches (e.g. C:\PTI)`)
	endpoint := fs.String("endpoint", "", "API endpoint (only when support says so)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	base, snap := findLocalAgent()

	if *key == "" && *secret == "" && *facility == "" && *save == "" {
		if snap != nil && snap.isPaired() {
			where := snap.FacilityName
			if where == "" {
				where = "your facility"
			}
			fmt.Printf("%s already connected to %s (%s, %d codes) - nothing to do.\n", check(), where, snap.Provider, snap.CodeCount)
			fmt.Println("to re-pair: open the Gate Bridge window (Update credentials), or pass the flags below.")
			fmt.Println("  unitrise-gate pair --key K --secret S --facility F --save C:\\PTI")
			return nil
		}
		fmt.Println("Pairing takes the four values from the console's Gate hardware card:")
		fmt.Println("  unitrise-gate pair --key K --secret S --facility F --save C:\\PTI [--endpoint URL]")
		fmt.Println("Easiest path: open the Gate Bridge window - its pairing form does the same thing.")
		return fmt.Errorf("missing --key/--secret/--facility/--save")
	}

	// The running agent validates LIVE and saves as the service - the config
	// directory is service-only, so this is the path that works unelevated.
	if base != "" {
		fmt.Println("verifying with UnitRise (via the running agent)…")
		_, err := agentPost(base, "/api/pair", map[string]string{
			"accessKey":    strings.TrimSpace(*key),
			"accessSecret": strings.TrimSpace(*secret),
			"facilityId":   strings.TrimSpace(*facility),
			"savePath":     strings.TrimSpace(*save),
			"apiEndpoint":  strings.TrimSpace(*endpoint),
		}, 30*time.Second)
		if err != nil {
			return fmt.Errorf("pairing failed: %w", err)
		}
		fmt.Printf("%s connected - the agent is syncing.\n", check())
		return nil
	}

	// No agent running: write the config directly (needs an Administrator
	// prompt - the directory is locked to SYSTEM + Administrators).
	cfg, _ := config.Load()
	if cfg == nil {
		cfg = &config.Config{APIEndpoint: config.DefaultAPIEndpoint}
	}
	set := func(dst *string, v string) {
		if v = strings.TrimSpace(v); v != "" {
			*dst = v
		}
	}
	set(&cfg.AccessKey, *key)
	set(&cfg.AccessSecret, *secret)
	set(&cfg.FacilityID, *facility)
	set(&cfg.SavePath, *save)
	if strings.TrimSpace(*endpoint) != "" {
		cfg.APIEndpoint = config.NormalizeEndpoint(*endpoint)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("%w\n(the config folder is restricted - run this from an Administrator prompt, or start the service and re-run)", err)
	}
	fmt.Printf("%s saved %s\nnext: unitrise-gate test\n", check(), config.Path())
	return nil
}

// test = the Gate Settings "Test" button: prove credentials AND prove we can
// write into the save path, end to end, before anything is left unattended.
func test() error {
	cfg, err := config.Load()
	if err != nil {
		// Service-only config - report through the running agent instead.
		if base, snap := findLocalAgent(); base != "" && snap != nil {
			if !snap.isPaired() {
				return fmt.Errorf("the agent is running but not paired yet - open the Gate Bridge window, or: unitrise-gate pair --key … --secret … --facility … --save …")
			}
			where := snap.FacilityName
			if where == "" {
				where = "your facility"
			}
			if snap.OK {
				fmt.Printf("%s agent %s is running and in sync - %s (%s, %d codes)\n", check(), snap.AgentVersion, where, snap.Provider, snap.CodeCount)
				return nil
			}
			d := snap.Detail
			if d == "" {
				d = "open the Gate Bridge window for details"
			}
			return fmt.Errorf("agent %s is running but the last cycle failed: %s", snap.AgentVersion, d)
		}
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	client := api.New(cfg)
	st, _, err := client.GetState("")
	if err != nil {
		return fmt.Errorf("API check failed: %w", err)
	}
	fmt.Printf("%s API ok - facility %q, provider %q, %d code(s)\n", check(), st.Facility.Name, st.Provider, len(st.Credentials))
	probe := filepath.Join(cfg.SavePath, "unitrise-bridge-probe.tmp")
	if err := os.MkdirAll(cfg.SavePath, 0o755); err != nil {
		return fmt.Errorf("save path: %w", err)
	}
	if err := os.WriteFile(probe, []byte("unitrise gate bridge write probe\n"), 0o644); err != nil {
		return fmt.Errorf("cannot write into %s: %w", cfg.SavePath, err)
	}
	os.Remove(probe)
	fmt.Printf("%s save path writable (%s)\n", check(), cfg.SavePath)
	fmt.Println("all good - install the service: unitrise-gate service install")
	return nil
}

// openBrowser opens a URL in the default browser (shared by ui/tray/installer).
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtimeGOOS() {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// openUI opens the local dashboard in the default browser (agent must be running).
func openUI() error {
	url := fmt.Sprintf("http://127.0.0.1:%d", ui.DefaultPort)
	fmt.Println("opening", url)
	return openBrowser(url)
}

func runtimeGOOS() string { return runtime.GOOS }

func waitForEnter(msg string) {
	fmt.Println(msg)
	bufio.NewReader(os.Stdin).ReadString('\n')
}
