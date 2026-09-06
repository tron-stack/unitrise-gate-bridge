//go:build windows

package main

// The tray: the agent's visible face, folded into the single binary as
// `unitrise-gate tray` (installed to run at login). It only ever READS the
// localhost status API plus the dashboard's one harmless force action - the
// sync core runs in the service, and quitting the tray never touches it.
// Windows-only: the exe is built -H=windowsgui so a login launch never
// flashes a console. macOS gets a real menu-bar app with the Mac release
// pass (Cocoa wants an app bundle, not a bare binary).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"fyne.io/systray"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/trayicon"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/ui"
)

// The dashboard binds 127.0.0.1 on DefaultPort, bumping up to +4 when taken
// (two agents on one machine) - probe the same window.
const trayPortProbes = 5

// traySnapshot mirrors the fields the tray needs from status.Snapshot
// (decoded loosely on purpose - an older agent answering with fewer fields
// must not break the tray).
type traySnapshot struct {
	AgentVersion string    `json:"agentVersion"`
	FacilityName string    `json:"facilityName"`
	Provider     string    `json:"provider"`
	CodeCount    int       `json:"codeCount"`
	LastApplyAt  time.Time `json:"lastApplyAt"`
	OK           bool      `json:"ok"`
	Detail       string    `json:"detail"`
	// Pointer: an older agent without the field decodes to nil, which must
	// read as "paired" (it wouldn't be running otherwise on old builds).
	Paired          *bool  `json:"paired"`
	UpdateAvailable string `json:"updateAvailable"`
}

var trayHTTP = &http.Client{Timeout: 2 * time.Second}

// findAgent probes the dashboard ports and returns (baseURL, snapshot, ok).
func findAgent() (string, traySnapshot, bool) {
	for p := ui.DefaultPort; p < ui.DefaultPort+trayPortProbes; p++ {
		base := fmt.Sprintf("http://127.0.0.1:%d", p)
		resp, err := trayHTTP.Get(base + "/api/status")
		if err != nil {
			continue
		}
		var s traySnapshot
		err = json.NewDecoder(resp.Body).Decode(&s)
		resp.Body.Close()
		if err == nil {
			return base, s, true
		}
	}
	return "", traySnapshot{}, false
}

func agoShort(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func trayCmd() error {
	systray.Run(trayReady, func() {})
	return nil
}

func trayReady() {
	systray.SetIcon(trayicon.Off())
	systray.SetTooltip("UnitRise Gate Bridge")

	title := systray.AddMenuItem("UnitRise Gate Bridge", "")
	title.Disable()
	statusLine := systray.AddMenuItem("Looking for the agent…", "")
	statusLine.Disable()
	detailLine := systray.AddMenuItem("", "")
	detailLine.Disable()
	detailLine.Hide()
	systray.AddSeparator()
	openItem := systray.AddMenuItem("Open Gate Bridge", "Status, gate codes, activity, service control")
	syncItem := systray.AddMenuItem("Sync now", "Push a full update to the gate software")
	updateItem := systray.AddMenuItem("", "Download, verify, and restart onto the new version")
	updateItem.Hide()
	systray.AddSeparator()
	verItem := systray.AddMenuItem("Version "+api.AgentVersion, "")
	verItem.Disable()
	quitItem := systray.AddMenuItem("Quit", "Close this icon (the sync service keeps running)")

	// The tray never blocks on the poll: state lands over a channel from a
	// single poller goroutine, menu clicks are handled as they come.
	type trayState struct {
		base string
		snap traySnapshot
		up   bool
	}
	states := make(chan trayState, 1)
	go func() {
		for {
			base, snap, up := findAgent()
			states <- trayState{base, snap, up}
			time.Sleep(5 * time.Second)
		}
	}()

	cur := trayState{}
	for {
		select {
		case cur = <-states:
			if cur.up && cur.snap.UpdateAvailable != "" {
				updateItem.SetTitle("Install update " + cur.snap.UpdateAvailable)
				updateItem.Show()
			} else {
				updateItem.Hide()
			}
			switch {
			case !cur.up:
				systray.SetIcon(trayicon.Off())
				systray.SetTooltip("UnitRise Gate Bridge - agent not running")
				statusLine.SetTitle("Agent not running")
				detailLine.SetTitle("Start it from the Services list (UnitRise Gate Bridge)")
				detailLine.Show()
				syncItem.Disable()
			case cur.snap.Paired != nil && !*cur.snap.Paired:
				// Setup mode: the agent is up but has no credentials yet -
				// the dashboard's pairing form is one click away.
				systray.SetIcon(trayicon.Warn())
				systray.SetTooltip("UnitRise Gate Bridge - needs setup")
				statusLine.SetTitle("Not connected yet")
				detailLine.SetTitle("Open the dashboard to enter your credentials")
				detailLine.Show()
				syncItem.Disable()
			case cur.snap.OK:
				systray.SetIcon(trayicon.OK())
				where := cur.snap.FacilityName
				if where == "" {
					where = "your facility"
				}
				line := fmt.Sprintf("Online - %d codes - synced %s", cur.snap.CodeCount, agoShort(cur.snap.LastApplyAt))
				systray.SetTooltip("UnitRise Gate Bridge - " + line)
				statusLine.SetTitle(line)
				detailLine.SetTitle(where + " (" + cur.snap.Provider + ")")
				detailLine.Show()
				syncItem.Enable()
			default:
				systray.SetIcon(trayicon.Warn())
				systray.SetTooltip("UnitRise Gate Bridge - needs attention")
				statusLine.SetTitle("Needs attention")
				d := cur.snap.Detail
				if d == "" {
					d = "Open the dashboard for details"
				}
				if len(d) > 60 {
					d = d[:57] + "…"
				}
				detailLine.SetTitle(d)
				detailLine.Show()
				syncItem.Enable()
			}
		case <-openItem.ClickedCh:
			// The control window (its own process; a second click focuses
			// the existing window rather than spawning another).
			if exe, err := os.Executable(); err == nil {
				exec.Command(exe, "window").Start() //nolint:errcheck
			}
		case <-syncItem.ClickedCh:
			if cur.base != "" {
				go func(base string) {
					resp, err := trayHTTP.Post(base+"/api/force", "text/plain", nil)
					if err == nil {
						resp.Body.Close()
					}
				}(cur.base)
			}
		case <-updateItem.ClickedCh:
			if cur.base != "" {
				updateItem.SetTitle("Installing update…")
				go func(base string) {
					// The guard wants JSON; a longer timeout - it downloads.
					c := &http.Client{Timeout: 3 * time.Minute}
					resp, err := c.Post(base+"/api/update", "application/json", strings.NewReader("{}"))
					if err == nil {
						resp.Body.Close()
					}
					// The agent restarts itself; the poller repaints state.
				}(cur.base)
			}
		case <-quitItem.ClickedCh:
			systray.Quit()
			return
		}
	}
}
