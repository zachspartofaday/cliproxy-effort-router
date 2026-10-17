package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

// jev starts a classifier answering choice[user_message] ("" = HTTP 500) and
// points the router at it with a fresh state file. choice["wrap"] switches to
// SemIf's {data:{answers}} shape; choice["auth"] is the required Authorization.
// It returns the call count.
func jev(t *testing.T, choice map[string]string, extraConfig string) *int {
	calls := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		var req struct {
			State struct {
				UserMessage string `json:"user_message"`
			} `json:"state"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		c := choice[req.State.UserMessage]
		if c == "" || r.Header.Get("Authorization") != choice["auth"] {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		ans := map[string]any{"answers": map[string]any{"effort": map[string]any{"choice": c}}}
		if choice["wrap"] != "" {
			ans = map[string]any{"code": 0, "data": ans}
		}
		json.NewEncoder(w).Encode(ans)
	}))
	t.Cleanup(srv.Close)
	if err := configure([]byte("jev-url: " + srv.URL + "\nstate-file: " + filepath.Join(t.TempDir(), "s.jsonl") + "\n" + extraConfig)); err != nil {
		t.Fatal(err)
	}
	return calls
}

// effortsAt lists each message's effort ("" for ordinary messages).
func effortsAt(body []byte, path string) []string {
	var out []string
	for _, m := range gjson.GetBytes(body, path).Array() {
		out = append(out, m.Get("output_config.effort").String()+m.Get("reasoning.effort").String())
	}
	return out
}

func TestRouteKeepsDecisionsStableAcrossRequests(t *testing.T) {
	calls := jev(t, map[string]string{"hi": "low"}, "")
	sys := `{"type":"text","text":"<system-reminder>ctx</system-reminder>"}`
	// SessionStart hook output trails the first user message as role=system.
	turn1 := `{"messages":[{"role":"user","content":[` + sys + `,{"type":"text","text":"hi"}]},{"role":"system","content":[{"type":"text","text":"hook"}]}]}`
	out1 := route("claude-fable-5-1", []byte(turn1))
	if e := effortsAt(out1, "messages"); len(e) != 3 || e[0] != "low" || *calls != 1 {
		t.Fatalf("first request: %s (calls=%d)", out1, *calls)
	}

	// Same turn continues with a tool loop, cache_control moved: no reclassify, same insert.
	loop := `{"messages":[{"role":"user","content":[` + sys + `,{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"x","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}`
	out2 := route("claude-fable-5-1", []byte(loop))
	if e := effortsAt(out2, "messages"); len(e) != 4 || e[0] != "low" || *calls != 1 {
		t.Fatalf("tool loop: %s (calls=%d)", out2, *calls)
	}

	// Restart: decisions come back from the state file.
	state.load(cfg.StateFile)
	if string(route("claude-fable-5-1", []byte(loop))) != string(out2) || *calls != 1 {
		t.Fatal("decision not restored from state file")
	}
}

func TestRecordSurvivesTornLastLine(t *testing.T) {
	jev(t, nil, "")
	os.WriteFile(cfg.StateFile, []byte(`{"key":"`+key("a")+`","effort":"high"}`+"\n"+`{"key":"b","eff`), 0o600)
	state.record(cfg, logLine{Key: key("c"), Effort: "low"}, key("c"))
	state.load(cfg.StateFile)
	if state.decisions[key("a")] != "high" || state.decisions[key("c")] != "low" {
		t.Fatalf("records around a torn line must load: %v", state.decisions)
	}
}

func TestRouteSkipsOverridesFailuresAndOtherModels(t *testing.T) {
	choice := map[string]string{}
	jev(t, choice, "")
	body := `{"messages":[{"role":"user","content":"do the thing"}]}`
	if out := route("claude-fable-5-1", []byte(body)); string(out) != body {
		t.Fatalf("failed classification must not inject: %s", out)
	}
	choice["do the thing"] = "high"
	if out := route("claude-fable-5-1", []byte(body)); string(out) != body {
		t.Fatalf("failed turn must stay uninjected later too: %s", out)
	}
	choice["go all out"] = "max"
	if out := route("claude-fable-5-1", []byte(`{"messages":[{"role":"user","content":"go all out"}]}`)); gjson.GetBytes(out, "messages.#").Int() != 1 {
		t.Fatalf("the classifier may only pick low..xhigh: %s", out)
	}
	choice["new turn"] = "low"
	override := `{"messages":[{"role":"user","content":"new turn"},{"role":"system","content":[{"type":"text","text":"hook"}],"output_config":{"effort":"xhigh"}}]}`
	if out := route("claude-fable-5-1", []byte(override)); string(out) != override {
		t.Fatalf("user /effort must win: %s", out)
	}
	other := `{"messages":[{"role":"user","content":"x"}]}`
	if out := route("claude-sonnet-5", []byte(other)); string(out) != other {
		t.Fatalf("unrouted model touched: %s", out)
	}
}

func TestRouteFollowsClaudeCodePerTurnEffort(t *testing.T) {
	jev(t, map[string]string{"design it": "xhigh", "ok": "medium"}, "")
	cc := func(msg string) string {
		return `{"messages":[{"role":"user","content":"` + msg + `"},{"role":"system","content":[{"type":"text","text":"hook"}],"output_config":{"effort":"medium"}}]}`
	}
	if e := effortsAt(route("claude-opus-5-5", []byte(cc("design it"))), "messages"); len(e) != 3 || e[1] != "medium" || e[2] != "xhigh" {
		t.Fatalf("router effort must follow Claude Code's untouched one: %v", e)
	}
	if out := route("claude-opus-5-5", []byte(cc("ok"))); string(out) != cc("ok") {
		t.Fatalf("baseline decision on a baseline directive needs no insert: %s", out)
	}
}

func TestClassifierAcceptsSemIfShapeAndBearerKey(t *testing.T) {
	jev(t, map[string]string{"hi": "high", "wrap": "1", "auth": "Bearer k"}, "jev-api-key: k")
	if e := effortsAt(route("claude-fable-5-1", []byte(`{"messages":[{"role":"user","content":"hi"}]}`)), "messages"); len(e) != 2 || e[0] != "high" {
		t.Fatalf("wrapped answer with bearer key: %v", e)
	}
}

func TestRouteCodexUsesConfigurationUpdates(t *testing.T) {
	calls := jev(t, map[string]string{"hi": "low", "design": "xhigh", "thanks": "medium"}, "")
	user := func(s string) string {
		return `{"type":"message","role":"user","content":[{"type":"input_text","text":"` + s + `"}]}`
	}
	reply := `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"yo"}]}`
	reminder := user(`<system-reminder>\nhook\n</system-reminder>`)
	req := func(items ...string) string {
		s := `{"reasoning":{"effort":"medium"},"input":[`
		for i, it := range items {
			if i > 0 {
				s += ","
			}
			s += it
		}
		return s + `]}`
	}
	out := routeCodex("gpt-6-sol", []byte(req(user("hi"), reminder)))
	if e := effortsAt(out, "input"); len(e) != 3 || e[0] != "low" || gjson.GetBytes(out, "reasoning.effort").String() != "medium" {
		t.Fatalf("turn 1: %s", out)
	}
	if again := routeCodex("gpt-6-sol", out); string(again) != string(out) {
		t.Fatalf("second normalize pass must be a no-op: %s", again)
	}
	routeCodex("gpt-6-sol", []byte(req(user("hi"), reminder, reply, user("design"))))
	out = routeCodex("gpt-6-sol", []byte(req(user("hi"), reminder, reply, user("design"), reply, user("thanks"))))
	if e := effortsAt(out, "input"); len(e) != 9 || e[0] != "low" || e[4] != "xhigh" || e[7] != "medium" || *calls != 3 {
		t.Fatalf("each turn needs its own update, baseline too (calls=%d): %v", *calls, e)
	}
	override := `{"reasoning":{"effort":"max"},"input":[` + user("hi") + `]}`
	if out := routeCodex("gpt-6-sol", []byte(override)); string(out) != override {
		t.Fatalf("user /effort must win: %s", out)
	}
	if plain := req(user("hi")); string(routeCodex("gpt-5.6-luna", []byte(plain))) != plain {
		t.Fatal("gpt-5 must stay untouched")
	}
}

func TestNormalizeRoutesNativeAndClaudeClients(t *testing.T) {
	jev(t, map[string]string{"hi": "high"}, "codex-baseline-effort: medium")
	body := `{"reasoning":{"effort":"medium"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	normalize := func(from string) string {
		in, _ := json.Marshal(pluginapi.RequestTransformRequest{FromFormat: from, ToFormat: "codex", Model: "gpt-6-sol", Body: []byte(body)})
		raw, _ := handleMethod(pluginabi.MethodRequestNormalize, in)
		var res pluginapi.PayloadResponse
		json.Unmarshal([]byte(gjson.GetBytes(raw, "result").Raw), &res)
		return string(res.Body)
	}
	if normalize("openai-response") == body {
		t.Fatal("native Responses sampling must be routed")
	}
	if normalize("claude") == body {
		t.Fatal("Claude Code traffic must be routed")
	}
}

// providerJev serves every vendor preset from one local server and records each
// request's model, state keys and Authorization header.
func providerJev(t *testing.T) *[]map[string]any {
	seen := new([]map[string]any)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		req["auth"] = r.Header.Get("Authorization")
		*seen = append(*seen, req)
		json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"effort": map[string]any{"choice": "low"}}})
	}))
	t.Cleanup(srv.Close)
	saved := providerURLs
	providerURLs = map[string]string{"typesafe": srv.URL + "/ts", "opencode": srv.URL + "/oc"}
	t.Cleanup(func() { providerURLs = saved })
	return seen
}

func TestProviderPresetsCallVendorsWithoutLocalMetadata(t *testing.T) {
	seen := providerJev(t)
	for _, tc := range []struct{ provider, extra, model string }{
		{"typesafe", "", "jev-latest"},
		{"opencode", "", "jev-1.13"},
		{"opencode", "jev-model: jev-1.13-free\n", "jev-1.13-free"},
	} {
		*seen = nil
		cfgText := "jev-provider: " + tc.provider + "\njev-api-key: k\n" + tc.extra + "state-file: " + filepath.Join(t.TempDir(), "s.jsonl") + "\n"
		if err := configure([]byte(cfgText)); err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		route("claude-fable-5-1", []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
		if len(*seen) != 1 {
			t.Fatalf("%s: %d classifier calls", tc.provider, len(*seen))
		}
		req := (*seen)[0]
		state, _ := req["state"].(map[string]any)
		if req["model"] != tc.model || req["auth"] != "Bearer k" || len(state) != 2 || state["user_message"] != "hi" {
			t.Fatalf("%s: model %v auth %v state keys %v", tc.provider, req["model"], req["auth"], state)
		}
	}
}

func TestProviderCustomKeepsAdapterMetadata(t *testing.T) {
	jev(t, map[string]string{"hi": "low"}, "jev-provider: custom\n")
	if cfg.URL == providerURLs["typesafe"] || cfg.Provider != "custom" {
		t.Fatalf("custom must keep jev-url: %+v", cfg)
	}
	seen := providerJev(t)
	*seen = nil
	srvURL := providerURLs["typesafe"]
	if err := configure([]byte("jev-url: " + srvURL + "\nstate-file: " + filepath.Join(t.TempDir(), "s.jsonl") + "\n")); err != nil {
		t.Fatal(err)
	}
	route("claude-fable-5-1", []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	state, _ := (*seen)[0]["state"].(map[string]any)
	if _, ok := state["turn_id"]; !ok {
		t.Fatalf("custom (default) provider must keep turn_id for local adapters: %v", state)
	}
}

func TestProviderConfigErrors(t *testing.T) {
	sf := "state-file: " + filepath.Join(t.TempDir(), "s.jsonl") + "\n"
	for name, text := range map[string]string{
		"unknown provider": "jev-provider: semif\n",
		"typesafe no key":  "jev-provider: typesafe\n",
		"opencode no key":  "jev-provider: opencode\n",
		"conflicting url":  "jev-provider: opencode\njev-api-key: k\njev-url: http://127.0.0.1:8765/v1/systemone\n",
	} {
		if configure([]byte(text+sf)) == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if err := configure([]byte("jev-provider: typesafe\njev-api-key: k\njev-url: " + providerURLs["typesafe"] + "\n" + sf)); err != nil {
		t.Errorf("matching jev-url must be accepted: %v", err)
	}
}
