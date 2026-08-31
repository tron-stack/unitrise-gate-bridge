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
	for {
		select {
		case <-ctx.Done():
			s.log.Infof("shutting down")
			return
		case <-s.force:
			s.lastHash = "" // drop the ETag so the next fetch is a full 200
			s.cycle(&poll)
		case <-timer.C:
			s.cycle(&poll)
		}
		status.Update(func(st *status.Snapshot) {
			st.PollSeconds = int(poll / time.Second)
			st.NextPollAt = time.Now().Add(poll)
		})
		timer.Reset(poll)
	}
}

func (s *Syncer) cycle(poll *time.Duration) {
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
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", full)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", full)
	}
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
