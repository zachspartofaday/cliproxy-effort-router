package main

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func nativeFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/codex-0.162.1/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func normalizeNative(t *testing.T, body []byte, target string) []byte {
	t.Helper()
	in, _ := json.Marshal(pluginapi.RequestTransformRequest{FromFormat: "openai-response", ToFormat: target, Model: "gpt-6.1-sol", Stream: gjson.GetBytes(body, "stream").Bool(), Body: body})
	raw, err := handleMethod(pluginabi.MethodRequestNormalize, in)
	if err != nil {
		t.Fatal(err)
	}
	var envelope pluginabi.Envelope
	var result pluginapi.PayloadResponse
	if json.Unmarshal(raw, &envelope) != nil || !envelope.OK || json.Unmarshal(envelope.Result, &result) != nil {
		t.Fatal("invalid ABI response")
	}
	return result.Body
}
func inputPrefix(t *testing.T, before, after []byte) {
	t.Helper()
	a, b := gjson.GetBytes(before, "input").Array(), gjson.GetBytes(after, "input").Array()
	if len(b) < len(a) {
		t.Fatal("history shrank")
	}
	for i := range a {
		if a[i].Raw != b[i].Raw {
			t.Fatalf("history prefix changed at %d", i)
		}
	}
}
func updateEfforts(body []byte) []string {
	var efforts []string
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "configuration_update" {
			efforts = append(efforts, item.Get("reasoning.effort").String())
		}
	}
	return efforts
}
func TestNativeCodexAppendOnlyReplayAndToolLoop(t *testing.T) {
	calls := jev(t, map[string]string{"first synthetic turn": "low", "second synthetic turn": "xhigh"}, "")
	first := normalizeNative(t, nativeFixture(t, "turn-1"), "codex")
	loop := normalizeNative(t, nativeFixture(t, "tool-loop"), "codex")
	secondBody := nativeFixture(t, "turn-2")
	second := normalizeNative(t, secondBody, "codex")
	inputPrefix(t, first, loop)
	inputPrefix(t, loop, second)
	if !slices.Equal(updateEfforts(second), []string{"low", "xhigh"}) || *calls != 2 {
		t.Fatal("wrong turn efforts or duplicate classification")
	}
	for _, field := range []string{"tools", "reasoning", "store", "text", "include", "prompt_cache_key", "client_metadata"} {
		if gjson.GetBytes(secondBody, field).Raw != gjson.GetBytes(second, field).Raw {
			t.Fatalf("native field %s changed", field)
		}
	}
	state.load(cfg.StateFile)
	if restored := normalizeNative(t, secondBody, "codex"); !bytes.Equal(second, restored) || *calls != 2 {
		t.Fatal("restart changed native history")
	}
	if repeated := normalizeNative(t, second, "codex"); !bytes.Equal(second, repeated) {
		t.Fatal("repeat pass changed native history")
	}
}
func TestNativeStandaloneCompactAndStreamingTrigger(t *testing.T) {
	calls := jev(t, map[string]string{"first synthetic turn": "low", "second synthetic turn": "high"}, "")
	normalizeNative(t, nativeFixture(t, "turn-1"), "codex")
	compact := nativeFixture(t, "standalone-compact")
	if out := normalizeNative(t, compact, "openai-response"); !bytes.Equal(out, compact) || len(updateEfforts(out)) != 0 {
		t.Fatal("standalone compact acquired updates")
	}
	stream := normalizeNative(t, nativeFixture(t, "stream-compact"), "codex")
	if !slices.Equal(updateEfforts(stream), []string{"low"}) || *calls != 1 {
		t.Fatal("stream compaction lost replay or classified synthetic control")
	}
	if gjson.GetBytes(stream, "input").Array()[len(gjson.GetBytes(stream, "input").Array())-1].Get("type").String() != "compaction_trigger" {
		t.Fatal("trigger lost")
	}
}
func TestNativeCompactionStartsFreshWindow(t *testing.T) {
	calls := jev(t, map[string]string{"first synthetic turn": "low", "after compact synthetic turn": "high"}, "")
	normalizeNative(t, nativeFixture(t, "turn-1"), "codex")
	body := nativeFixture(t, "after-compact")
	out := normalizeNative(t, body, "codex")
	if !slices.Equal(updateEfforts(out), []string{"high"}) || *calls != 2 {
		t.Fatal("obsolete window replay or missing fresh effort")
	}
	items := gjson.GetBytes(out, "input").Array()
	if items[2].Get("type").String() != "compaction" || items[3].Get("type").String() != "configuration_update" {
		t.Fatal("compaction anchor changed")
	}
	state.load(cfg.StateFile)
	if !bytes.Equal(out, normalizeNative(t, body, "codex")) {
		t.Fatal("compacted window changed after restart")
	}
}
func TestNativeShadowAndFailuresAreByteIdentical(t *testing.T) {
	jev(t, map[string]string{"first synthetic turn": "low", "second synthetic turn": "high", "after compact synthetic turn": "medium"}, "shadow: true")
	for _, name := range []string{"turn-1", "tool-loop", "turn-2", "stream-compact", "after-compact"} {
		body := nativeFixture(t, name)
		if !bytes.Equal(body, normalizeNative(t, body, "codex")) {
			t.Fatalf("shadow changed %s", name)
		}
	}
	jev(t, nil, "")
	body := nativeFixture(t, "turn-1")
	if !bytes.Equal(body, normalizeNative(t, body, "codex")) {
		t.Fatal("failed classifier changed native request")
	}
}
func TestNativeClientUpdateOwnership(t *testing.T) {
	for _, position := range []string{"before", "after"} {
		for _, effort := range []string{"high", "low"} {
			t.Run(position+effort, func(t *testing.T) {
				calls := jev(t, map[string]string{"first synthetic turn": "medium"}, "")
				body := nativeFixture(t, "turn-1")
				items := gjson.GetBytes(body, "input").Array()
				update := `{"type":"configuration_update","reasoning":{"effort":"` + effort + `"}}`
				user := items[1].Raw
				raw := items[0].Raw + "," + update + "," + user
				if position == "after" {
					raw = items[0].Raw + "," + user + "," + update
				}
				body, _ = sjson.SetRawBytes(body, "input", []byte("["+raw+"]"))
				out := normalizeNative(t, body, "codex")
				want := "medium"
				if effort == "low" {
					want = "low"
					if *calls != 0 {
						t.Fatal("classified manual override")
					}
				}
				if !slices.Equal(updateEfforts(out), []string{want}) {
					t.Fatal("duplicate update or manual setting lost")
				}
			})
		}
	}
}
func TestNativeUnsupportedControlsAndSeparateSessions(t *testing.T) {
	calls := jev(t, map[string]string{"first synthetic turn": "low"}, "")
	base := nativeFixture(t, "turn-1")
	for _, tc := range []struct {
		path  string
		value any
	}{{"reasoning.effort", "xhigh"}, {"truncation", "auto"}, {"context_management", []any{map[string]any{"type": "compaction", "compact_threshold": 1000}}}} {
		body, _ := sjson.SetBytes(base, tc.path, tc.value)
		if !bytes.Equal(body, normalizeNative(t, body, "codex")) {
			t.Fatal("unsupported/manual setting changed")
		}
	}
	if *calls != 0 {
		t.Fatal("unsupported request classified")
	}
	normalizeNative(t, base, "codex")
	other, _ := sjson.SetBytes(base, "prompt_cache_key", "different-synthetic-session")
	normalizeNative(t, other, "codex")
	if *calls != 2 {
		t.Fatal("native sessions shared a decision")
	}
}
