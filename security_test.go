package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestShadowIsByteIdenticalAndDoesNotReplayInActive(t *testing.T) {
	calls := jev(t, map[string]string{"hi": "high"}, "shadow: true")
	bodies := []struct {
		model, body string
		route       func(string, []byte) []byte
	}{
		{"claude-opus-5-5", `{ "messages": [{"role":"user","content":"hi"}] }`, route},
		{"gpt-6-sol", `{ "input": [{"role":"user","content":"hi"}] }`, routeCodex},
	}
	for _, b := range bodies {
		for range 2 {
			if out := b.route(b.model, []byte(b.body)); !bytes.Equal(out, []byte(b.body)) {
				t.Fatal("shadow altered request")
			}
		}
	}
	if *calls != 2 {
		t.Fatal("shadow reclassified turn")
	}
	state.load(cfg.StateFile)
	for _, b := range bodies {
		b.route(b.model, []byte(b.body))
	}
	if *calls != 2 {
		t.Fatal("shadow restart lost decisions")
	}
	c := cfg
	c.Shadow = false
	// Reconfigure using same file through the actual parser.
	raw := []byte("jev-url: " + c.URL + "\nstate-file: " + c.StateFile + "\nshadow: false\n")
	if err := configure(raw); err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"done"}]}`)
	if out := route("claude-opus-5-5", old); !bytes.Equal(out, old) {
		t.Fatal("past shadow effort injected")
	}
	state.load(cfg.StateFile)
	if out := route("claude-opus-5-5", old); !bytes.Equal(out, old) {
		t.Fatal("past shadow effort injected after restart")
	}
}
func TestClassifierPayloadPrivacyAndErrors(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.WriteHeader(500)
		io.WriteString(w, "echoed-sensitive-text")
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "state")
	if err := configure([]byte("jev-url: " + srv.URL + "\nstate-file: " + file)); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("界", 1300)
	raw, _ := json.Marshal(map[string]any{"messages": []any{map[string]string{"role": "assistant", "content": strings.Repeat("a", 700)}, map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "<system-reminder>private reminder"}, map[string]string{"type": "text", "text": text}}}}})
	route("claude-opus-5-5", raw)
	sent := captured["state"].(map[string]any)
	if len([]rune(sent["user_message"].(string))) != 1200 || len(sent["assistant_previous_reply_tail"].(string)) != 600 {
		t.Fatal("wrong classifier disclosure")
	}
	log, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{"界", "echoed-sensitive-text", "private reminder", "preview"} {
		if bytes.Contains(log, []byte(sensitive)) {
			t.Fatal("state leaks text")
		}
	}
}
func TestConcurrentAndRecursiveCPARequests(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		// Re-enter the same normalizer just as the CPA-backed adapter does.
		body := []byte(`{"input":[{"role":"user","content":"classify"}]}`)
		if !bytes.Equal(routeCodex("ocg/deepseek-v4.1-flash", body), body) {
			t.Error("classifier routed recursively")
		}
		time.Sleep(20 * time.Millisecond)
		io.WriteString(w, `{"answers":{"effort":{"choice":"high"}}}`)
	}))
	defer srv.Close()
	if err := configure([]byte("jev-url: " + srv.URL + "\nstate-file: " + filepath.Join(t.TempDir(), "state"))); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	body := []byte(`{"messages":[{"role":"user","content":"same turn"}]}`)
	for range 20 {
		wg.Go(func() {
			if bytes.Equal(route("claude-opus-5-5", body), body) {
				t.Error("decision lost")
			}
		})
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal("duplicate classification")
	}
}
func TestStateFailureAndSymlinkDoNotApplyOrWrite(t *testing.T) {
	jev(t, map[string]string{"hi": "high"}, "")
	target := filepath.Join(t.TempDir(), "target")
	os.WriteFile(target, []byte("private-file"), 0600)
	os.Symlink(target, cfg.StateFile)
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	if !bytes.Equal(route("claude-opus-5-5", body), body) {
		t.Fatal("unpersisted decision applied")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "private-file" {
		t.Fatal("symlink target changed")
	}
}
func TestInvalidConfigRetainsPriorConfig(t *testing.T) {
	jev(t, nil, "")
	before := cfg
	for _, raw := range []string{"timeout-ms: 0", "timeout-ms: -1", "timeout-ms: 30001", "models: [ocg/]", "models: ['']", "jev-url: http://fake:key@127.0.0.1/", "jev-url: http://127.0.0.1/?key=fake", "state-file: relative", "shadow: [bad]"} {
		if configure([]byte(raw)) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if cfg.URL != before.URL || cfg.StateFile != before.StateFile {
		t.Fatal("invalid reconfiguration changed runtime")
	}
}
func TestClassifierTimeoutSizeAndRedirect(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"timeout", func(w http.ResponseWriter, r *http.Request) { time.Sleep(50 * time.Millisecond) }},
		{"size", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat("x", 16385)) }},
		{"redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://127.0.0.1:1", 307) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			configure([]byte("jev-url: " + srv.URL + "\nstate-file: " + filepath.Join(t.TempDir(), "state") + "\ntimeout-ms: 10"))
			body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
			if !bytes.Equal(route("claude-opus-5-5", body), body) {
				t.Fatal("failure injected")
			}
		})
	}
}

func TestClaudeSecondNormalizePassIsStable(t *testing.T) {
	jev(t, map[string]string{"hi": "high"}, "")
	first := route("claude-opus-5-5", []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if second := route("claude-opus-5-5", first); !bytes.Equal(first, second) {
		t.Fatal("second pass duplicated directive")
	}
}
func TestRepeatedMessageWithDifferentHistoryGetsNewDecision(t *testing.T) {
	calls := jev(t, map[string]string{"hi": "high"}, "")
	route("claude-opus-5-5", []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	route("claude-opus-5-5", []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[]},{"role":"user","content":"hi"}]}`))
	if *calls != 2 {
		t.Fatal("distinct turn conflated")
	}
}

func TestTopLevelClaudeEffortOverrideWins(t *testing.T) {
	calls := jev(t, map[string]string{"hi": "low"}, "")
	body := []byte(`{"output_config":{"effort":"max"},"messages":[{"role":"user","content":"hi"}]}`)
	if !bytes.Equal(route("claude-opus-5-5", body), body) || *calls != 0 {
		t.Fatal("explicit top-level effort overridden")
	}
}

func TestPluginMarksClassifierShadowStatus(t *testing.T) {
	for _, shadow := range []bool{true, false} {
		var marked bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				State struct {
					Shadow bool `json:"router_shadow"`
				} `json:"state"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				t.Error("invalid classification payload")
			}
			marked = req.State.Shadow
			io.WriteString(w, `{"answers":{"effort":{"choice":"high"}}}`)
		}))
		value := "false"
		if shadow {
			value = "true"
		}
		if err := configure([]byte("jev-url: " + srv.URL + "\nstate-file: " + filepath.Join(t.TempDir(), "state") + "\nshadow: " + value)); err != nil {
			t.Fatal(err)
		}
		body := []byte(`{"messages":[{"role":"user","content":"shadow marker test"}]}`)
		out := route("claude-opus-5-5", body)
		if marked != shadow || shadow && !bytes.Equal(out, body) {
			t.Fatal("missing shadow provenance")
		}
		srv.Close()
	}
}
