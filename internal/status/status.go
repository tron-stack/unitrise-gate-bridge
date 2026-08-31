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

var (
	mu   sync.Mutex
	snap Snapshot
	ring []string
)

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
