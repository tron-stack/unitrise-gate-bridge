package main

// The control window's loopback server, built here (portable) so its guards
// are tested on every platform; window_windows.go only listens and opens the
// WebView2 window onto it.
//
// ⚠ THE HOST GATE RUNS ON THE ORIGINAL REQUEST (2026-11-20). The proxy's
// Director rewrites Host to 127.0.0.1 for the hop to the agent, so the
// agent's own DNS-rebinding gate (internal/ui IsLoopbackHost) always saw a
// loopback Host and passed: a web page whose DNS was rebound to this port
// could read the dashboard through the window's server. The whole handler is
// wrapped in the same check BEFORE the Director runs.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/ui"
)

// shellDeps are the window process's own powers, injected so the handler is
// testable without a service or a UAC prompt.
type shellDeps struct {
	agentPort func() int         // 0 = agent not reachable
	svcStatus func() string      // the Windows service state
	svcAction func(string) error // "start" / "stop", elevating via UAC
	logo      []byte             // the mark, for the offline panel
}

// requireLoopbackHost refuses any request whose ORIGINAL Host isn't a
// loopback name, on every route the window serves.
func requireLoopbackHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ui.IsLoopbackHost(r) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// shellGuard: a PARSED loopback Origin (prefix matching passed
// http://127.0.0.1.evil.com - audit 2026-09-07 M1) and JSON content-type so a
// cross-site "simple POST" can never fire the UAC-popping start/stop - a
// browser only sends JSON cross-origin after a preflight we never approve.
// (The Host is already checked for every route by requireLoopbackHost.)
func shellGuard(w http.ResponseWriter, r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return false
		}
		if h := u.Hostname(); h != "127.0.0.1" && h != "localhost" && h != "::1" {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return false
		}
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "json only", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

// newShellHandler: /shell/* is the window's own control surface; everything
// else reverse-proxies to the agent, so the dashboard IS this origin's
// content. Agent down = the offline panel.
func newShellHandler(d shellDeps) http.Handler {
	proxy := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			port := d.agentPort()
			if port == 0 {
				port = ui.DefaultPort // ErrorHandler will serve the offline panel
			}
			r.URL.Scheme = "http"
			r.URL.Host = fmt.Sprintf("127.0.0.1:%d", port)
			// The hop to the agent addresses it by loopback. The ORIGINAL
			// Host was already checked by requireLoopbackHost.
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
		w.Write(d.logo) //nolint:errcheck
	})
	mux.HandleFunc("/shell/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"service":   d.svcStatus(),
			"agentPort": d.agentPort(),
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
			if err := d.svcAction(verb); err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}
	mux.HandleFunc("/shell/start", svcAction("start"))
	mux.HandleFunc("/shell/stop", svcAction("stop"))
	return requireLoopbackHost(mux)
}

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
