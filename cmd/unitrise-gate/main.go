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

Setup (in order, on the gate computer):
  pair       enter the credentials from the console's Gate hardware card
  test       verify the credentials and prove the save folder is writable
  service    install | uninstall | start | stop   (Windows service / launchd)

Day to day:
  run        run in the foreground (the service runs this for you)
  ui         open the local dashboard (http://127.0.0.1:%d)
  force      push one full update right now, then exit
  update     download and install the latest published agent
  version    print the agent version

Docs & help: https://unitrise.com/help
`, api.AgentVersion, ui.DefaultPort)
}

func main() {
	// No command = show help, never default into `run`. The service always
	// passes "run" explicitly (SCM args / launchd ProgramArguments), so the
	// only people who arrive here bare are humans - most of them site staff
	// who just double-clicked the downloaded exe expecting a setup wizard.
	// Defaulting to `run` gave them a config error flashing in a console
	// that closed before it could be read (user report 2026-09-05).
	if len(os.Args) < 2 {
		usage(os.Stdout)
		if launchedByDoubleClick() {
			fmt.Print(`
It looks like the agent was double-clicked. This program is the agent itself,
not a setup wizard. To install it:

  1. Download install.ps1 from the same place as this exe (keep them together).
  2. Right-click Start -> "Windows PowerShell (Admin)", cd to that folder, run:
       powershell -ExecutionPolicy Bypass -File .\install.ps1
  3. The installer walks through pairing and starts the background service.

Press Enter to close this window...`)
			fmt.Scanln()
		}
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
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
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

func run(forceOnce bool) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("no config (%v) - run `unitrise-gate pair` first", err)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	// ONE agent per machine, ever: a second copy (the service already running,
	// plus a tech's console `run`) would double-write the vendor file and
	// corrupt delta rosters. `force` counts too - it runs a real cycle.
	release, err := lock.Acquire()
	if err != nil {
		return err
	}
	defer release()
	log, err := newLogger(cfg)
	if err != nil {
		return err
	}
	defer log.Close()
	status.Init(api.AgentVersion)
	logging.Hook = status.AppendLog

	s := syncer.New(cfg, log)
	// Local dashboard (127.0.0.1) - the UnitRise-styled status window.
	if cfg.UIPort >= 0 {
		if addr, uerr := ui.Serve(cfg.UIPort, s.ForceFullUpdate); uerr != nil {
			log.Errorf("dashboard: %v", uerr)
		} else {
			log.Infof("dashboard at %s", addr)
		}
	}
	loop := func(ctx context.Context) {
		// Log when a newer agent is published (ctx-tied; retries hourly until
		// the first successful check, then daily). LOG-ONLY - the swap is
		// always a person running `unitrise-gate update` (the console shows
		// the same "update available" chip from heartbeats).
		go update.Watch(ctx, api.New(cfg), log.Infof)
		if forceOnce {
			// One forced cycle, then leave - used by installers and support.
			s.ForceFullUpdate()
			cctx, cancel := context.WithCancel(ctx)
			go func() { s.Run(cctx) }()
			<-ctx.Done()
			cancel()
			return
		}
		s.Run(ctx)
	}

	if service.IsWindowsService() {
		return service.RunAsService(loop)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if forceOnce {
		// Foreground force: run exactly one cycle worth of time.
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
	cfg.APIEndpoint = ask("API endpoint", cfg.APIEndpoint)
	// A bare hostname gets the scheme it should have; a plain-http endpoint to
	// anywhere but loopback is refused by Validate below (the HMAC headers
	// must never cross the wire in the clear).
	if cfg.APIEndpoint != "" && !strings.Contains(cfg.APIEndpoint, "://") {
		cfg.APIEndpoint = "https://" + cfg.APIEndpoint
	}
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

// openUI opens the local dashboard in the default browser (agent must be running).
func openUI() error {
	url := fmt.Sprintf("http://127.0.0.1:%d", ui.DefaultPort)
	var cmd *exec.Cmd
	switch runtimeGOOS() {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	fmt.Println("opening", url)
	return cmd.Start()
}

func runtimeGOOS() string { return runtime.GOOS }

func waitForEnter(msg string) {
	fmt.Println(msg)
	bufio.NewReader(os.Stdin).ReadString('\n')
}
