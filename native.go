package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Native sampling only: CPA's standalone /responses/compact translates to
// openai-response and never calls this function. Do not infer endpoint from Stream.
func routeNativeCodex(model string, body []byte) []byte {
	c, s := snapshot()
	if s == nil || !strings.HasPrefix(model, "gpt-6") || !routed(c, model) || !gjson.ValidBytes(body) {
		return body
	}
	if e := gjson.GetBytes(body, "reasoning.effort"); e.Exists() && e.String() != c.CodexBaseline {
		return body
	}
	// OpenAI disallows configuration updates with automatic compaction/truncation.
	if gjson.GetBytes(body, "context_management").Exists() || gjson.GetBytes(body, "truncation").String() == "auto" {
		return body
	}
	items := gjson.GetBytes(body, "input").Array()
	boundary, lastUser, lastAnchor := -1, -1, -1
	compacting := false
	for i, it := range items {
		switch it.Get("type").String() {
		case "compaction_trigger":
			compacting = true
		case "function_call", "custom_tool_call", "reasoning", "compaction", "context_compaction":
			boundary = i
		}
		if typ := it.Get("type").String(); typ == "compaction" || typ == "context_compaction" {
			lastAnchor = i
		}
		if it.Get("role").String() == "assistant" {
			boundary = i
		}
		if _, ok := userText(it); ok {
			lastUser = i
		}
	}
	// Scope native identities by the client's cache/session key. Hash context
	// window anchors so a retained user message is not mistaken for a new turn.
	seed, _ := json.Marshal([]string{"native-v1", gjson.GetBytes(body, "prompt_cache_key").String()})
	scope := key(string(seed))
	history := scope
	var last gjson.Result
	manual := c.CodexBaseline
	inserts := map[int]string{}
	replacements := map[int]string{}
	for i, it := range items {
		typ := it.Get("type").String()
		if typ == "compaction" || typ == "context_compaction" {
			history = historyKey(scope, it.Raw)
			last = gjson.Result{}
			manual = c.CodexBaseline
		}
		if typ == "configuration_update" {
			manual = it.Get("reasoning.effort").String()
		}
		if it.Get("role").String() == "assistant" {
			last = it
		}
		text, ok := userText(it)
		if !ok {
			continue
		}
		history = historyKey(history, text)
		if i <= lastAnchor {
			continue
		} // Compaction starts a new context window.
		attached := -1
		selected := manual
		if i > 0 && items[i-1].Get("type").String() == "configuration_update" {
			attached = i - 1
		}
		if i+1 < len(items) && items[i+1].Get("type").String() == "configuration_update" {
			attached = i + 1
			selected = items[i+1].Get("reasoning.effort").String()
		}
		// Preserve explicit client settings, including updates after a user message.
		if selected != c.CodexBaseline {
			continue
		}
		effort := decide(c, s, model, text, assistantTail(last, 600), history, i == lastUser && i > boundary && !compacting)
		if c.Shadow || effort == "" {
			continue
		}
		if attached >= 0 {
			// A client baseline update occupies this position already; replace its
			// effort on every replay instead of adding an adjacent update. Other fields
			// and all nonbaseline updates remain client-owned.
			patched, err := sjson.SetBytes([]byte(items[attached].Raw), "reasoning.effort", effort)
			if err == nil {
				replacements[attached] = string(patched)
			}
		} else {
			inserts[i] = fmt.Sprintf(`{"type":"configuration_update","reasoning":{"effort":%q}}`, effort)
		}
	}
	if len(inserts)+len(replacements) == 0 {
		return body
	}
	out := make([]string, 0, len(items)+len(inserts))
	for i, it := range items {
		if d, ok := inserts[i]; ok {
			out = append(out, d)
		}
		if replacement, ok := replacements[i]; ok {
			out = append(out, replacement)
		} else {
			out = append(out, it.Raw)
		}
	}
	patched, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(out, ",")+"]"))
	if err != nil {
		return body
	}
	return patched
}
