package main

// effort-router picks the reasoning effort for each new user turn with a Jev-style
// typed decision (SemIf, Laya or real Jev: all speak POST /v1/systemone with
// model jev-latest, so switching is only the host) and expresses it in the form
// each upstream can switch without invalidating the prompt cache: Claude's
// effort-only system message, or a Responses configuration_update item (GPT-6).
//
// Clients resend the full history on every request and never contain our
// inserts, so every past decision is re-inserted at the same position on every
// request. Decisions are persisted; losing them would edit history (cache miss,
// and on Fable 5.1 invalidated thinking blocks).

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"gopkg.in/yaml.v3"
)

type config struct {
	Provider      string   `yaml:"jev-provider"` // custom (jev-url), typesafe or opencode
	URL           string   `yaml:"jev-url"`
	Model         string   `yaml:"jev-model"`
	APIKey        string   `yaml:"jev-api-key"`
	TimeoutMS     int      `yaml:"timeout-ms"`
	Models        []string `yaml:"models"`                // model-name prefixes to route
	StateFile     string   `yaml:"state-file"`            // append-only decision log
	Shadow        bool     `yaml:"shadow"`                // classify and persist, never alter requests
	CodexBaseline string   `yaml:"codex-baseline-effort"` // fixed native Codex request baseline
	ProofFile     string   `yaml:"proof-file"`            // opt-in redacted normalizer evidence; empty by default
	Baseline      string   `yaml:"baseline-effort"`       // client default; any other per-turn value is a manual /effort and wins
}

// Vendor endpoints selectable by jev-provider; tests point them at local servers.
var providerURLs = map[string]string{
	"typesafe": "https://api.typesafe.ai/v1/systemone",
	"opencode": "https://opencode.ai/inference/systemone/v1/systemone",
}

const noDecision = "-" // classifier failed or turn predates the router: never inject

// Configuration swaps are atomic; each state owns its decision and write locks.
// Network calls never hold either lock, including recursive CPA classifier traffic.
type routerState struct {
	mu        sync.Mutex
	decisions map[string]string
	pending   map[string]chan struct{}
	warned    bool
}

var (
	cfgMu           sync.RWMutex
	cfg             config
	state           *routerState
	classifierSlots = make(chan struct{}, 8)
)

func configure(raw []byte) error {
	home, _ := os.UserHomeDir()
	c := config{
		URL: "http://127.0.0.1:8765/v1/systemone", Model: "jev-latest",
		TimeoutMS: 1500, Models: []string{"claude-fable-5-1", "claude-opus-5-5", "gpt-6"},
		StateFile: filepath.Join(home, ".cli-proxy-api", "effort-router.jsonl"), Baseline: "medium",
		CodexBaseline: "high",
	}
	var explicit struct {
		URL *string `yaml:"jev-url"`
	}
	if yaml.Unmarshal(raw, &c) != nil || yaml.Unmarshal(raw, &explicit) != nil {
		return errors.New("invalid plugin configuration")
	}
	switch c.Provider {
	case "", "custom":
		c.Provider = "custom"
	case "typesafe", "opencode":
		if explicit.URL != nil && *explicit.URL != "" && *explicit.URL != providerURLs[c.Provider] {
			return errors.New("jev-url is only for jev-provider custom")
		}
		if c.APIKey == "" {
			return errors.New("jev-provider " + c.Provider + " needs jev-api-key")
		}
		c.URL = providerURLs[c.Provider]
		if c.Provider == "opencode" && c.Model == "jev-latest" {
			c.Model = "jev-1.13" // Zen lists versioned Jev models only
		}
	default:
		return errors.New("invalid jev-provider")
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid classifier URL")
	}
	if c.TimeoutMS < 1 || c.TimeoutMS > 30000 {
		return errors.New("timeout-ms must be 1..30000")
	}
	if !filepath.IsAbs(c.StateFile) {
		return errors.New("state-file must be absolute (no tilde expansion)")
	}
	if c.ProofFile != "" && (!filepath.IsAbs(c.ProofFile) || c.ProofFile == c.StateFile) {
		return errors.New("proof-file must be a separate absolute path")
	}
	if _, ok := efforts[c.Baseline]; !ok && c.Baseline != "max" {
		return errors.New("invalid baseline-effort")
	}
	if _, ok := efforts[c.CodexBaseline]; !ok {
		return errors.New("invalid codex-baseline-effort")
	}
	for _, m := range c.Models {
		if m == "" || len(m) > 128 || strings.HasPrefix("ocg/", m) || strings.HasPrefix(m, "ocg/") {
			return errors.New("models must exclude ocg/ classifier traffic and empty prefixes")
		}
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if state == nil || cfg.StateFile != c.StateFile {
		state = &routerState{decisions: map[string]string{}, pending: map[string]chan struct{}{}}
		state.load(c.StateFile)
	}
	cfg = c
	return nil
}

func snapshot() (config, *routerState) {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg, state
}

type logLine struct {
	Key    string `json:"key"`
	Effort string `json:"effort"`
	At     string `json:"at,omitempty"`
	Model  string `json:"model,omitempty"`
	Ms     int64  `json:"ms,omitempty"`
	Shadow bool   `json:"shadow,omitempty"`
	Err    string `json:"err,omitempty"`
}

func (s *routerState) warn(err error) {
	if err != nil && !s.warned {
		s.warned = true
		log.Print("effort-router: state unavailable; new decisions will not be applied")
	}
}

// Caller holds s.mu (or owns a new state not yet published).
func (s *routerState) load(path string) {
	s.decisions = map[string]string{}
	f, err := openStateFile(path, false)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		s.warn(err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		s.warn(errors.New("unsafe state file"))
		return
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l logLine
		if json.Unmarshal(sc.Bytes(), &l) == nil && len(l.Key) == 64 {
			if _, ok := efforts[l.Effort]; ok || l.Effort == noDecision {
				k := l.Key
				if l.Shadow {
					k = "shadow:" + k
				}
				s.decisions[k] = l.Effort
			}
		}
	}
	s.warn(sc.Err())
}

// Persist before publishing. No prompt, response body, URL or secret is recorded.
func (s *routerState) record(c config, l logLine, cacheKey string) {
	f, err := openStateFile(c.StateFile, true)
	if err == nil {
		var info fs.FileInfo
		info, err = f.Stat()
		if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0) {
			err = errors.New("unsafe state file permissions")
		}
		if err == nil {
			b, _ := json.Marshal(l)
			_, err = f.Write(append(append([]byte{'\n'}, b...), '\n'))
			if err == nil {
				err = f.Sync()
			}
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	}
	s.warn(err)
	if err != nil {
		s.decisions[cacheKey] = noDecision
	} else {
		s.decisions[cacheKey] = l.Effort
	}
}

// userText returns the typed text of a user message, or ok=false for tool
// results and other non-typed turns. Claude Code's <system-reminder> blocks are
// dropped, and cache_control markers never reach the key.
func userText(msg gjson.Result) (string, bool) {
	if msg.Get("role").String() != "user" {
		return "", false
	}
	content := msg.Get("content")
	if content.Type == gjson.String {
		t := content.String()
		return t, t != "" && !strings.HasPrefix(t, "<system-reminder>")
	}
	var parts []string
	typed := true
	content.ForEach(func(_, block gjson.Result) bool {
		switch block.Get("type").String() {
		case "tool_result":
			typed = false
			return false
		case "text", "input_text": // Claude, Responses
			if t := block.Get("text").String(); !strings.HasPrefix(t, "<system-reminder>") {
				parts = append(parts, t)
			}
		}
		return true
	})
	text := strings.Join(parts, "\n")
	return text, typed && text != ""
}

func isEffortDirective(msg gjson.Result) bool {
	return msg.Get("role").String() == "system" && msg.Get("output_config.effort").Exists()
}

// assistantTail is "" for the zero Result (no assistant message yet).
func assistantTail(msg gjson.Result, n int) string {
	var parts []string
	msg.Get("content").ForEach(func(_, block gjson.Result) bool {
		if t := block.Get("type").String(); t == "text" || t == "output_text" {
			parts = append(parts, block.Get("text").String())
		}
		return true
	})
	if msg.Get("content").Type == gjson.String {
		parts = append(parts, msg.Get("content").String())
	}
	r := []rune(strings.Join(parts, "\n"))
	return string(r[max(0, len(r)-n):])
}

func head(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func key(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Chain typed user turns so key computation stays linear in history size.
func historyKey(previous, text string) string {
	material, _ := json.Marshal([]string{previous, text})
	return key(string(material))
}

func modelPrefix(c config, model string) string {
	for _, prefix := range c.Models {
		if strings.HasPrefix(model, prefix) {
			return prefix
		}
	}
	return ""
}

func routed(c config, model string) bool {
	return slices.ContainsFunc(c.Models, func(prefix string) bool { return strings.HasPrefix(model, prefix) })
}

func route(model string, body []byte) []byte {
	c, s := snapshot()
	if s == nil || !routed(c, model) || !gjson.ValidBytes(body) {
		return body
	}
	if e := gjson.GetBytes(body, "output_config.effort"); e.Exists() && e.String() != c.Baseline {
		return body
	}
	msgs := gjson.GetBytes(body, "messages").Array()
	finalAssistant := -1
	for i, msg := range msgs {
		if msg.Get("role").String() == "assistant" {
			finalAssistant = i
		}
	}
	inserts := map[int]string{} // message index -> directive placed before it
	history := ""
	var last gjson.Result // latest assistant message so far
	for i, msg := range msgs {
		if msg.Get("role").String() == "assistant" {
			last = msg
		}
		text, ok := userText(msg)
		if !ok {
			continue
		}
		history = historyKey(history, text)
		if i > 0 && isEffortDirective(msgs[i-1]) && msgs[i-1].Get("content").IsArray() && len(msgs[i-1].Get("content").Array()) == 0 {
			continue // Already carries an effort-only directive from a prior pass.
		}
		// Claude Code 2.1.280 attaches the turn's effort to the system message(s)
		// right after the user message. A non-baseline value is the user's /effort.
		directive := -1
		for j := i + 1; j < len(msgs) && msgs[j].Get("role").String() == "system"; j++ {
			if isEffortDirective(msgs[j]) {
				directive = j
			}
		}
		if directive >= 0 && msgs[directive].Get("output_config.effort").String() != c.Baseline {
			continue
		}
		effort := decide(c, s, model, text, assistantTail(last, 600), history, i > finalAssistant)
		if c.Shadow || effort == "" {
			continue
		}
		at := i
		if directive >= 0 {
			if effort == c.Baseline {
				continue
			}
			at = directive + 1 // after Claude Code's own value, so ours applies
		}
		inserts[at] = fmt.Sprintf(`{"role":"system","content":[],"output_config":{"effort":%q}}`, effort)
	}
	if len(inserts) == 0 {
		return body
	}
	out := make([]string, 0, len(msgs)+len(inserts))
	for i, msg := range msgs {
		if d, ok := inserts[i]; ok {
			out = append(out, d)
		}
		out = append(out, msg.Raw)
	}
	if d, ok := inserts[len(msgs)]; ok {
		out = append(out, d)
	}
	patched, err := sjson.SetRawBytes(body, "messages", []byte("["+strings.Join(out, ",")+"]"))
	if err != nil {
		return body
	}
	return patched
}

// routeCodex is route for OpenAI Responses bodies (GPT-6 via the Codex
// upstream). Top-level reasoning.effort sits in the hidden system instructions,
// so changing it rewrites the cached prefix; a configuration_update input item
// switches effort from that point on and keeps the prefix. The proxy's Claude
// translator drops Claude Code's per-turn directive, so the only user override
// visible here is a non-baseline top-level effort (/effort), which wins.
func routeCodex(model string, body []byte) []byte {
	c, s := snapshot()
	if s == nil || !routed(c, model) || !gjson.ValidBytes(body) {
		return body
	}
	if e := gjson.GetBytes(body, "reasoning.effort"); e.Exists() && e.String() != c.Baseline {
		return body
	}
	items := gjson.GetBytes(body, "input").Array()
	finalAssistant := -1
	for i, it := range items {
		if t := it.Get("type").String(); it.Get("role").String() == "assistant" || t == "function_call" || t == "reasoning" {
			finalAssistant = i
		}
	}
	out := make([]string, 0, len(items)*2)
	history := ""
	var last gjson.Result // latest assistant message so far
	for i, it := range items {
		if it.Get("role").String() == "assistant" {
			last = it
		}
		// The API rejects adjacent updates, and normalize may see its own output.
		if text, ok := userText(it); ok {
			history = historyKey(history, text)
			if i > 0 && items[i-1].Get("type").String() == "configuration_update" {
				out = append(out, it.Raw)
				continue
			}
			if effort := decide(c, s, model, text, assistantTail(last, 600), history, i > finalAssistant); effort != "" && !c.Shadow {
				// Every decided turn carries its own item: the latest one wins, so a
				// medium turn after an xhigh one must say so.
				out = append(out, fmt.Sprintf(`{"type":"configuration_update","reasoning":{"effort":%q}}`, effort))
			}
		}
		out = append(out, it.Raw)
	}
	if len(out) == len(items) {
		return body
	}
	patched, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(out, ",")+"]"))
	if err != nil {
		return body
	}
	return patched
}

// decide returns the turn's effort, or "" when there is none to inject. Only the
// first request of a turn (no reply yet) may classify: deciding later would edit
// history the model has already answered from.
func decide(c config, s *routerState, model, text, prev string, history string, mayClassify bool) string {
	// Full digest, with model and previous context; separate shadow namespace so
	// switching to active cannot insert past shadow decisions into an existing history.
	material, _ := json.Marshal(struct {
		Version, Model, Previous string
		History                  string
	}{"v2", model, prev, history})
	k := key(string(material))
	cacheKey := k
	if c.Shadow {
		cacheKey = "shadow:" + k
	}
	s.mu.Lock()
	if wait := s.pending[cacheKey]; wait != nil {
		s.mu.Unlock()
		<-wait
		s.mu.Lock()
	}
	if effort, known := s.decisions[cacheKey]; known {
		s.mu.Unlock()
		if effort == noDecision {
			return ""
		}
		return effort
	}
	if s.warned {
		s.mu.Unlock()
		return "" // Preserve known history, but stop new decisions after a state error.
	}
	if !mayClassify {
		// Remember pre-router and shadow histories as never-inject, including restarts.
		s.record(c, logLine{Key: k, Effort: noDecision, Shadow: c.Shadow}, cacheKey)
		s.mu.Unlock()
		return ""
	}
	done := make(chan struct{})
	s.pending[cacheKey] = done
	s.mu.Unlock()
	start := time.Now()
	effort, err := safeClassify(c, prev, head(text, 1200), k)
	l := logLine{Key: k, Effort: effort, At: start.UTC().Format(time.RFC3339), Model: modelPrefix(c, model), Ms: time.Since(start).Milliseconds(), Shadow: c.Shadow}
	if err != nil {
		l.Effort, l.Err = noDecision, err.Error()
	}
	s.mu.Lock()
	s.record(c, l, cacheKey)
	result := s.decisions[cacheKey]
	delete(s.pending, cacheKey)
	close(done)
	s.mu.Unlock()
	if result == noDecision {
		return ""
	}
	return result
}

func safeClassify(c config, previous, message, turnID string) (effort string, err error) {
	defer func() {
		if recover() != nil {
			effort, err = "", errors.New("classifier_panic")
		}
	}()
	select {
	case classifierSlots <- struct{}{}:
		defer func() { <-classifierSlots }()
	default:
		return "", errors.New("classifier_busy")
	}
	return classify(c, previous, message, turnID)
}

var efforts = map[string]string{
	"low":    "Trivial: a quick question, confirmation, chit-chat, running one command, or a tiny obvious edit.",
	"medium": "Routine: a clear, well-scoped coding task or explanation with obvious steps.",
	"high":   "Substantial: multi-step engineering, debugging, or design that needs careful reasoning.",
	"xhigh":  "Hard: open-ended architecture, subtle debugging, research, or a long autonomous build.",
}

func classify(c config, previous, message, turnID string) (string, error) {
	state := map[string]any{"assistant_previous_reply_tail": previous, "user_message": message}
	if c.Provider == "custom" {
		// Local metadata for a loopback adapter; vendor endpoints never see it.
		state["turn_id"], state["router_shadow"] = turnID, c.Shadow
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model": c.Model,
		"state": state,
		"questions": map[string]any{"effort": map[string]any{
			"type":         "choice",
			"instructions": "How much reasoning effort should a coding agent spend on this user message, given the assistant's previous reply?",
			"criteria":     efforts,
		}},
	})
	req, err := http.NewRequest(http.MethodPost, c.URL, bytes.NewReader(reqBody))
	if err != nil {
		return "", errors.New("classifier_request")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := (&http.Client{Timeout: time.Duration(c.TimeoutMS) * time.Millisecond, Transport: classifierTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", errors.New("classifier_timeout")
		}
		return "", errors.New("classifier_transport")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024+1))
	if err != nil {
		return "", errors.New("classifier_read")
	}
	if len(data) > 16*1024 {
		return "", errors.New("classifier_oversize")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("classifier_http_%d", resp.StatusCode)
	}
	if !gjson.ValidBytes(data) {
		return "", errors.New("classifier_json")
	}
	// Laya/Jev return {answers}, SemIf wraps it as {code,data:{answers}}.
	choice := gjson.GetBytes(data, "answers.effort.choice")
	if !choice.Exists() {
		choice = gjson.GetBytes(data, "data.answers.effort.choice")
	}
	if _, ok := efforts[choice.String()]; !ok {
		return "", errors.New("classifier_answer")
	}
	return choice.String(), nil
}

// A classifier request never consults HTTP_PROXY/HTTPS_PROXY. Redirects are rejected.
var classifierTransport = func() *http.Transport { t := http.DefaultTransport.(*http.Transport).Clone(); t.Proxy = nil; return t }()
