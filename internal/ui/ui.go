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
	"strings"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/status"
)

//go:embed index.html
var page []byte

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
}

// localBrowserGuard blocks the two ways a hostile web page could reach a
// localhost API from the site clerk's browser:
//   - CSRF: we only accept application/json, which browsers won't send
//     cross-origin without a preflight we never approve - and any Origin
//     header that isn't this page's own origin is refused outright.
//   - DNS rebinding: the Host header must be a loopback address; a rebound
//     hostname (attacker.com resolving to 127.0.0.1) fails this check.
func localBrowserGuard(w http.ResponseWriter, r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && !strings.HasPrefix(o, "http://127.0.0.1") &&
		!strings.HasPrefix(o, "http://localhost") && !strings.HasPrefix(o, "http://[::1]") {
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
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status.Get())
	})
	mux.HandleFunc("/api/force", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		hooks.OnForce()
		w.WriteHeader(http.StatusNoContent)
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
	go http.Serve(ln, mux) //nolint:errcheck - lives for the process lifetime
	return "http://" + ln.Addr().String(), nil
}
