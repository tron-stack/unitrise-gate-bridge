package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// An agent that has not applied anything yet sends no lastApplyAt at all,
// never the zero time ("0001-01-01T00:00:00Z").
func TestAHeartbeatBeforeTheFirstApplySendsNoApplyTime(t *testing.T) {
	b, err := json.Marshal(Heartbeat{AgentVersion: "0.6.0", OS: "windows/amd64", OK: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "lastApplyAt") || strings.Contains(string(b), "0001-01-01") {
		t.Fatalf("heartbeat = %s, want no lastApplyAt before the first apply", b)
	}
	at := time.Date(2026, 11, 20, 15, 4, 5, 0, time.UTC)
	b, _ = json.Marshal(Heartbeat{LastApplyAt: &at})
	if !strings.Contains(string(b), `"lastApplyAt":"2026-11-20T15:04:05Z"`) {
		t.Fatalf("CONTROL: a real apply time is sent: %s", b)
	}
}

// A release file is fetched over https only: a plain-http link, however it
// got into the update answer, is refused before any request.
func TestDownloadRefusesPlainHTTP(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hit = true; w.Write([]byte("binary")) }))
	defer srv.Close()
	if _, err := New(nil).Download(srv.URL + "/unitrise-gate.exe"); err == nil || !strings.Contains(err.Error(), "non-https") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if hit {
		t.Fatal("the plain-http server was contacted")
	}
}
