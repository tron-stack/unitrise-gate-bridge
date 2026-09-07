//go:build windows

package main

// The desktop control window - the Gate Bridge's own app window. A WebView2
// shell (cgo-free binding, so the ubuntu release runner keeps
// cross-compiling) that navigates DIRECTLY to the agent's dashboard through
// a local same-origin reverse proxy - no iframe, no embedding layer at all
// (two real-Windows rounds showed embedded frames not rendering,
// 2026-09-06). What makes it an app and not a browser tab:
//
//   - a service-control bar INJECTED into every page (webview Init script):
//     live service state plus Start/Stop, each elevating via UAC while the
//     window stays unelevated - the agent IS the service and can't start
//     itself, so control lives in this process;
//   - when the agent is down, the proxy serves a self-recovering offline
//     panel instead of a browser error, and the bar keeps working - which
//     is exactly when Start matters.
//
// If the WebView2 runtime is missing (rare - it ships with Windows 10/11),
// the window falls back to the browser dashboard with an explanation.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/brand"
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

// serveShell starts the window's loopback server: /shell/* is the window's
// own control surface; everything else reverse-proxies to the agent, so the
// dashboard IS this origin's content. Agent down = the offline panel.
func serveShell() (string, error) {
	proxy := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			port := findAgentPort()
			if port == 0 {
				port = ui.DefaultPort // ErrorHandler will serve the offline panel
			}
			r.URL.Scheme = "http"
			r.URL.Host = fmt.Sprintf("127.0.0.1:%d", port)
			// The agent's browser guards pin Host to loopback - keep it true.
			r.Host = r.URL.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// API calls from a still-loaded dashboard get a plain error;
			// page navigations get the offline panel.
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.Error(w, "agent not reachable", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(offlinePage)) //nolint:errcheck
		},
	}
	mux := http.NewServeMux()
	mux.Handle("/", proxy)
	// The mark, served by the WINDOW process itself - the offline panel needs
	// it exactly when the agent (which serves /assets/…) is down.
	mux.HandleFunc("/shell/logo.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(brand.FalconMark) //nolint:errcheck
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
			Height: 780,
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
	// The service bar rides EVERY page this window shows (dashboard and
	// offline panel alike) - injected at document creation.
	w.Init(strings.ReplaceAll(barScript, "__SHELL_ORIGIN__", shellURL))
	w.Navigate(shellURL + "/")
	w.Run()
	return nil
}

// barScript is injected into every document: the persistent service bar +
// the up/down navigation logic (dashboard when the agent answers, offline
// panel when it doesn't).
const barScript = `
(function () {
  if (location.origin !== "__SHELL_ORIGIN__") return; // never decorate foreign pages
  var misses = 0;
  function ready(fn) {
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", fn);
    else fn();
  }
  ready(function () {
    var bar = document.createElement("div");
    bar.id = "urShellBar";
    bar.style.cssText = "position:fixed;left:0;right:0;bottom:0;z-index:2147483647;display:flex;align-items:center;gap:10px;" +
      "padding:9px 16px;border-top:1px solid #E9E5DC;background:#FFFFFF;font:12.5px/1.4 Inter,'Segoe UI',sans-serif;color:#3E4A60";
    bar.innerHTML =
      '<span id="urSvcDot" style="width:9px;height:9px;border-radius:99px;background:#6B7085;flex:none"></span>' +
      '<span id="urSvcText" style="font-weight:600">Checking service…</span>' +
      '<button id="urSvcBtn" style="display:none;appearance:none;cursor:pointer;border-radius:999px;padding:6px 15px;font-weight:700;' +
      'font-size:12.5px;background:#FFFFFF;color:#131F35;border:1px solid #E9E5DC"></button>' +
      '<span id="urSvcHint" style="margin-left:auto;color:#6B7085"></span>';
    document.body.appendChild(bar);
    document.body.style.paddingBottom = "52px";

    var busyUntil = 0;
    var btn = document.getElementById("urSvcBtn");
    btn.onclick = function () {
      busyUntil = Date.now() + 8000; // give UAC + the SCM a beat
      btn.disabled = true;
      fetch("/shell/" + btn.dataset.verb, { method: "POST" }).catch(function () {});
    };

    function paint(svc, agentPort) {
      var busy = Date.now() < busyUntil;
      var dot = document.getElementById("urSvcDot");
      var txt = document.getElementById("urSvcText");
      var hint = document.getElementById("urSvcHint");
      btn.disabled = busy;
      if (svc === "running") {
        dot.style.background = "#15803D";
        txt.textContent = agentPort ? "Service running" : "Service running - agent coming online…";
        btn.style.display = ""; btn.textContent = "Stop service"; btn.dataset.verb = "stop";
        hint.textContent = "Stopping pauses code sync - the gate keeps its current list.";
      } else if (svc === "starting" || svc === "stopping" || busy) {
        dot.style.background = "#C38B4E";
        txt.textContent = busy ? "Working… (allow the administrator prompt)" : "Service " + svc + "…";
        btn.style.display = "none"; hint.textContent = "";
      } else if (svc === "not-installed") {
        dot.style.background = "#B91C1C";
        txt.textContent = "Service not installed - run the installer again";
        btn.style.display = "none"; hint.textContent = "";
      } else {
        dot.style.background = "#B91C1C";
        txt.textContent = "Service stopped - gate codes are not syncing";
        btn.style.display = ""; btn.textContent = "Start service"; btn.dataset.verb = "start";
        hint.textContent = "";
      }
    }

    function tick() {
      fetch("/shell/state").then(function (r) { return r.json(); }).then(function (d) {
        paint(d.service, d.agentPort);
        var onOffline = !!window.__UR_OFFLINE;
        if (d.agentPort > 0) {
          misses = 0;
          if (onOffline) location.replace("/"); // agent is back - load the dashboard
        } else if (!onOffline) {
          // Dashboard loaded but agent gone (stopped, or restarting after an
          // update): after a few misses, swap to the offline panel.
          misses++;
          if (misses >= 3) location.replace("/");
        }
      }).catch(function () {});
    }
    tick();
    setInterval(tick, 2000);
  });
})();
`

// offlinePage is what the proxy serves when the agent isn't answering - a
// Rise-styled panel instead of a browser error. The injected bar supplies
// the Start button and swaps back to the dashboard when the agent returns.
const offlinePage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"/><title>UnitRise Gate Bridge</title></head>
<body style="margin:0;height:100vh;display:flex;align-items:center;justify-content:center;background:#FAF8F5;font:14px/1.5 Inter,'Segoe UI',sans-serif;color:#131F35">
<script>window.__UR_OFFLINE = true;</script>
<div style="max-width:440px;text-align:center;padding:32px;background:#FFFFFF;border:1px solid #E9E5DC;border-radius:16px">
  <img src="/shell/logo.png" alt="" style="height:56px;width:auto;margin-bottom:12px"/>
  <h1 style="font-size:19px;margin:0 0 8px;font-weight:700">The sync agent isn't running</h1>
  <p style="color:#3E4A60;margin:0">Gate codes aren't syncing right now - the gate keeps admitting from its
  last list. Use <b>Start service</b> below; this page switches to the live dashboard by itself once the
  agent is up.</p>
</div>
</body></html>`
