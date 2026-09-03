// Package syncer runs the agent's core loop, copied from the shape the
// storEDGE Gate agent has proven for a decade:
//
//	every N seconds (default 300):
//	  pull desired state (ETag-cheap when nothing changed)
//	  render the vendor file
//	  ATOMIC write into the watched folder (temp + rename - never half a file)
//	  run the consume command, capture its output   (the Ptisend.bat pattern)
//	  report applied + heartbeat
//
// Failure never destroys the last good file: if anything errs, the previous
// file stays in place, the gate keeps admitting from its last list, and the
// failure is reported so the console can show it (suspensions not propagating
// is an ALARM, not a log line).
package syncer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/config"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/logging"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/render"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/status"
)

const defaultPoll = 300 * time.Second

type Syncer struct {
	cfg    *config.Config
	client *api.Client
	log    *logging.Logger

	lastHash    string
	lastApplyAt time.Time
	// Force makes the next cycle re-apply even on 304 (the "Force Full
	// Update" menu action).
	force chan struct{}
}

func New(cfg *config.Config, log *logging.Logger) *Syncer {
	// The template renderer's delta memory lives beside the config file.
	render.RosterPath = filepath.Join(config.Dir(), "last-roster.json")
	return &Syncer{cfg: cfg, client: api.New(cfg), log: log, force: make(chan struct{}, 1)}
}

func (s *Syncer) ForceFullUpdate() {
	render.ForceNextFull() // delta formats re-emit the whole roster
	select {
	case s.force <- struct{}{}:
	default:
	}
}

// Run blocks until ctx is done.
func (s *Syncer) Run(ctx context.Context) {
	s.log.Infof("agent %s starting (os %s/%s, contract %s)", api.AgentVersion, runtime.GOOS, runtime.GOARCH, api.ContractVersion)
	poll := defaultPoll
	if s.cfg.PollSeconds > 0 {
		poll = time.Duration(s.cfg.PollSeconds) * time.Second
	}
	status.Update(func(st *status.Snapshot) {
		st.SavePath = s.cfg.SavePath
		st.PollSeconds = int(poll / time.Second)
	})
	timer := time.NewTimer(1 * time.Second) // first cycle almost immediately
	defer timer.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			s.log.Infof("shutting down")
			return
		case <-s.force:
			s.lastHash = "" // drop the ETag so the next fetch is a full 200
			s.safeCycle(&poll, &failures)
		case <-timer.C:
			s.safeCycle(&poll, &failures)
		}
		// A failed cycle retries on a short backoff instead of waiting out the
		// whole poll interval - a network blip at a site polling every 30 min
		// must not cost 30 minutes of stale gate codes. 30s → 1m → 2m → 4m,
		// capped by the poll itself, with ±20% jitter so a fleet that lost the
		// same uplink doesn't stampede back in lockstep.
		next := poll
		if failures > 0 {
			backoff := 30 * time.Second << uint(min(failures-1, 3))
			if backoff < poll {
				next = backoff
			}
			next = jitter(next)
		}
		status.Update(func(st *status.Snapshot) {
			st.PollSeconds = int(poll / time.Second)
			st.NextPollAt = time.Now().Add(next)
		})
		timer.Reset(next)
	}
}

func jitter(d time.Duration) time.Duration {
	// ±20%, cheap deterministic-enough source (no need for crypto here).
	f := 0.8 + 0.4*float64(time.Now().UnixNano()%1000)/1000.0
	return time.Duration(float64(d) * f)
}

// safeCycle guards the loop against a panic anywhere in render/apply/consume:
// the service must degrade to "cycle failed, will retry" - never die silently
// on a site PC nobody is watching.
func (s *Syncer) safeCycle(poll *time.Duration, failures *int) {
	defer func() {
		if r := recover(); r != nil {
			*failures++
			detail := fmt.Sprintf("internal error: %v", r)
			s.log.Errorf("%s", detail)
			status.Update(func(v *status.Snapshot) { v.OK = false; v.Detail = detail })
		}
	}()
	if s.cycle(poll) {
		*failures = 0
	} else {
		*failures++
	}
}

func (s *Syncer) cycle(poll *time.Duration) bool {
	ok, detail := true, ""
	st, hash, err := s.client.GetState(s.lastHash)
	switch {
	case err != nil:
		ok, detail = false, "state fetch: "+err.Error()
		s.log.Errorf("%s", detail)
	case st == nil:
		s.log.Debugf("no change (hash %.12s…)", hash)
	default:
		s.log.Infof("pulling latest gate codes for %s", st.Facility.Name)
		s.log.Infof("%d gate code(s) found", len(st.Credentials))
		status.Update(func(v *status.Snapshot) {
			v.FacilityName = st.Facility.Name
			v.Provider = st.Provider
			v.CodeCount = len(st.Credentials)
			v.ConsumeCmd = st.Settings.ConsumeCommand
		})
		// Server-tuned cadence - unless the site config pins its own
		// (a local override outranks the cloud; it exists for exactly
		// the cases where a tech needs to slow/speed one machine).
		if st.Settings.PollSeconds > 0 && s.cfg.PollSeconds == 0 {
			*poll = time.Duration(st.Settings.PollSeconds) * time.Second
		}
		if err := s.apply(st); err != nil {
			ok, detail = false, "apply: "+err.Error()
			s.log.Errorf("%s", detail)
		} else {
			s.lastHash = st.StateHash
			s.lastApplyAt = time.Now()
		}
	}
	status.Update(func(v *status.Snapshot) {
		v.OK = ok
		v.Detail = detail
		v.LastApplyAt = s.lastApplyAt
		v.LastHash = s.lastHash
	})

	hb := api.Heartbeat{
		AgentVersion:  api.AgentVersion,
		OS:            runtime.GOOS + "/" + runtime.GOARCH,
		LastApplyAt:   s.lastApplyAt,
		LastApplyHash: s.lastHash,
		OK:            ok,
		Detail:        detail,
	}
	if err := s.client.SendHeartbeat(hb); err != nil {
		s.log.Errorf("heartbeat: %v", err)
	}
	return ok
}

func (s *Syncer) apply(st *api.State) error {
	r, err := render.Get(st.Provider)
	if err != nil {
		return err
	}
	data, err := r.Render(st)
	if err != nil {
		return fmt.Errorf("render %s: %w", st.Provider, err)
	}

	name := st.Settings.GeneratedFileName
	if s.cfg.GeneratedFileName != "" {
		name = s.cfg.GeneratedFileName
	}
	if name == "" {
		name = r.FileName()
	}
	target := filepath.Join(s.cfg.SavePath, name)

	if err := os.MkdirAll(s.cfg.SavePath, 0o755); err != nil {
		return fmt.Errorf("save path: %w", err)
	}
	// Atomic replace: the gate software must never observe a partial file.
	tmp := target + ".unitrise-tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename into place: %w", err)
	}
	s.log.Infof("wrote %s (%d bytes)", target, len(data))
	// The file is the interface: now that it landed, remember what it told
	// the gate (delta formats diff against this next cycle).
	if err := render.CommitApplied(); err != nil {
		s.log.Errorf("roster save: %v", err)
	}
	status.Update(func(v *status.Snapshot) { v.TargetFile = target })

	exit, out := s.consume(st)
	if err := s.client.ReportApplied(api.Applied{
		StateHash:     st.StateHash,
		WroteFile:     target,
		ConsumeExit:   exit,
		ConsumeOutput: out,
	}); err != nil {
		s.log.Errorf("report applied: %v", err)
	}
	return nil
}

// consume runs the vendor's import trigger (e.g. PTI's Ptisend.bat), if one is
// configured. Exit code -1 = no command configured; -2 = failed to start.
func (s *Syncer) consume(st *api.State) (int, string) {
	cmdName := st.Settings.ConsumeCommand
	if s.cfg.ConsumeCommand != "" {
		cmdName = s.cfg.ConsumeCommand
	}
	if cmdName == "" {
		return -1, ""
	}
	full := cmdName
	if !filepath.IsAbs(full) {
		full = filepath.Join(s.cfg.SavePath, cmdName)
	}
	s.log.Infof("consuming the gate codes (running %s)", full)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// shellCommand is per-OS: cmd.exe needs explicit quoting when the resolved
	// path has spaces (C:\Program Files\PTI\Ptisend.bat) - exec's default
	// Windows escaping through `cmd /C` mangles it.
	cmd := shellCommand(ctx, full)
	cmd.Dir = s.cfg.SavePath
	outB, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(outB))
	if len(out) > 2048 {
		out = out[:2048]
	}
	if out != "" {
		s.log.Infof("consume process output:\n%s", out)
	}
	if err != nil {
		if exitErr, okE := err.(*exec.ExitError); okE {
			return exitErr.ExitCode(), out
		}
		s.log.Errorf("consume failed to start: %v", err)
		return -2, out + "\n" + err.Error()
	}
	return 0, out
}
