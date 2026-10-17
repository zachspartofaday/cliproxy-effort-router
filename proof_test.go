package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProofEvidenceRedactionAndPrefix(t *testing.T) {
	jev(t, map[string]string{"first synthetic turn": "low", "second synthetic turn": "xhigh"}, "")
	file := filepath.Join(t.TempDir(), "proof.jsonl")
	if err := configure([]byte("jev-url: " + cfg.URL + "\nstate-file: " + cfg.StateFile + "\nproof-file: " + file)); err != nil {
		t.Fatal(err)
	}
	normalizeNative(t, nativeFixture(t, "turn-1"), "codex")
	normalizeNative(t, nativeFixture(t, "tool-loop"), "codex")
	normalizeNative(t, nativeFixture(t, "turn-2"), "codex")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"synthetic turn", "synthetic-native-session", "synthetic answer", "Execute synthetic", "text(1 + 1)", "Bearer"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatalf("proof leaks %s", private)
		}
	}
	scan := bufio.NewScanner(bytes.NewReader(raw))
	var lines []proofLine
	for scan.Scan() {
		var l proofLine
		if json.Unmarshal(scan.Bytes(), &l) != nil {
			t.Fatal("invalid proof JSON")
		}
		lines = append(lines, l)
	}
	if len(lines) != 3 || lines[1].PrefixPreserved == nil || !*lines[1].PrefixPreserved || lines[2].TopPreserved == nil || !*lines[2].TopPreserved {
		t.Fatal("missing prefix/top preservation evidence")
	}
	for _, line := range lines {
		if !line.RequestTopUnchanged {
			t.Fatal("router changed native top-level fields")
		}
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0600 {
		t.Fatal("proof file not private")
	}
}
