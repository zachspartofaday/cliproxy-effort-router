package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type proofItem struct {
	Type    string `json:"type"`
	Role    string `json:"role,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Payload string `json:"payload,omitempty"`
}
type proofLine struct {
	Sequence            uint64      `json:"sequence"`
	From                string      `json:"from"`
	To                  string      `json:"to"`
	Model               string      `json:"model_prefix"`
	Shadow              bool        `json:"shadow"`
	Scope               string      `json:"scope"`
	Changed             bool        `json:"changed"`
	NewWindow           bool        `json:"new_window"`
	PrefixPreserved     *bool       `json:"prefix_preserved,omitempty"`
	TopPreserved        *bool       `json:"stable_top_fields_preserved,omitempty"`
	RequestTopUnchanged bool        `json:"request_top_fields_unchanged"`
	Before              []proofItem `json:"before"`
	After               []proofItem `json:"after"`
}
type proofPrefix struct {
	Items      []string
	Top, Epoch string
}

var proofState = struct {
	sync.Mutex
	Salt     []byte
	Sequence uint64
	Prefixes map[string]proofPrefix
}{Prefixes: make(map[string]proofPrefix)}

func proofDigest(raw string) string {
	digest := hmac.New(sha256.New, proofState.Salt)
	digest.Write([]byte(raw))
	return hex.EncodeToString(digest.Sum(nil))
}
func proofShape(body []byte, path string) []proofItem {
	var out []proofItem
	for _, it := range gjson.GetBytes(body, path).Array() {
		typ := it.Get("type").String()
		if typ == "" {
			typ = "message"
		}
		switch typ {
		case "message", "configuration_update", "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output", "reasoning", "compaction", "context_compaction", "compaction_trigger":
		default:
			typ = "other"
		}
		role := it.Get("role").String()
		switch role {
		case "user", "assistant", "system", "developer":
		default:
			role = ""
		}
		effort := ""
		if typ == "configuration_update" {
			effort = it.Get("reasoning.effort").String()
		} else if role == "system" {
			effort = it.Get("output_config.effort").String()
		}
		if _, ok := efforts[effort]; !ok {
			effort = ""
		}
		item := proofItem{Type: typ, Role: role, Effort: effort}
		if effort == "" {
			item.Payload = "[REDACTED]"
		}
		out = append(out, item)
	}
	return out
}

// This is normalizer-stage evidence, not an upstream acknowledgement. Only
// whitelisted enums and in-memory-keyed equality results reach the evidence file.
// Neither the HMAC salt, raw bodies, headers, IDs, arguments nor text are stored.
func recordProof(req pluginapi.RequestTransformRequest, after []byte) {
	c, _ := snapshot()
	if c.ProofFile == "" || modelPrefix(c, req.Model) == "" {
		return
	}
	if !gjson.ValidBytes(req.Body) || !gjson.ValidBytes(after) {
		return
	}
	proofState.Lock()
	defer proofState.Unlock()
	if proofState.Salt == nil {
		proofState.Salt = make([]byte, 32)
		if _, err := rand.Read(proofState.Salt); err != nil {
			proofState.Salt = nil
			return
		}
	}
	path := "input"
	if req.ToFormat == "claude" {
		path = "messages"
	}
	from, to := req.FromFormat, req.ToFormat
	allowed := func(format string) string {
		switch format {
		case "claude", "codex", "openai-response", "openai":
			return format
		}
		return "other"
	}
	from, to = allowed(from), allowed(to)
	cache := gjson.GetBytes(after, "prompt_cache_key").Raw
	if cache == "" {
		cache = gjson.GetBytes(after, "metadata.user_id").Raw
	}
	rawScope, _ := json.Marshal([]string{c.ProofFile, from, to, req.Model, cache})
	scope := proofDigest(string(rawScope))
	items := gjson.GetBytes(after, path).Array()
	if len(items) > 4096 || len(gjson.GetBytes(req.Body, path).Array()) > 4096 {
		return
	}
	digests := make([]string, len(items))
	epoch := "initial"
	for i, it := range items {
		digests[i] = proofDigest(it.Raw)
		switch it.Get("type").String() {
		case "compaction", "context_compaction":
			epoch = digests[i]
		}
	}
	top := []string{}
	beforeTop := []string{}
	for _, field := range []string{"model", "instructions", "tools", "reasoning", "text", "store", "include", "prompt_cache_key"} {
		top = append(top, gjson.GetBytes(after, field).Raw)
		beforeTop = append(beforeTop, gjson.GetBytes(req.Body, field).Raw)
	}
	rawTop, _ := json.Marshal(top)
	rawBeforeTop, _ := json.Marshal(beforeTop)
	topDigest := proofDigest(string(rawTop))
	proofState.Sequence++
	line := proofLine{Sequence: proofState.Sequence, From: from, To: to, Model: modelPrefix(c, req.Model), Shadow: c.Shadow, Scope: scope, Changed: !bytes.Equal(req.Body, after), NewWindow: true, Before: proofShape(req.Body, path), After: proofShape(after, path)}
	line.RequestTopUnchanged = bytes.Equal(rawTop, rawBeforeTop)
	if previous, ok := proofState.Prefixes[scope]; ok && previous.Epoch == epoch {
		same := len(digests) >= len(previous.Items)
		if same {
			for i, digest := range previous.Items {
				if digest != digests[i] {
					same = false
					break
				}
			}
		}
		topSame := previous.Top == topDigest
		line.PrefixPreserved = &same
		line.TopPreserved = &topSame
		line.NewWindow = false
	}
	// Bound diagnostic memory. Eviction makes the next sample a new comparison
	// window; it cannot affect routing or masquerade as an established match.
	if len(proofState.Prefixes) >= 128 {
		proofState.Prefixes = make(map[string]proofPrefix)
	}
	proofState.Prefixes[scope] = proofPrefix{Items: digests, Top: topDigest, Epoch: epoch}
	raw, _ := json.Marshal(line)
	f, err := openStateFile(c.ProofFile, true)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return
	}
	_, _ = f.Write(append(raw, '\n')) // Diagnostics failure must never change routing.
}
