package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// stateWith configures the router on a fresh state file holding lines.
func stateWith(t *testing.T, lines ...string) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := configure([]byte("state-file: " + path + "\nbaseline-effort: high\nmodels: [claude-opus-5-5, gpt-6]\n")); err != nil {
		t.Fatal(err)
	}
}

func decisions(t *testing.T, query string) (summary, string) {
	q, _ := url.ParseQuery(query)
	resp := handleManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: decisionsPath, Query: q})
	if resp.StatusCode != 0 || resp.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("decisions: %d %v", resp.StatusCode, resp.Headers)
	}
	var s summary
	if err := json.Unmarshal(resp.Body, &s); err != nil {
		t.Fatal(err)
	}
	return s, string(resp.Body)
}

func TestManagementSummaryValidatesEveryField(t *testing.T) {
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	line := func(v map[string]any) string { b, _ := json.Marshal(v); return string(b) }
	stateWith(t,
		line(map[string]any{"key": "SECRETKEYHASH", "effort": "low", "at": at(time.Minute), "model": "claude-opus-5-5", "ms": 300}),
		line(map[string]any{"key": "k2", "effort": "high", "at": at(2 * time.Minute), "model": "claude-opus-5-5", "ms": 400}),
		line(map[string]any{"key": "k3", "effort": "medium", "at": at(3 * time.Minute), "model": "gpt-6", "ms": 200}),
		line(map[string]any{"key": "k4", "effort": "low", "at": at(4 * time.Minute), "model": "gpt-6", "ms": 250, "shadow": true}),
		line(map[string]any{"key": "k5", "effort": "-", "at": at(5 * time.Minute), "model": "gpt-6", "ms": 1500, "err": "classifier_timeout"}),
		line(map[string]any{"key": "k6", "effort": "<b>x</b>", "at": at(6 * time.Minute), "model": "evil<img src=x>", "ms": -5, "err": "<script>alert(1)</script>"}),
		line(map[string]any{"key": "k7", "effort": "low", "at": at(30 * time.Hour), "model": "gpt-6"}), // outside 24 h
		line(map[string]any{"key": "k8", "effort": "-"}), // replay marker
		`{not json`,
	)
	s, body := decisions(t, "hours=24")
	for _, leak := range []string{"SECRETKEYHASH", "key", "evil", "<", "script", "alert"} {
		if strings.Contains(body, leak) {
			t.Fatalf("summary leaks %q: %s", leak, body)
		}
	}
	if s.Mode != "active" || s.Baseline != "high" || s.Decisions != 6 {
		t.Fatalf("header: %+v", s)
	}
	if s.ByModel["claude-opus-5-5"]["low"] != 1 || s.ByModel["claude-opus-5-5"]["high"] != 1 || s.ByModel["gpt-6"]["medium"] != 1 {
		t.Fatalf("by_model: %v", s.ByModel)
	}
	if s.ShadowByModel["gpt-6"]["low"] != 1 || s.ByModel["gpt-6"]["low"] != 0 {
		t.Fatalf("shadow decisions must be separate: %v / %v", s.ShadowByModel, s.ByModel)
	}
	if s.Errors["classifier_timeout"] != 1 || s.Errors["other"] != 1 {
		t.Fatalf("errors: %v", s.Errors)
	}
	if s.LatencyMs["max"] != 400 || len(s.Recent) != 6 || s.Recent[0].Effort != "low" || s.Recent[0].Model != "claude-opus-5-5" {
		t.Fatalf("latency %v recent %+v", s.LatencyMs, s.Recent)
	}
	if bad := s.Recent[5]; bad.Model != "other" || bad.Effort != "none" || bad.Err != "other" || bad.Ms != 0 {
		t.Fatalf("invalid row not neutralised: %+v", bad)
	}
	if s7, _ := decisions(t, "hours=48"); s7.Decisions != 7 {
		t.Fatalf("48 h window: %d", s7.Decisions)
	}
	if sBad, _ := decisions(t, "hours=9999"); sBad.Hours != 24 {
		t.Fatalf("out-of-range hours must fall back to 24: %d", sBad.Hours)
	}
}

func TestManagementSummaryWithoutStateFile(t *testing.T) {
	if err := configure([]byte("state-file: " + filepath.Join(t.TempDir(), "missing.jsonl") + "\n")); err != nil {
		t.Fatal(err)
	}
	if s, _ := decisions(t, ""); s.Decisions != 0 || s.Recent == nil || s.Hourly == nil {
		t.Fatalf("empty summary: %+v", s)
	}
}

func TestManagementResourcesAreStaticAndGuarded(t *testing.T) {
	stateWith(t, `{"key":"k","effort":"low","at":"`+time.Now().UTC().Format(time.RFC3339)+`","model":"gpt-6"}`)
	base := "/v0/resource/plugins/effort-router"
	page := handleManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: base + "/page"})
	js := handleManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: base + "/app.js"})
	if !bytes.Equal(page.Body, pageHTML) || !bytes.Equal(js.Body, pageJS) || !strings.Contains(page.Headers.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("static resources: %v / %v", page.Headers, js.Headers)
	}
	if strings.Contains(string(pageJS), "innerHTML") || strings.Contains(string(pageHTML), "<script>") {
		t.Fatal("page must render text only and load no inline script")
	}
	for _, req := range []pluginapi.ManagementRequest{
		{Method: http.MethodGet, Path: base + "/decisions"},                 // data only on the authenticated route
		{Method: http.MethodGet, Path: base + decisionsPath},                // nested lookalike
		{Method: http.MethodPost, Path: decisionsPath},                      // GET only
		{Method: http.MethodGet, Path: "/v0/management/effort-router/page"}, // page is not a management route
	} {
		if resp := handleManagement(req); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s: %d", req.Method, req.Path, resp.StatusCode)
		}
	}
}

func TestManagementABIRoundTrip(t *testing.T) {
	stateWith(t)
	raw, err := handleMethod(pluginabi.MethodManagementRegister, []byte(`{"BasePath":"/v0/management","ResourceBasePath":"/v0/resource/plugins/effort-router"}`))
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		Result struct {
			Routes    []pluginapi.ManagementRoute `json:"routes"`
			Resources []pluginapi.ResourceRoute   `json:"resources"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &reg) != nil || len(reg.Result.Routes) != 1 || reg.Result.Routes[0].Path != decisionsPath || reg.Result.Resources[0].Menu != "Effort routing" {
		t.Fatalf("registration: %s", raw)
	}
	req, _ := json.Marshal(struct {
		pluginapi.ManagementRequest
		HostCallbackID string `json:"host_callback_id"`
	}{pluginapi.ManagementRequest{Method: http.MethodGet, Path: decisionsPath}, "cb-1"})
	raw, err = handleMethod(pluginabi.MethodManagementHandle, req)
	var out struct {
		OK     bool                         `json:"ok"`
		Result pluginapi.ManagementResponse `json:"result"`
	}
	if err != nil || json.Unmarshal(raw, &out) != nil || !out.OK || !bytes.Contains(out.Result.Body, []byte(`"mode":"active"`)) {
		t.Fatalf("handle: %v %s", err, raw)
	}
}

func TestReadStateTailDropsPartialFirstLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	last := `{"key":"k","effort":"low","at":"2026-10-17T00:00:00Z","model":"gpt-6"}`
	data := append(bytes.Repeat([]byte("x"), stateTailBytes), []byte("\n"+last+"\n")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readStateTail(path); string(got) != last+"\n" {
		t.Fatalf("tail: %d bytes", len(got))
	}
}
