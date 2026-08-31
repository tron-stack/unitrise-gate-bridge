// Package api is the signed HTTP client for the UnitRise gate-bridge API.
// The contract lives in docs/API_CONTRACT.md - change both together.
package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/config"
)

const ContractVersion = "v1"

type Client struct {
	cfg  *config.Config
	http *http.Client
}

func New(cfg *config.Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
}

// Credential is one gate code in the desired state.
type Credential struct {
	Code          string `json:"code"`
	UnitLabel     string `json:"unitLabel"`
	TenantName    string `json:"tenantName"`
	Status        string `json:"status"` // active | suspended
	TimeZoneGroup int    `json:"timeZoneGroup"`
}

// FormatSpec is the server-editable vendor file template (contract
// §settings.format). The agent interpolates it - format fixes are console
// edits that arrive on the next poll, never agent redeploys.
type FormatSpec struct {
	Preset        string `json:"preset"`
	Mode          string `json:"mode"` // full | delta
	Header        string `json:"header"`
	Line          string `json:"line"`
	SuspendedLine string `json:"suspendedLine"`
	AddedLine     string `json:"addedLine"`
	ChangedLine   string `json:"changedLine"`
	RemovedLine   string `json:"removedLine"`
	Footer        string `json:"footer"`
	LineEnding    string `json:"lineEnding"` // crlf | lf
	SortBy        string `json:"sortBy"`     // code | unit | name | none
}

type StateSettings struct {
	GeneratedFileName string      `json:"generatedFileName"`
	ConsumeCommand    string      `json:"consumeCommand"`
	DefaultTimeZone   int         `json:"defaultTimeZone"`
	PollSeconds       int         `json:"pollSeconds"`
	Format            *FormatSpec `json:"format"`
}

type State struct {
	StateHash string `json:"stateHash"`
	Facility  struct {
		Name    string `json:"name"`
		Address string `json:"address"`
		Phone   string `json:"phone"`
	} `json:"facility"`
	Provider    string        `json:"provider"`
	Settings    StateSettings `json:"settings"`
	// Delta renderers key "force full update" off this: a changed nonce means
	// re-emit the whole roster as adds, not an empty diff.
	ForceNonce  int          `json:"forceNonce"`
	Credentials []Credential `json:"credentials"`
}

// sign produces the headers per the contract:
//   hmacSHA256(secret, "METHOD\nPATH\nTIMESTAMP\nsha256hex(body)")
func (c *Client) sign(req *http.Request, path string, body []byte) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	bodyHash := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(c.cfg.AccessSecret))
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s", req.Method, path, ts, hex.EncodeToString(bodyHash[:]))
	req.Header.Set("X-UR-Key", c.cfg.AccessKey)
	req.Header.Set("X-UR-Facility", c.cfg.FacilityID)
	req.Header.Set("X-UR-Timestamp", ts)
	req.Header.Set("X-UR-Signature", hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("User-Agent", "unitrise-gate-bridge/"+AgentVersion)
}

// AgentVersion is stamped at build time via -ldflags.
var AgentVersion = "dev"

// GetState fetches the desired state. Returns (nil, "", nil) on 304.
func (c *Client) GetState(etag string) (*State, string, error) {
	path := "/api/gate-bridge/" + ContractVersion + "/state"
	req, err := http.NewRequest(http.MethodGet, c.cfg.APIEndpoint+path, nil)
	if err != nil {
		return nil, "", err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", `"`+etag+`"`)
	}
	c.sign(req, path, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, etag, nil
	case http.StatusOK:
		var st State
		if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&st); err != nil {
			return nil, "", fmt.Errorf("bad state payload: %w", err)
		}
		return &st, st.StateHash, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, "", fmt.Errorf("credentials rejected (%d) - re-pair from the UnitRise console", resp.StatusCode)
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, "", fmt.Errorf("state fetch failed: %d %s", resp.StatusCode, bytes.TrimSpace(b))
	}
}

func (c *Client) postJSON(path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.cfg.APIEndpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.sign(req, path, body)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s → %d %s", path, resp.StatusCode, bytes.TrimSpace(b))
	}
	return nil
}

type Heartbeat struct {
	AgentVersion  string    `json:"agentVersion"`
	OS            string    `json:"os"`
	LastApplyAt   time.Time `json:"lastApplyAt,omitempty"`
	LastApplyHash string    `json:"lastApplyHash,omitempty"`
	OK            bool      `json:"ok"`
	Detail        string    `json:"detail,omitempty"`
}

func (c *Client) SendHeartbeat(hb Heartbeat) error {
	return c.postJSON("/api/gate-bridge/"+ContractVersion+"/heartbeat", hb)
}

type Applied struct {
	StateHash     string `json:"stateHash"`
	WroteFile     string `json:"wroteFile"`
	ConsumeExit   int    `json:"consumeExit"`
	ConsumeOutput string `json:"consumeOutput,omitempty"`
}

func (c *Client) ReportApplied(a Applied) error {
	return c.postJSON("/api/gate-bridge/"+ContractVersion+"/applied", a)
}
