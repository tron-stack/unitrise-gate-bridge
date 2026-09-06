// Package status is the shared, thread-safe view the local dashboard reads:
// current sync state + a ring buffer of recent log lines. The agent stays a
// single static binary - the UI is a localhost page served from memory.
package status

import (
	"sync"
	"time"
)

type Snapshot struct {
	AgentVersion string    `json:"agentVersion"`
	StartedAt    time.Time `json:"startedAt"`

	// Paired=false is SETUP MODE: the agent is up serving this dashboard but
	// has no (valid) credentials yet - the dashboard's pairing form is the
	// way in. State is "setup" or "running".
	Paired bool   `json:"paired"`
	State  string `json:"state"`
	// Set by the update watcher when a newer agent is published - the
	// dashboard and tray render a one-click "Install update" from it.
	UpdateAvailable string `json:"updateAvailable"`

	FacilityName string `json:"facilityName"`
	Provider     string `json:"provider"`
	SavePath     string `json:"savePath"`
	TargetFile   string `json:"targetFile"`
	ConsumeCmd   string `json:"consumeCmd"`

	CodeCount   int       `json:"codeCount"`
	LastApplyAt time.Time `json:"lastApplyAt"`
	LastHash    string    `json:"lastHash"`
	PollSeconds int       `json:"pollSeconds"`
	NextPollAt  time.Time `json:"nextPollAt"`

	OK     bool   `json:"ok"`
	Detail string `json:"detail"`

	Log []string `json:"log"`
}

const ringSize = 250

// RosterRow is one credential as last APPLIED to the gate file - what the
// dashboard/window's "Gate codes" view lists. Kept out of Snapshot so
// /api/status stays light; served on demand at /api/roster. Read-only by
// design: codes are managed in the UnitRise console, never from the gate PC
// (the local dashboard has no auth - it must hold no write powers over
// access).
type RosterRow struct {
	Unit   string `json:"unit"`
	Tenant string `json:"tenant"`
	Code   string `json:"code"`
	Status string `json:"status"`
	TZ     string `json:"tz"`
}

var (
	mu     sync.Mutex
	snap   Snapshot
	ring   []string
	roster []RosterRow
)

// SetRoster replaces the applied-roster view (called by the syncer after the
// vendor file lands).
func SetRoster(rows []RosterRow) {
	mu.Lock()
	roster = rows
	mu.Unlock()
}

func Roster() []RosterRow {
	mu.Lock()
	defer mu.Unlock()
	return append([]RosterRow(nil), roster...)
}

func Init(agentVersion string) {
	mu.Lock()
	defer mu.Unlock()
	snap.AgentVersion = agentVersion
	snap.StartedAt = time.Now()
	snap.OK = true
}

// Update applies a partial mutation under the lock.
func Update(fn func(s *Snapshot)) {
	mu.Lock()
	defer mu.Unlock()
	fn(&snap)
}

// AppendLog is hooked into the logger so the dashboard mirrors the log pane.
func AppendLog(line string) {
	mu.Lock()
	defer mu.Unlock()
	ring = append(ring, line)
	if len(ring) > ringSize {
		ring = ring[len(ring)-ringSize:]
	}
}

func Get() Snapshot {
	mu.Lock()
	defer mu.Unlock()
	out := snap
	out.Log = append([]string(nil), ring...)
	return out
}
