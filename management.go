package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// The Management Center lists the page under its plugin menu. Resource routes are not
// authenticated, so the page and script are static; the data comes only from the
// authenticated Management API route, which the page calls with the operator's key.
const decisionsPath = "/v0/management/effort-router/decisions"

//go:embed web/index.html
var pageHTML []byte

//go:embed web/app.js
var pageJS []byte

const stateTailBytes = 4 << 20 // summaries read at most the last 4 MiB of the state file

var (
	errorCategory = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	knownEfforts  = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true}
)

func managementRegistration() map[string]any {
	return map[string]any{
		"routes": []map[string]string{{"Method": http.MethodGet, "Path": decisionsPath}},
		"resources": []map[string]string{
			{"Path": "/page", "Menu": "Effort routing", "Description": "Effort-router decisions by model and effort, with recent turns. Needs the management key."},
			{"Path": "/app.js"},
		},
	}
}

func handleManagement(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	static := func(contentType string, body []byte) pluginapi.ManagementResponse {
		return pluginapi.ManagementResponse{Headers: http.Header{
			"Content-Type":            {contentType},
			"Cache-Control":           {"no-store"},
			"X-Content-Type-Options":  {"nosniff"},
			"Referrer-Policy":         {"no-referrer"},
			"Content-Security-Policy": {"default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'"},
		}, Body: body}
	}
	switch {
	case req.Method == http.MethodGet && req.Path == decisionsPath:
		c, _ := snapshot()
		hours, err := strconv.Atoi(req.Query.Get("hours"))
		if err != nil || hours < 1 || hours > 168 {
			hours = 24
		}
		body, _ := json.Marshal(summarize(c, readStateTail(c.StateFile), time.Now().UTC(), hours))
		return pluginapi.ManagementResponse{Headers: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}, Body: body}
	case strings.HasPrefix(req.Path, "/v0/resource/") && strings.HasSuffix(req.Path, "/page"):
		return static("text/html; charset=utf-8", pageHTML)
	case strings.HasPrefix(req.Path, "/v0/resource/") && strings.HasSuffix(req.Path, "/app.js"):
		return static("text/javascript; charset=utf-8", pageJS)
	}
	return pluginapi.ManagementResponse{StatusCode: http.StatusNotFound, Headers: http.Header{"Content-Type": {"text/plain"}}, Body: []byte("not found")}
}

// readStateTail returns whole lines from the end of the state file, or nil.
func readStateTail(path string) []byte {
	f, err := openStateFile(path, false)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	start := info.Size() - stateTailBytes
	if start < 0 {
		start = 0
	}
	data, err := io.ReadAll(io.NewSectionReader(f, start, info.Size()-start))
	if err != nil {
		return nil
	}
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	return data
}

type decisionRow struct {
	At     string `json:"at"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Ms     int64  `json:"ms"`
	Shadow bool   `json:"shadow"`
	Err    string `json:"err,omitempty"`
}

type hourRow struct {
	Hour    string         `json:"hour"`
	Efforts map[string]int `json:"efforts"`
	Errors  int            `json:"errors"`
}

type summary struct {
	Mode          string                    `json:"mode"`
	Provider      string                    `json:"provider"`
	Baseline      string                    `json:"baseline_effort"`
	CodexBaseline string                    `json:"codex_baseline_effort"`
	Models        []string                  `json:"models"`
	Since         string                    `json:"since"`
	Hours         int                       `json:"hours"`
	Decisions     int                       `json:"decisions"`
	ByModel       map[string]map[string]int `json:"by_model"` // model -> effort -> count, live decisions only
	ShadowByModel map[string]map[string]int `json:"shadow_by_model"`
	Errors        map[string]int            `json:"errors"`
	LatencyMs     map[string]int64          `json:"latency_ms"`
	Hourly        []hourRow                 `json:"hourly"`
	Recent        []decisionRow             `json:"recent"`
}

// summarize aggregates classified turns since now-hours. Every value is validated
// before it is returned; the state file never holds prompts, and keys are dropped.
func summarize(c config, data []byte, now time.Time, hours int) summary {
	mode := "active"
	if c.Shadow {
		mode = "shadow"
	}
	since := now.Add(-time.Duration(hours) * time.Hour)
	s := summary{Mode: mode, Provider: c.Provider, Baseline: c.Baseline, CodexBaseline: c.CodexBaseline,
		Models: c.Models, Since: since.Format(time.RFC3339), Hours: hours,
		ByModel: map[string]map[string]int{}, ShadowByModel: map[string]map[string]int{},
		Errors: map[string]int{}, LatencyMs: map[string]int64{}, Hourly: []hourRow{}, Recent: []decisionRow{}}
	hourly := map[string]*hourRow{}
	var ms []int64
	var rows []decisionRow
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var l logLine
		if json.Unmarshal(line, &l) != nil || l.At == "" {
			continue // pre-router and replay markers carry no time and no decision
		}
		at, err := time.Parse(time.RFC3339, l.At)
		if err != nil || at.Before(since) || at.After(now.Add(time.Minute)) {
			continue
		}
		r := decisionRow{At: at.UTC().Format(time.RFC3339), Model: "other", Effort: "none", Ms: l.Ms, Shadow: l.Shadow}
		if routed(c, l.Model) && len(l.Model) <= 128 {
			r.Model = l.Model
		}
		if knownEfforts[l.Effort] {
			r.Effort = l.Effort
		}
		if l.Err != "" {
			r.Err = "other"
			if errorCategory.MatchString(l.Err) {
				r.Err = l.Err
			}
			s.Errors[r.Err]++
		}
		if r.Ms < 0 || r.Ms > 600000 {
			r.Ms = 0
		}
		rows = append(rows, r)
		if r.Err == "" {
			table := s.ByModel
			if r.Shadow {
				table = s.ShadowByModel
			}
			if table[r.Model] == nil {
				table[r.Model] = map[string]int{}
			}
			table[r.Model][r.Effort]++
			ms = append(ms, r.Ms)
		}
		hk := at.UTC().Truncate(time.Hour).Format(time.RFC3339)
		h := hourly[hk]
		if h == nil {
			h = &hourRow{Hour: hk, Efforts: map[string]int{}}
			hourly[hk] = h
		}
		if r.Err != "" {
			h.Errors++
		} else {
			h.Efforts[r.Effort]++
		}
	}
	s.Decisions = len(rows)
	for _, h := range hourly {
		s.Hourly = append(s.Hourly, *h)
	}
	sort.Slice(s.Hourly, func(i, j int) bool { return s.Hourly[i].Hour < s.Hourly[j].Hour })
	if len(ms) > 0 {
		sort.Slice(ms, func(i, j int) bool { return ms[i] < ms[j] })
		pick := func(q float64) int64 { return ms[min(len(ms)-1, int(q*float64(len(ms))))] }
		s.LatencyMs = map[string]int64{"p50": pick(0.5), "p95": pick(0.95), "max": ms[len(ms)-1]}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].At < rows[j].At })
	for i := len(rows) - 1; i >= 0 && len(s.Recent) < 50; i-- {
		s.Recent = append(s.Recent, rows[i])
	}
	return s
}
