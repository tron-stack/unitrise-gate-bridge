// Package syncer runs the agent's core loop, copied from the shape the
// storEDGE Gate agent has proven for a decade:
//
//	every N seconds (default 300):
//	  pull desired state (ETag-cheap when nothing changed)
//	  render the vendor file
//	  ATOMIC write into the watched folder (temp + fsync + rename - never
//	  half a file, never an empty one after a power cut)
//	  run the consume command, capture its output   (the Ptisend.bat pattern)
//	  report applied + heartbeat
//
// and, on its own clock every 60-90 seconds, a heartbeat: the console calls
// an agent offline after 10 minutes without one, and a site can poll as
// rarely as once an hour.
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
	"sync"
	"time"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/config"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/fsutil"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/logging"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/mask"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/render"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/status"
)

const defaultPoll = 300 * time.Second

type Syncer struct {
	cfg    *config.Config
	client *api.Client
	log    *logging.Logger

	// etag is the hash of the last state applied in full (Run's goroutine
	// only): the next fetch sends it so an unchanged state is a cheap 304.
	etag string

	// What the heartbeat reports, read by its own goroutine too.
	mu          sync.Mutex
	lastHash    string
	lastApplyAt time.Time
	lastOK      bool
	lastDetail  string
	cycled      bool // a cycle has finished, so lastOK/lastDetail mean something

	// Force makes the next cycle re-apply even on 304 (the "Force Full
	// Update" menu action).
	force chan struct{}

	// heartbeatEvery is the gap between heartbeats on their own clock;
	// nil = heartbeatInterval (60-90 s). Tests shorten it.
	heartbeatEvery func() time.Duration

	// ownFilesPath records the files the agent wrote under its own default
	// names (ownfiles.go); empty = not tracked.
	ownFilesPath string
}

func New(cfg *config.Config, log *logging.Logger) *Syncer {
	// The template renderer's delta memory lives beside the config file.
	render.RosterPath = filepath.Join(config.Dir(), "last-roster.json")
	return &Syncer{
		cfg: cfg, client: api.New(cfg), log: log, force: make(chan struct{}, 1),
		ownFilesPath: filepath.Join(config.Dir(), "own-files.json"),
	}
}

// heartbeatInterval is 60-90 seconds, spread so a fleet that lost the same
// uplink doesn't report back in lockstep. Well inside the server's 10-minute
// online window whatever the poll interval is.
func heartbeatInterval() time.Duration {
	return 60*time.Second + time.Duration(time.Now().UnixNano()%int64(30*time.Second))
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
	go s.heartbeatLoop(ctx)
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
			s.etag = "" // drop the ETag so the next fetch is a full 200
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

// heartbeatLoop sends a heartbeat on its own clock (60-90 s), independent of
// the poll: with pollSeconds above 600 the server used to mark a healthy
// agent offline between polls. It starts reporting once the first cycle has
// finished, so it never claims a sync that hasn't happened.
func (s *Syncer) heartbeatLoop(ctx context.Context) {
	every := s.heartbeatEvery
	if every == nil {
		every = heartbeatInterval
	}
	t := time.NewTimer(every())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		ready := s.cycled
		s.mu.Unlock()
		if ready {
			s.sendHeartbeat()
		}
		t.Reset(every())
	}
}

// heartbeat is what the agent reports right now.
func (s *Syncer) heartbeat() api.Heartbeat {
	s.mu.Lock()
	defer s.mu.Unlock()
	hb := api.Heartbeat{
		AgentVersion:  api.AgentVersion,
		OS:            runtime.GOOS + "/" + runtime.GOARCH,
		LastApplyHash: s.lastHash,
		OK:            s.lastOK,
		Detail:        s.lastDetail,
	}
	if !s.lastApplyAt.IsZero() {
		at := s.lastApplyAt
		hb.LastApplyAt = &at
	}
	return hb
}

func (s *Syncer) sendHeartbeat() {
	if err := s.client.SendHeartbeat(s.heartbeat()); err != nil {
		s.log.Errorf("heartbeat: %v", err)
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
	st, hash, err := s.client.GetState(s.etag)
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
			// Next fetch is a full one even if the state goes back to the
			// last applied hash: the file on disk now says something else.
			s.etag = ""
		} else {
			s.etag = st.StateHash
			s.mu.Lock()
			s.lastHash = st.StateHash
			s.lastApplyAt = time.Now()
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	s.lastOK, s.lastDetail, s.cycled = ok, detail, true
	lastApplyAt, lastHash := s.lastApplyAt, s.lastHash
	s.mu.Unlock()
	status.Update(func(v *status.Snapshot) {
		v.OK = ok
		v.Detail = detail
		v.LastApplyAt = lastApplyAt
		v.LastHash = lastHash
	})
	// One heartbeat right after every cycle (the console sees a failure at
	// once), plus heartbeatLoop's own clock in between.
	s.sendHeartbeat()
	return ok
}

// consumeSetting is the consume command in force: this PC's override, else
// the server's. Empty = none.
func (s *Syncer) consumeSetting(st *api.State) (name string, local bool) {
	if s.cfg.ConsumeCommand != "" {
		return s.cfg.ConsumeCommand, true
	}
	return st.Settings.ConsumeCommand, false
}

func (s *Syncer) apply(st *api.State) error {
	r, err := render.Get(st.Provider)
	if err != nil {
		return err
	}
	// Which file to write: this PC's own override, else the server's name,
	// else the renderer's default - and whichever it is, a plain file name
	// inside the save folder, or nothing is written (consume.go, ED-6).
	name, local := st.Settings.GeneratedFileName, false
	if s.cfg.GeneratedFileName != "" {
		name, local = s.cfg.GeneratedFileName, true
	}
	if name == "" {
		name, local = r.FileName(), false
	}
	target, err := gateFilePath(s.cfg.SavePath, name, local)
	if err != nil {
		return err
	}
	// With NO consume command the gate software takes the file itself: the
	// last write counts as imported once the file is gone from its path.
	// Checked BEFORE rendering, because delta formats diff against what the
	// gate software has taken (internal/render, 2026-11-20).
	if cmdName, _ := s.consumeSetting(st); cmdName == "" {
		if took, err := render.ConfirmIfTaken(); err != nil {
			s.log.Errorf("roster: %v", err)
		} else if took {
			s.log.Infof("the gate software took the last file")
		}
	}
	data, err := r.Render(st)
	if err != nil {
		return fmt.Errorf("render %s: %w", st.Provider, err)
	}

	if err := os.MkdirAll(s.cfg.SavePath, 0o755); err != nil {
		return fmt.Errorf("save path: %w", err)
	}
	// Atomic and durable: the gate software must never observe a partial
	// file, nor an empty one after a power cut (fsync before the rename).
	if err := fsutil.WriteFileAtomic(target, data, 0o644); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	s.log.Infof("wrote %s (%d bytes)", target, len(data))
	// The file landed: what it tells the gate is PENDING until the gate
	// software takes it (the consume command exits 0, or the file goes).
	recorded := true
	if err := render.RecordWritten(target, data); err != nil {
		recorded = false // the confirmed roster stays: the next diff is a superset
		s.log.Errorf("roster save: %v", err)
	}
	// The "Gate codes" view: what the file now carries, per credential
	// (status.SetRoster masks every code - the local dashboard has no
	// sign-in).
	rows := make([]status.RosterRow, 0, len(st.Credentials))
	for _, c := range st.Credentials {
		rows = append(rows, status.RosterRow{
			Unit: c.UnitLabel, Tenant: c.TenantName, Code: c.Code,
			Status: c.Status, TZ: string(c.TimeZoneGroup),
		})
	}
	status.SetRoster(rows)
	// The agent's own test file (and GATECODES.TXT) left behind once the
	// output moved elsewhere carries every code: remove it.
	s.cleanOwnFiles(target, data)
	status.Update(func(v *status.Snapshot) { v.TargetFile = target })

	exit, out := s.consume(st)
	if exit == 0 && recorded {
		// The gate software imported the file: the pending roster is now
		// what the gate has.
		if err := render.ConfirmWritten(); err != nil {
			s.log.Errorf("roster confirm: %v", err)
		}
	}
	status.Update(func(v *status.Snapshot) {
		v.ConsumeExit = exit
		if exit != -1 {
			v.ConsumeRanAt = time.Now()
		}
		note := strings.TrimSpace(out)
		if len(note) > 240 {
			note = note[:240] + "…"
		}
		v.ConsumeNote = note
	})
	if err := s.client.ReportApplied(api.Applied{
		StateHash:     st.StateHash,
		WroteFile:     target,
		ConsumeExit:   exit,
		ConsumeOutput: out,
	}); err != nil {
		s.log.Errorf("report applied: %v", err)
	}
	// A configured consume command that fails IS a failed sync: the file
	// landed, but the gate never got it (on-site 2026-09-06: a mistyped
	// Ptisend.bat left the dashboard green "in sync" while the Falcon
	// received nothing). -1 = no command configured; -2 = refused or failed
	// to start; anything else nonzero turns the dashboard/tray red with the
	// command's own output.
	if exit == -2 {
		return fmt.Errorf("%s The gate file was written, but its import wasn't run.", asSentence(out))
	}
	if exit != -1 && exit != 0 {
		msg := fmt.Sprintf("consume command failed (exit %d)", exit)
		if o := strings.TrimSpace(out); o != "" {
			if len(o) > 300 {
				o = o[:300] + "…"
			}
			msg += ": " + o
		}
		return fmt.Errorf("%s - the file was written but the gate software wasn't told; check the consume command name and try Force full update", msg)
	}
	return nil
}

// asSentence capitalizes a reason and ends it with a full stop.
func asSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "The import file couldn't be run."
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// consume runs the vendor's import trigger (e.g. PTI's Ptisend.bat), if one is
// configured. Exit code -1 = no command configured; -2 = refused or failed to
// start. The command is a plain .bat, .cmd or .exe file name, run from the
// save folder as the file itself - never a command line, never through a
// shell (consume.go, ED-6). A full path or a command with arguments, which an
// older agent accepted, is refused in words and nothing runs.
//
// The process output is masked before it is logged, shown or sent (every run
// of three or more digits, internal/mask): a vendor tool can echo the lines it
// imported, and those are gate codes.
func (s *Syncer) consume(st *api.State) (int, string) {
	cmdName, local := s.consumeSetting(st)
	if cmdName == "" {
		return -1, ""
	}
	full, err := resolveConsume(s.cfg.SavePath, cmdName, local)
	if err != nil {
		s.log.Errorf("%v", err)
		return -2, err.Error()
	}
	s.log.Infof("consuming the gate codes (running %s)", full)

	// Prefer the SIGNED-IN user's session (Windows service only): the gate
	// vendor's desktop software lives there, and a sender run in session 0
	// can no-op with exit 0 (Falcon site, 2026-09-06). Falls back to the
	// plain exec when not applicable.
	if exit, out, ran := runConsumeInSession(full, s.cfg.SavePath, 2*time.Minute); ran {
		out = mask.DigitRuns(strings.TrimSpace(out))
		out = strings.TrimSpace(out + "\n[ran in the signed-in user's session]")
		if len(out) > 2048 {
			out = out[:2048]
		}
		s.log.Infof("consume finished in the user session (exit %d)", exit)
		if out != "" {
			s.log.Infof("consume process output:\n%s", out)
		}
		return exit, out
	}

	// From here the file runs as THIS process: the system account when the
	// service runs it with nobody signed in. If an ordinary user could have
	// changed the file or the files beside it, that is their code running as
	// SYSTEM, so it doesn't run (acl.go).
	if refusal := consumeACLRefusal(full); refusal != "" {
		s.log.Errorf("%s", refusal)
		return -2, refusal
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// The file itself, by its full path, with no arguments and no shell.
	// directCommand is per-OS: on Windows it hands the batch-file case one
	// quoted path, so a save folder with spaces (C:\Program Files\PTI) works.
	cmd := directCommand(ctx, full)
	cmd.Dir = s.cfg.SavePath
	outB, err := cmd.CombinedOutput()
	out := mask.DigitRuns(strings.TrimSpace(string(outB)))
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
		return -2, strings.TrimSpace(out + "\n" + err.Error())
	}
	return 0, out
}
