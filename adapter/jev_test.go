package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeJev(t *testing.T, status int, answer string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); io.WriteString(w, answer) }))
}
func TestJevContractAndPrimary(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer fake-zen" || r.Header.Get("X-Opencode-Session") != "" {
			t.Error("incorrect Jev endpoint/credential")
		}
		var body struct {
			Model     string                    `json:"model"`
			State     map[string]any            `json:"state"`
			Questions map[string]map[string]any `json:"questions"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		if body.Model != "jev-1.13-free" || body.State["user_message"] != "sensitive-user-text" || len(body.State) != 2 {
			t.Error("incorrect Jev model or disclosure")
		}
		if body.Questions["effort"]["instructions"] != effortInstructions {
			t.Error("untrusted instructions forwarded")
		}
		io.WriteString(w, `{"answers":{"effort":{"choice":"high","probabilities":{"high":0.9}}}}`)
	}))
	defer upstream.Close()
	var logs bytes.Buffer
	s, err := newJevServer(upstream.URL+"/v1/systemone", "jev-1.13-free", "fake-zen", time.Second, log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	in := testInput()
	in.State.Shadow = true
	in.State.TurnID = strings.Repeat("a", 64)
	in.Questions.Effort.Instructions = "malicious caller instructions"
	w := post(s, in)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"choice":"high"`) {
		t.Fatalf("Jev primary %d", w.Code)
	}
	for _, sensitive := range []string{"fake-zen", "sensitive-user-text", "sensitive-assistant-text", "probabilities"} {
		if strings.Contains(logs.String()+w.Body.String(), sensitive) {
			t.Fatal("Jev response/log leaks private fields")
		}
	}
	if !strings.Contains(logs.String(), `"available":false`) {
		t.Fatal("missing usage should not be presented as measured")
	}
}
func TestParallelShadowComparison(t *testing.T) {
	ready := make(chan string, 2)
	release := make(chan struct{})
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["state"].(map[string]any)["user_message"] != "sensitive-user-text" {
			t.Error("different Jev state")
		}
		ready <- "jev"
		<-release
		io.WriteString(w, `{"answers":{"effort":{"choice":"high"}}}`)
	}))
	defer jev.Close()
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		items := body["input"].([]any)
		var state map[string]string
		json.Unmarshal([]byte(items[1].(map[string]any)["content"].(string)), &state)
		if state["user_message"] != "sensitive-user-text" || len(state) != 2 {
			t.Error("different DS state")
		}
		if !strings.Contains(items[0].(map[string]any)["content"].(string), effortCriteria()["high"]) {
			t.Error("different rubric")
		}
		if r.Header.Get("Authorization") != "Bearer fake-cpa" {
			t.Error("Zen key forwarded to CPA")
		}
		ready <- "ds"
		<-release
		io.WriteString(w, successfulResponse("medium"))
	}))
	defer cpa.Close()
	var logs bytes.Buffer
	s, _ := newJevServer(jev.URL+"/v1/systemone", "jev-1.13-free", "fake-zen", time.Second, log.New(&logs, "", 0))
	s.comparison, _ = newServer(cpa.URL, "ocg/deepseek-v4.1-flash", "fake-cpa", time.Second, nil)
	in := testInput()
	in.State.Shadow = true
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { finished <- post(s, in) }()
	// Both mocks must be entered before either can finish: proves concurrent calls.
	for range 2 {
		select {
		case <-ready:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("classifiers did not run concurrently")
		}
	}
	close(release)
	w := <-finished
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"choice":"high"`) {
		t.Fatal("comparator replaced Jev primary")
	}
	var entry struct {
		Primary, Comparison observation
		Agreement           *bool
	}
	if json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry) != nil || entry.Primary.Effort != "high" || entry.Comparison.Effort != "medium" || entry.Agreement == nil || *entry.Agreement {
		t.Fatalf("missing paired disagreement: %s", logs.String())
	}
	for _, secret := range []string{"fake-zen", "fake-cpa", "sensitive-user-text", "sensitive-assistant-text"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("paired logs leak input")
		}
	}
}
func TestComparisonRequiresShadowBeforeNetwork(t *testing.T) {
	var count atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1) }))
	defer upstream.Close()
	s, _ := newJevServer(upstream.URL+"/v1/systemone", "jev-1.13-free", "fake", time.Second, nil)
	s.comparison, _ = newServer(upstream.URL, "ocg/fake", "fake", time.Second, nil)
	if w := post(s, testInput()); w.Code != 400 || count.Load() != 0 {
		t.Fatal("active comparison reached upstream")
	}
}
func TestComparisonFailuresNeverFallback(t *testing.T) {
	for _, tc := range []struct {
		primary, secondary int
		expected           int
	}{{500, 200, 502}, {200, 500, 200}} {
		t.Run(http.StatusText(tc.primary)+http.StatusText(tc.secondary), func(t *testing.T) {
			jev := fakeJev(t, tc.primary, `{"answers":{"effort":{"choice":"high"}}}`)
			defer jev.Close()
			cpa := fakeJev(t, tc.secondary, successfulResponse("low"))
			defer cpa.Close()
			var logs bytes.Buffer
			s, _ := newJevServer(jev.URL+"/v1/systemone", "jev-1.13-free", "fake", time.Second, log.New(&logs, "", 0))
			s.comparison, _ = newServer(cpa.URL, "ocg/fake", "fake", time.Second, nil)
			in := testInput()
			in.State.Shadow = true
			w := post(s, in)
			if w.Code != tc.expected || strings.Contains(w.Body.String(), `"choice":"low"`) || strings.Contains(logs.String(), `"agreement"`) {
				t.Fatal("failure fallback or misleading agreement")
			}
		})
	}
}
func TestJevResponseBoundsAndValidation(t *testing.T) {
	for _, answer := range []string{`{"answers":{"effort":{"choice":"max"}}}`, `not-json secret`, strings.Repeat("x", 16385)} {
		upstream := fakeJev(t, 200, answer)
		s, _ := newJevServer(upstream.URL+"/v1/systemone", "jev-1.13-free", "fake", time.Second, nil)
		if w := post(s, testInput()); w.Code != 502 || strings.Contains(w.Body.String(), "secret") {
			t.Fatal("invalid Jev result accepted or echoed")
		}
		upstream.Close()
	}
	for _, endpoint := range []string{"http://opencode.ai/zen/v1/systemone", "https://example.com/v1/systemone", "https://opencode.ai/zen/v1/systemone?key=fake", "http://127.0.0.1/other"} {
		if _, err := newJevServer(endpoint, "jev-1.13-free", "fake", time.Second, nil); err == nil {
			t.Fatalf("unsafe endpoint %s", endpoint)
		}
	}
}
func TestComparisonTimeoutKeepsJevDecision(t *testing.T) {
	jev := fakeJev(t, 200, `{"answers":{"effort":{"choice":"high"}}}`)
	defer jev.Close()
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer cpa.Close()
	var logs bytes.Buffer
	s, _ := newJevServer(jev.URL+"/v1/systemone", "jev-1.13-free", "fake", 20*time.Millisecond, log.New(&logs, "", 0))
	s.comparison, _ = newServer(cpa.URL, "ocg/fake", "fake", 20*time.Millisecond, nil)
	in := testInput()
	in.State.Shadow = true
	if w := post(s, in); w.Code != 200 || !strings.Contains(w.Body.String(), `"choice":"high"`) || !strings.Contains(logs.String(), `"outcome":"timeout"`) {
		t.Fatal("comparator timeout replaced Jev")
	}
}
