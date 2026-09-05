// unitrise-gate - the UnitRise Gate Bridge agent.
//
// A deliberately small courier (the storEDGE Gate shape, proven for a decade):
// it pulls the facility's full gate-code list from UnitRise, writes the
// vendor-format file into the folder the on-site gate software watches, runs
// the vendor's consume command, and heartbeats. All policy - who gets access,
// suspensions, code generation - lives in the cloud; this binary is dumb on
// purpose.
//
//	unitrise-gate pair        interactive setup (key / secret / facility / paths)
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
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"

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
  pair       enter the credentials from the console's Gate hardware card
  test       verify the credentials and prove the save folder is writable
  service    install | uninstall | start | stop   (Windows service / launchd)

Day to day:
  ui         open the local dashboard (http://127.0.0.1:%d)
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
	pairCh := make(chan *config.Config, 1)
	hooks := ui.Hooks{
		OnForce: func() {
			if s := current.Load(); s != nil {
				s.ForceFullUpdate()
			}
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
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("no config (%v) - run `unitrise-gate pair` first", err)
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

// pair mirrors the storEDGE Gate Settings dialog: three credentials + paths.
func pair() error {
	cfg, _ := config.Load()
	if cfg == nil {
		cfg = &config.Config{APIEndpoint: config.DefaultAPIEndpoint}
	}
	in := bufio.NewReader(os.Stdin)
	ask := func(label, cur string) string {
		if cur != "" {
			fmt.Printf("%s [%s]: ", label, cur)
		} else {
			fmt.Printf("%s: ", label)
		}
		t, _ := in.ReadString('\n')
		t = strings.TrimSpace(t)
		if t == "" {
			return cur
		}
		return t
	}
	fmt.Println("UnitRise Gate Bridge pairing - values come from the console's Gate hardware card.")
	cfg.AccessKey = ask("API Access Key", cfg.AccessKey)
	cfg.AccessSecret = ask("API Access Secret", cfg.AccessSecret)
	cfg.FacilityID = ask("API Facility ID", cfg.FacilityID)
	cfg.SavePath = ask("Gate provider save path (folder the gate software watches)", cfg.SavePath)
	cfg.APIEndpoint = config.NormalizeEndpoint(ask("API endpoint", cfg.APIEndpoint))
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("%s saved %s\nnext: unitrise-gate test\n", check(), config.Path())
	return nil
}

// test = the Gate Settings "Test" button: prove credentials AND prove we can
// write into the save path, end to end, before anything is left unattended.
func test() error {
	cfg, err := config.Load()
	if err != nil {
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
