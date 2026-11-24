// Package ui serves the agent's local dashboard - the UnitRise-styled
// equivalent of the storEDGE Gate window. Bound to 127.0.0.1 ONLY: it's a
// status window plus the pairing form, not a remote admin surface.
//
// It has NO sign-in: any account on the gate PC (and any program one runs)
// can reach it. So what it serves holds no secret: the access secret never
// leaves config.json, the Gate codes roster carries each code masked to its
// last two digits (internal/status SetRoster), and the consume command's
// output is masked before it reaches the status or the log (internal/mask).
// Its actions are force-full-update, the self-update (signature-checked,
// internal/update) and pairing, which on an already paired machine can only
// replace the access key and secret; changing the server or the save folder
// takes an Administrator (`unitrise-gate pair` from an elevated prompt).
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
// applies). On a machine that is already paired only the key and secret may
// differ from what is stored (cmd/unitrise-gate pairing.go planDashboardPair).
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
// the CURRENT syncer; OnPair applies the dashboard's pairing rules, validates
// the credentials LIVE (API + save-path probe) and only then saves +
// (re)starts syncing - it never stores an unproven config.
type Hooks struct {
	OnForce func()
	OnPair  func(PairRequest) (PairResult, error)
	// OnUpdate downloads + installs the latest published agent (signature-
	// and checksum-verified, internal/update) and, when running as a service,
	// restarts onto it. Returns a human summary line.
	OnUpdate func() (string, error)
}

// IsLoopbackHost: the request's Host header names a loopback literal. This is
// the DNS-rebinding gate and it wraps the WHOLE mux (audit 2026-09-07 H3):
// a page at evil.com whose DNS was rebound to 127.0.0.1 becomes same-origin
// with this server and could otherwise read /api/status or drive /api/pair.
// Every legitimate caller addresses 127.0.0.1/localhost. The control
// window's proxy applies it to the ORIGINAL request too, before it rewrites
// Host for the hop to the agent.
func IsLoopbackHost(r *http.Request) bool {
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
	if !IsLoopbackHost(r) {
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
	mux := newMux(hooks)

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
	go http.Serve(ln, handler(mux)) //nolint:errcheck - lives for the process lifetime
	return "http://" + ln.Addr().String(), nil
}

// newMux is the dashboard's routes (Serve wraps them in the Host gate).
func newMux(hooks Hooks) *http.ServeMux {
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
	return mux
}

// handler wraps the dashboard mux in the Host gate. It covers EVERYTHING -
// the roster and status reads included (audit 2026-09-07 H3). The window's
// proxy checks the original request's Host itself before it rewrites Host
// to loopback for the hop here (cmd/unitrise-gate shell.go).
func handler(mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsLoopbackHost(r) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
