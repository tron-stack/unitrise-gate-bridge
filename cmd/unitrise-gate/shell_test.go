package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// A stand-in agent that records every request the window's proxy hands it.
func fakeAgent(t *testing.T) (port int, hits *atomic.Int32) {
	t.Helper()
	hits = &atomic.Int32{}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"codeCount":3}`) //nolint:errcheck
	}))
	t.Cleanup(agent.Close)
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(agent.URL, "http://"))
	port, _ = strconv.Atoi(p)
	return port, hits
}

func shellServer(t *testing.T, agentPort int, actions *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newShellHandler(shellDeps{
		agentPort: func() int { return agentPort },
		svcStatus: func() string { return "running" },
		svcAction: func(verb string) error { *actions = append(*actions, verb); return nil },
		logo:      []byte("png"),
	}))
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, host, ctype string) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader("{}"))
	if host != "" {
		req.Host = host
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// DNS rebinding through the window: the proxy rewrites Host to loopback for
// the hop to the agent, so the check has to run on the ORIGINAL request, or a
// rebound page reads the dashboard through the window's port.
func TestTheWindowRefusesAForeignHostBeforeProxying(t *testing.T) {
	port, hits := fakeAgent(t)
	var actions []string
	srv := shellServer(t, port, &actions)
	for _, p := range []string{"/api/status", "/api/roster", "/", "/shell/state", "/shell/logo.png"} {
		if code := do(t, http.MethodGet, srv.URL+p, "rebound.example", ""); code != http.StatusForbidden {
			t.Errorf("%s with a foreign Host answered %d, want 403", p, code)
		}
	}
	if code := do(t, http.MethodPost, srv.URL+"/shell/start", "rebound.example:80", "application/json"); code != http.StatusForbidden {
		t.Errorf("/shell/start with a foreign Host answered %d, want 403", code)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("a foreign-Host request reached the agent %d time(s)", n)
	}
	if len(actions) != 0 {
		t.Fatalf("a foreign-Host request ran a service action: %v", actions)
	}
	// CONTROL: the window's own loopback requests go through.
	if code := do(t, http.MethodGet, srv.URL+"/api/status", "", ""); code != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("CONTROL: a loopback request is proxied: %d, %d hit(s)", code, hits.Load())
	}
	if code := do(t, http.MethodPost, srv.URL+"/shell/start", "", "application/json"); code != http.StatusNoContent || len(actions) != 1 {
		t.Fatalf("CONTROL: a loopback start runs: %d %v", code, actions)
	}
	// The JSON guard still stands on the actions.
	if code := do(t, http.MethodPost, srv.URL+"/shell/stop", "", "text/plain"); code != http.StatusUnsupportedMediaType {
		t.Fatalf("a non-JSON stop answered %d", code)
	}
}
