//go:build windows

package main

// The desktop control window - the Gate Bridge's own app window (user ask
// 2026-09-06: "I don't want it to live in the browser"). A WebView2 shell
// (cgo-free binding, so the ubuntu release runner keeps cross-compiling)
// around a tiny local shell page that:
//
//   - embeds the agent's dashboard (status, activity, gate-codes roster,
//     pairing, updates) when the agent is up,
//   - shows an honest offline panel when it isn't - which is exactly when
//     the window's native powers matter: a service-control bar that can
//     START and STOP the Windows service (each action elevates via UAC;
//     the window itself stays unelevated).
//
// The shell page is served by THIS process on its own loopback port; the
// service-control endpoints live here, not on the agent - the agent IS the
// service and can't start itself. If the WebView2 runtime is missing
// (rare - it ships with Windows 10/11), the window falls back to the
// browser dashboard with an explanation.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/service"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/ui"
)

const windowTitle = "UnitRise Gate Bridge"

var shellHTTP = &http.Client{Timeout: 1500 * time.Millisecond}

// findAgentPort probes the dashboard's port window; 0 = agent not reachable.
func findAgentPort() int {
	for p := ui.DefaultPort; p < ui.DefaultPort+5; p++ {
		resp, err := shellHTTP.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", p))
		if err == nil {
			resp.Body.Close()
			return p
		}
	}
	return 0
}

// shellGuard: loopback Host + no foreign Origin. The start/stop endpoints
// only pop UAC prompts, but a drive-by page shouldn't get to pop them.
func shellGuard(w http.ResponseWriter, r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && !strings.HasPrefix(o, "http://127.0.0.1") &&
		!strings.HasPrefix(o, "http://localhost") {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return false
	}
	return true
}

// serveShell starts the window's own loopback server and returns its URL.
func serveShell() (string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(shellPage))
	})
	mux.HandleFunc("/shell/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"service":   service.Status(),
			"agentPort": findAgentPort(),
		})
	})
	svcAction := func(verb string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST only", http.StatusMethodNotAllowed)
				return
			}
			if !shellGuard(w, r) {
				return
			}
			// Elevate just the action (UAC prompt) - the window itself
			// stays a normal user process.
			if err := relaunchElevated("service " + verb); err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}
	mux.HandleFunc("/shell/start", svcAction("start"))
	mux.HandleFunc("/shell/stop", svcAction("stop"))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go http.Serve(ln, mux) //nolint:errcheck - lives for the window's lifetime
	return "http://" + ln.Addr().String(), nil
}

// focusExistingWindow brings an already-open control window forward instead
// of spawning a second one. Title match is enough - the title is ours.
func focusExistingWindow() bool {
	find := user32.NewProc("FindWindowW")
	hw, _, _ := find.Call(0, uintptr(unsafe.Pointer(utf16p(windowTitle))))
	if hw == 0 {
		return false
	}
	pShowWindow.Call(hw, 9 /*SW_RESTORE*/) //nolint:errcheck
	pSetForegroundWindow.Call(hw)          //nolint:errcheck
	return true
}

func windowCmd() error {
	if focusExistingWindow() {
		return nil
	}
	shellURL, err := serveShell()
	if err != nil {
		return err
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  windowTitle,
			Width:  1010,
			Height: 760,
			IconId: 1, // the exe's embedded hexagon (winres -icon)
			Center: true,
		},
	})
	if w == nil {
		// No WebView2 runtime (rare). Be honest and still useful.
		openBrowser(fmt.Sprintf("http://127.0.0.1:%d", ui.DefaultPort)) //nolint:errcheck
		msgBox("The app window needs Microsoft's WebView2 runtime, which isn't on this PC - opening the dashboard in your browser instead.\n\nTo get the window: install \"WebView2 Runtime\" from Microsoft, then relaunch.", mbOK|mbIconInfo)
		return nil
	}
	defer w.Destroy()
	w.Navigate(shellURL)
	w.Run()
	return nil
}

// The shell page: a slim Rise-styled frame - the agent dashboard fills it
// when up; a service bar at the bottom is always present.
const shellPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"/>
<title>UnitRise Gate Bridge</title>
<style>
  :root{--paper:#FAF8F5;--card:#FFFFFF;--line:#E9E5DC;--ink:#111322;--body:#4B4E63;--muted:#8A8DA3;
        --amber:#F59E0B;--indigo:#1E1B4B;--success:#22C55E;--danger:#EF4444}
  *{box-sizing:border-box} html,body{margin:0;height:100%}
  body{display:flex;flex-direction:column;background:var(--paper);font:14px/1.5 "Segoe UI",-apple-system,sans-serif;color:var(--ink)}
  #frame{flex:1;border:0;width:100%;display:none}
  #offline{flex:1;display:flex;align-items:center;justify-content:center}
  .panel{max-width:440px;text-align:center;padding:32px;background:var(--card);border:1px solid var(--line);border-radius:16px}
  .panel h1{font-size:19px;margin:0 0 8px;font-weight:700}
  .panel p{color:var(--body);margin:0 0 18px}
  .hex{width:44px;height:44px;margin-bottom:12px}
  #bar{display:flex;align-items:center;gap:10px;padding:9px 16px;border-top:1px solid var(--line);background:var(--card);font-size:12.5px}
  .dot{width:9px;height:9px;border-radius:99px;background:var(--muted);flex:none}
  .dot.on{background:var(--success)} .dot.off{background:var(--danger)} .dot.mid{background:var(--amber)}
  #svcText{color:var(--body);font-weight:600}
  button{appearance:none;border:0;cursor:pointer;border-radius:999px;padding:7px 16px;font-weight:700;font-size:12.5px;
         background:var(--amber);color:var(--indigo)}
  button.ghost{background:var(--card);color:var(--ink);border:1px solid var(--line)}
  button:disabled{opacity:.5;cursor:default}
  #hint{color:var(--muted);margin-left:auto}
</style></head>
<body>
  <iframe id="frame" title="Gate Bridge dashboard"></iframe>
  <div id="offline"><div class="panel">
    <svg class="hex" viewBox="0 0 100 100" aria-hidden="true"><polygon points="50,4 92,27 92,73 50,96 8,73 8,27" fill="none" stroke="#111322" stroke-width="5"/><polygon points="50,22 76,36 76,64 50,78 24,64 24,36" fill="#F59E0B"/></svg>
    <h1 id="offTitle">Looking for the sync service…</h1>
    <p id="offBody">One moment.</p>
    <button id="offStart" style="display:none">Start the service</button>
  </div></div>
  <div id="bar">
    <span class="dot" id="svcDot"></span><span id="svcText">Checking service…</span>
    <button class="ghost" id="svcBtn" style="display:none"></button>
    <span id="hint"></span>
  </div>
<script>
  const $ = (id) => document.getElementById(id);
  let agentPort = 0, svc = "unknown", busyUntil = 0;

  async function act(verb) {
    busyUntil = Date.now() + 8000; // give the UAC prompt + SCM a beat
    $("svcBtn").disabled = true; $("offStart").disabled = true;
    try { await fetch("/shell/" + verb, { method: "POST" }); } catch {}
  }
  $("offStart").onclick = () => act("start");
  $("svcBtn").onclick = () => act($("svcBtn").dataset.verb);

  function paint() {
    const busy = Date.now() < busyUntil;
    const up = agentPort > 0;
    // main area
    if (up) {
      const src = "http://127.0.0.1:" + agentPort + "/";
      if ($("frame").dataset.src !== src) { $("frame").src = src; $("frame").dataset.src = src; }
      $("frame").style.display = ""; $("offline").style.display = "none";
    } else {
      $("frame").style.display = "none"; $("offline").style.display = "flex";
      if (svc === "running" || svc === "starting" || busy) {
        $("offTitle").textContent = "Starting up…";
        $("offBody").textContent = "The sync service is coming online.";
        $("offStart").style.display = "none";
      } else if (svc === "not-installed") {
        $("offTitle").textContent = "The service isn't installed";
        $("offBody").textContent = "Run the Gate Bridge installer again to repair this machine.";
        $("offStart").style.display = "none";
      } else {
        $("offTitle").textContent = "The sync service is stopped";
        $("offBody").textContent = "Gate codes aren't syncing while it's stopped - the gate keeps admitting from its last list.";
        $("offStart").style.display = ""; $("offStart").disabled = busy;
      }
    }
    // service bar
    const dot = $("svcDot"), txt = $("svcText"), btn = $("svcBtn");
    btn.disabled = busy;
    if (svc === "running") {
      dot.className = "dot on"; txt.textContent = "Service running";
      btn.style.display = ""; btn.textContent = "Stop service"; btn.dataset.verb = "stop";
      $("hint").textContent = "Stopping pauses code sync - the gate keeps its current list.";
    } else if (svc === "starting" || svc === "stopping" || busy) {
      dot.className = "dot mid"; txt.textContent = busy ? "Working… (allow the administrator prompt)" : "Service " + svc + "…";
      btn.style.display = "none"; $("hint").textContent = "";
    } else if (svc === "not-installed") {
      dot.className = "dot off"; txt.textContent = "Service not installed";
      btn.style.display = "none"; $("hint").textContent = "";
    } else {
      dot.className = "dot off"; txt.textContent = "Service stopped";
      btn.style.display = ""; btn.textContent = "Start service"; btn.dataset.verb = "start";
      $("hint").textContent = "";
    }
  }

  async function tick() {
    try {
      const r = await fetch("/shell/state");
      const d = await r.json();
      svc = d.service; agentPort = d.agentPort;
    } catch { svc = "unknown"; agentPort = 0; }
    paint();
  }
  tick(); setInterval(tick, 2000);
</script>
</body></html>`
