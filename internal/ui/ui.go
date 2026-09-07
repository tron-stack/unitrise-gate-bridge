// Package ui serves the agent's local dashboard - the UnitRise-styled
// equivalent of the storEDGE Gate window. Bound to 127.0.0.1 ONLY: it's a
// status window plus the pairing form, not a remote admin surface. It exposes
// no secrets; its actions are force-full-update (harmless by design) and
// pairing, which is guarded below.
package ui

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/brand"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/status"
)

//go:embed index.html
var page []byte

//go:embed about.html
var aboutPage []byte

const DefaultPort = 47810

// PairRequest is the dashboard pairing form. Endpoint may be blank (default
// applies); everything else is required.
type PairRequest struct {
	AccessKey    string `json:"accessKey"`
	AccessSecret string `json:"accessSecret"`
	FacilityID   string `json:"facilityId"`
	SavePath     string `json:"savePath"`
	APIEndpoint  string `json:"apiEndpoint"`
}

// PairResult is what a successful live-validated pairing reports back.
type PairResult struct {
	FacilityName string `json:"facilityName"`
	Provider     string `json:"provider"`
	CodeCount    int    `json:"codeCount"`
}

// Hooks are the agent actions the dashboard can trigger. OnForce routes to
// the CURRENT syncer; OnPair validates the credentials LIVE (API + save-path
// probe) and only then saves + (re)starts syncing - it never stores an
// unproven config.
type Hooks struct {
	OnForce func()
	OnPair  func(PairRequest) (PairResult, error)
	// OnUpdate downloads + installs the latest published agent (checksum-
	// verified) and, when running as a service, restarts onto it. Returns a
	// human summary line.
	OnUpdate func() (string, error)
}

// isLoopbackHost: the request's Host header names a loopback literal. This is
// the DNS-rebinding gate and it wraps the WHOLE mux (audit 2026-09-07 H3):
// a page at evil.com whose DNS was rebound to 127.0.0.1 becomes same-origin
// with this server and could otherwise READ /api/roster - and the gate codes
// in it ARE secrets. Every legitimate caller addresses 127.0.0.1/localhost.
func isLoopbackHost(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// isLoopbackOrigin: an absent Origin (same-origin navigation, the tray's Go
// client, curl) passes; a present one must PARSE to a loopback host. The old
// prefix match accepted http://127.0.0.1.evil.com (audit 2026-09-07 M1).
func isLoopbackOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

// localBrowserGuard blocks the ways a hostile web page could reach a
// localhost API from the site clerk's browser:
//   - CSRF: we only accept application/json, which browsers won't send
//     cross-origin without a preflight we never approve - and any Origin
//     header that doesn't parse to loopback is refused outright.
//   - DNS rebinding: the Host check (also enforced mux-wide in Serve).
func localBrowserGuard(w http.ResponseWriter, r *http.Request) bool {
	if !isLoopbackHost(r) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return false
	}
	if !isLoopbackOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return false
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "json only", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

// Serve starts the dashboard listener. Returns the bound address (port may
// have been bumped if the default was taken - e.g. a second agent on one
// machine).
func Serve(port int, hooks Hooks) (string, error) {
	if port == 0 {
		port = DefaultPort
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	// About is its own PAGE, not a modal: it must be escapable by ordinary
	// navigation in every host (browser tab, the WebView2 window) - a modal
	// that misbehaves traps the whole dashboard (on-site 2026-09-07).
	mux.HandleFunc("/about", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(aboutPage) //nolint:errcheck
	})
	mux.HandleFunc("/assets/falcon-mark.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(brand.FalconMark) //nolint:errcheck
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status.Get())
	})
	mux.HandleFunc("/api/roster", func(w http.ResponseWriter, r *http.Request) {
		rows := status.Roster()
		if rows == nil {
			rows = []status.RosterRow{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"rows": rows})
	})
	mux.HandleFunc("/api/force", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		// Not content-type-gated (the tray posts text/plain), but a browser's
		// cross-site POST carries the page's Origin - refuse it. A drive-by
		// page hammering force = continuous vendor-file rewrites + consume
		// command runs, which is not "harmless" (audit 2026-09-07 M2).
		if !isLoopbackOrigin(r) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		hooks.OnForce()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/update", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		// Same browser guards as pairing: installing a binary is not an
		// action a hostile web page gets to trigger.
		if !localBrowserGuard(w, r) {
			return
		}
		msg, err := hooks.OnUpdate()
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"message": msg})
	})
	mux.HandleFunc("/api/pair", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if !localBrowserGuard(w, r) {
			return
		}
		var req PairRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		res, err := hooks.OnPair(req)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(res)
	})

	// Try the preferred port, then a few above it (two agents on one PC).
	var ln net.Listener
	var err error
	for p := port; p < port+5; p++ {
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			break
		}
	}
	if err != nil {
		return "", fmt.Errorf("dashboard listen: %w", err)
	}
	// The Host gate wraps EVERYTHING - the roster and status reads included
	// (audit 2026-09-07 H3). The window's proxy rewrites Host to loopback
	// before forwarding, so it passes.
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
	go http.Serve(ln, guarded) //nolint:errcheck - lives for the process lifetime
	return "http://" + ln.Addr().String(), nil
}
