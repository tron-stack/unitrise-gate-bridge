// unitrise-gate-tray - the agent's visible face: a branded tray icon
// (Windows) / menu-bar item (macOS) so the Gate Bridge never lives secretly
// in a terminal. Deliberately a SEPARATE binary from the agent:
//
//   - the agent is the audited sync core, running headless as a service; the
//     tray is a per-user companion that only ever READS the localhost status
//     API (plus the one harmless "force full update" action the dashboard
//     already exposes),
//   - Windows builds it with -H=windowsgui so launching it at login never
//     flashes a console window - which a console-subsystem binary always
//     would, and which is the exact papercut this app exists to remove.
//
// It discovers the agent on the dashboard's localhost ports, polls status
// every few seconds, and shows one of three states: amber (healthy), red
// (agent reporting a problem, with the detail in the menu), gray (agent not
// running). Installed to run at login by install.ps1 (Windows Run key); the
// macOS LaunchAgent lands with the Mac release pass.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"fyne.io/systray"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/trayicon"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/ui"
)

// The dashboard binds 127.0.0.1 on DefaultPort, bumping up to +4 when taken
// (two agents on one machine) - probe the same window.
const portProbes = 5

// snapshot mirrors the fields the tray needs from internal/status.Snapshot
// (decoded loosely on purpose - an older agent answering with fewer fields
// must not break the tray).
type snapshot struct {
	AgentVersion string    `json:"agentVersion"`
	FacilityName string    `json:"facilityName"`
	Provider     string    `json:"provider"`
	CodeCount    int       `json:"codeCount"`
	LastApplyAt  time.Time `json:"lastApplyAt"`
	OK           bool      `json:"ok"`
	Detail       string    `json:"detail"`
	// Pointer: an older agent without the field decodes to nil, which must
	// read as "paired" (it wouldn't be running otherwise on old builds).
	Paired *bool `json:"paired"`
}

var httpClient = &http.Client{Timeout: 2 * time.Second}

// findAgent probes the dashboard ports and returns (baseURL, snapshot, ok).
func findAgent() (string, snapshot, bool) {
	for p := ui.DefaultPort; p < ui.DefaultPort+portProbes; p++ {
		base := fmt.Sprintf("http://127.0.0.1:%d", p)
		resp, err := httpClient.Get(base + "/api/status")
		if err != nil {
			continue
		}
		var s snapshot
		err = json.NewDecoder(resp.Body).Decode(&s)
		resp.Body.Close()
		if err == nil {
			return base, s, true
		}
	}
	return "", snapshot{}, false
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func ago(t time.Time) string {
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

func main() {
	systray.Run(onReady, func() {})
}

func onReady() {
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
	openItem := systray.AddMenuItem("Open dashboard", "Open the local status page")
	syncItem := systray.AddMenuItem("Sync now", "Push a full update to the gate software")
	systray.AddSeparator()
	verItem := systray.AddMenuItem("Tray "+api.AgentVersion, "")
	verItem.Disable()
	quitItem := systray.AddMenuItem("Quit", "Close this icon (the sync service keeps running)")

	// The tray never blocks on the poll: state lands over a channel from a
	// single poller goroutine, menu clicks are handled as they come.
	type state struct {
		base string
		snap snapshot
		up   bool
	}
	states := make(chan state, 1)
	go func() {
		for {
			base, snap, up := findAgent()
			states <- state{base, snap, up}
			time.Sleep(5 * time.Second)
		}
	}()

	cur := state{}
	for {
		select {
		case cur = <-states:
			switch {
			case !cur.up:
				systray.SetIcon(trayicon.Off())
				systray.SetTooltip("UnitRise Gate Bridge - agent not running")
				statusLine.SetTitle("Agent not running")
				detailLine.SetTitle("Start it: services list, or `unitrise-gate service start`")
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
				line := fmt.Sprintf("Online - %d codes - synced %s", cur.snap.CodeCount, ago(cur.snap.LastApplyAt))
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
			base := cur.base
			if base == "" {
				base = fmt.Sprintf("http://127.0.0.1:%d", ui.DefaultPort)
			}
			openBrowser(base)
		case <-syncItem.ClickedCh:
			if cur.base != "" {
				go func(base string) {
					resp, err := httpClient.Post(base+"/api/force", "text/plain", nil)
					if err == nil {
						resp.Body.Close()
					}
				}(cur.base)
			}
		case <-quitItem.ClickedCh:
			systray.Quit()
			return
		}
	}
}
