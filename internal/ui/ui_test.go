package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/status"
)

func dashboard(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler(newMux(Hooks{
		OnForce:  func() {},
		OnPair:   func(PairRequest) (PairResult, error) { return PairResult{}, nil },
		OnUpdate: func() (string, error) { return "", nil },
	})))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srv *httptest.Server, path, host string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// DNS rebinding: a page whose name now points at 127.0.0.1 still sends its own
// name as Host, and is refused on every route.
func TestEveryRouteRefusesAForeignHost(t *testing.T) {
	srv := dashboard(t)
	for _, p := range []string{"/", "/api/status", "/api/roster", "/about"} {
		if code, _ := get(t, srv, p, "evil.example:47810"); code != http.StatusForbidden {
			t.Errorf("%s with a foreign Host answered %d, want 403", p, code)
		}
		if code, _ := get(t, srv, p, ""); code != http.StatusOK {
			t.Errorf("CONTROL: %s from 127.0.0.1 answered %d", p, code)
		}
	}
}

// The roster route serves only masked codes, whatever the syncer applied.
func TestTheRosterRouteServesMaskedCodes(t *testing.T) {
	status.SetRoster([]status.RosterRow{{Unit: "A14", Code: "482913", Status: "active"}})
	srv := dashboard(t)
	_, body := get(t, srv, "/api/roster", "")
	if strings.Contains(body, "482913") || !strings.Contains(body, "••••13") {
		t.Fatalf("/api/roster = %s, want the code masked to its last two digits", body)
	}
}
