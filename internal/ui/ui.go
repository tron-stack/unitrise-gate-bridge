// Package ui serves the agent's local dashboard - the UnitRise-styled
// equivalent of the storEDGE Gate window. Bound to 127.0.0.1 ONLY: it's a
// status window, not a remote admin surface. It exposes no secrets and its
// single action (force full update) is harmless-by-design.
package ui

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/status"
)

//go:embed index.html
var page []byte

const DefaultPort = 47810

// Serve starts the dashboard listener. onForce is the syncer's
// ForceFullUpdate. Returns the bound address (port may have been bumped if the
// default was taken - e.g. a second agent on one machine).
func Serve(port int, onForce func()) (string, error) {
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
		onForce()
		w.WriteHeader(http.StatusNoContent)
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
