package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testInput() jevRequest {
	var in jevRequest
	in.Model = "jev-latest"
	in.State.Message = "sensitive-user-text"
	in.State.Previous = "sensitive-assistant-text"
	in.Questions.Effort.Type = "choice"
	in.Questions.Effort.Criteria = map[string]string{}
	for _, c := range choices {
		in.Questions.Effort.Criteria[c] = "description"
	}
	return in
}
func successfulResponse(effort string) string {
	answer, _ := json.Marshal(map[string]string{"effort": effort})
	raw, _ := json.Marshal(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": string(answer)}}}}, "usage": map[string]any{"input_tokens": 123, "output_tokens": 9}})
	return string(raw)
}
func post(s *server, in jevRequest) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(in)
	req := httptest.NewRequest("POST", "/v1/systemone", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}
func TestContractViaMockCPA(t *testing.T) {
	var sessions []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer fake-only" {
			t.Error("wrong endpoint/auth")
		}
		sessions = append(sessions, r.Header.Get("X-Opencode-Session"))
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "ocg/deepseek-v4.1-flash" || body["store"] != false || body["stream"] != false {
			t.Error("wrong model or persistence")
		}
		format := body["text"].(map[string]any)["format"].(map[string]any)
		if format["type"] != "json_schema" || format["strict"] != true {
			t.Error("missing structured output")
		}
		schema := format["schema"].(map[string]any)
		if schema["additionalProperties"] != false {
			t.Error("schema accepts extras")
		}
		io.WriteString(w, successfulResponse("high"))
	}))
	defer upstream.Close()
	var logs bytes.Buffer
	s, err := newServer(upstream.URL, "ocg/deepseek-v4.1-flash", "fake-only", time.Second, log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if w := post(s, testInput()); w.Code != 200 || !strings.Contains(w.Body.String(), `"choice":"high"`) {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	if sessions[0] == "" || sessions[0] != sessions[1] {
		t.Fatal("session must be stable")
	}
	in := testInput()
	in.State.Message = "other turn"
	post(s, in)
	if sessions[0] == sessions[2] {
		t.Fatal("unrelated turns shared session")
	}
	for _, secret := range []string{"fake-only", "sensitive-user-text", "sensitive-assistant-text"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("logs leak input")
		}
	}
	if !strings.Contains(logs.String(), `"input_tokens":123`) {
		t.Fatal("missing usage")
	}
}
func TestUpstreamFailuresAreBoundedAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"http", "secret-from-cpa", 401}, {"malformed", "secret-from-cpa", 200}, {"unknown", successfulResponse("max"), 200},
		{"oversize", strings.Repeat("x", 65537), 200}, {"incomplete", `{"status":"incomplete","output":[]}`, 200},
		{"refusal", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"secret-from-cpa"}]}]}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer upstream.Close()
			var logs bytes.Buffer
			s, _ := newServer(upstream.URL, "ocg/fake", "fake-only", time.Second, log.New(&logs, "", 0))
			w := post(s, testInput())
			if w.Code != 502 || strings.Contains(logs.String()+w.Body.String(), "secret-from-cpa") {
				t.Fatal("failure not safe")
			}
		})
	}
}
func TestTimeoutRedirectAndCancellation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer upstream.Close()
	s, _ := newServer(upstream.URL, "ocg/fake", "fake", 20*time.Millisecond, nil)
	if w := post(s, testInput()); w.Code != 504 {
		t.Fatalf("timeout %d", w.Code)
	}
	reached := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer redirect.Close()
	s, _ = newServer(redirect.URL, "ocg/fake", "fake", time.Second, nil)
	if w := post(s, testInput()); w.Code != 502 || reached {
		t.Fatal("redirect followed")
	}
}
func TestInvalidContractAndBusyDoNotReachCPA(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected upstream request") }))
	defer upstream.Close()
	s, _ := newServer(upstream.URL, "ocg/fake", "fake", time.Second, nil)
	in := testInput()
	in.Model = "arbitrary"
	if w := post(s, in); w.Code != 400 {
		t.Fatal(w.Code)
	}
	in = testInput()
	in.State.Message = strings.Repeat("x", 1201)
	if w := post(s, in); w.Code != 400 {
		t.Fatal(w.Code)
	}
	for range cap(s.slots) {
		s.slots <- struct{}{}
	}
	if w := post(s, testInput()); w.Code != 503 {
		t.Fatal(w.Code)
	}
}
func TestLoopbackAndCredential(t *testing.T) {
	for _, origin := range []string{"https://127.0.0.1", "http://localhost", "http://192.0.2.1", "http://user:pass@127.0.0.1", "http://127.0.0.1?key=fake", "http://127.0.0.1/v1"} {
		if _, err := loopbackURL(origin); err == nil {
			t.Fatalf("accepted %s", origin)
		}
	}
	path := filepath.Join(t.TempDir(), "fake-credential")
	if err := os.WriteFile(path, []byte("fake-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if key, err := readCredential(path); err != nil || key != "fake-only" {
		t.Fatal("private credential rejected")
	}
	os.Chmod(path, 0644)
	if _, err := readCredential(path); err == nil {
		t.Fatal("public credential accepted")
	}
}

func writeCredential(t *testing.T, dir string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, "zen-key")
	os.Remove(path)
	if err := os.WriteFile(path, []byte("fake-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// Group read without a matching ACL stays rejected everywhere, as do symlinks
// and any other group or world bit, even inside the credentials directory.
func TestCredentialModeWithoutACL(t *testing.T) {
	creds := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", creds)
	if key, err := readCredential(writeCredential(t, creds, 0400)); err != nil || key != "fake-only" {
		t.Fatal("owner-only credential rejected")
	}
	for _, mode := range []os.FileMode{0440, 0444, 0460, 0450, 0640, 0404} {
		if _, err := readCredential(writeCredential(t, creds, mode)); err == nil {
			t.Fatalf("credential %o without ACL accepted", mode)
		}
	}
	target := writeCredential(t, t.TempDir(), 0600)
	link := filepath.Join(creds, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredential(link); err == nil {
		t.Fatal("symlinked credential accepted")
	}
}

func aclValue(entries ...[3]uint32) []byte {
	b := []byte{2, 0, 0, 0}
	for _, e := range entries {
		b = binary.LittleEndian.AppendUint16(b, uint16(e[0]))
		b = binary.LittleEndian.AppendUint16(b, uint16(e[1]))
		b = binary.LittleEndian.AppendUint32(b, e[2])
	}
	return b
}

const aclUndefinedID = 0xffffffff

// systemdACL is the access ACL systemd sets on a LoadCredential copy for uid.
func systemdACL(uid uint32, extra ...[3]uint32) []byte {
	entries := [][3]uint32{{aclUserObj, 4, aclUndefinedID}, {aclUser, 4, uid}}
	entries = append(entries, extra...)
	entries = append(entries, [3]uint32{aclGroupObj, 0, aclUndefinedID}, [3]uint32{aclMask, 4, aclUndefinedID}, [3]uint32{aclOther, 0, aclUndefinedID})
	return aclValue(entries...)
}

func TestACLGrantsOnly(t *testing.T) {
	const uid = 984
	if !aclGrantsOnly(systemdACL(uid), uid) {
		t.Fatal("systemd credential ACL rejected")
	}
	owner := [3]uint32{aclUserObj, 4, aclUndefinedID}
	user := [3]uint32{aclUser, 4, uid}
	group := [3]uint32{aclGroupObj, 0, aclUndefinedID}
	mask := [3]uint32{aclMask, 4, aclUndefinedID}
	other := [3]uint32{aclOther, 0, aclUndefinedID}
	for name, acl := range map[string][]byte{
		"other user":        systemdACL(uid + 1),
		"extra user":        systemdACL(uid, [3]uint32{aclUser, 4, uid + 1}),
		"duplicate user":    systemdACL(uid, user),
		"named group":       systemdACL(uid, [3]uint32{aclGroup, 4, 0}),
		"owning group":      aclValue(owner, user, [3]uint32{aclGroupObj, 4, aclUndefinedID}, mask, other),
		"other":             aclValue(owner, user, group, mask, [3]uint32{aclOther, 4, aclUndefinedID}),
		"no named user":     aclValue(owner, group, other),
		"named user only":   aclValue(user),
		"missing owner":     aclValue(user, group, mask, other),
		"missing group":     aclValue(owner, user, mask, other),
		"missing mask":      aclValue(owner, user, group, other),
		"missing other":     aclValue(owner, user, group, mask),
		"duplicate owner":   aclValue(owner, owner, user, group, mask, other),
		"duplicate mask":    aclValue(owner, user, group, mask, mask, other),
		"out of order":      aclValue(owner, user, mask, group, other),
		"invalid perm bits": aclValue([3]uint32{aclUserObj, 8 | 4, aclUndefinedID}, user, group, mask, other),
		"unknown tag":       aclValue(owner, user, group, mask, other, [3]uint32{0x40, 0, 0}),
		"bad version":       append([]byte{1, 0, 0, 0}, systemdACL(uid)[4:]...),
		"truncated":         systemdACL(uid)[:9],
		"header only":       aclValue(),
		"empty":             nil,
	} {
		if aclGrantsOnly(acl, uid) {
			t.Fatalf("%s ACL accepted", name)
		}
	}
}

type fakeInfo struct {
	os.FileInfo
	mode os.FileMode
	uid  uint32
}

func (f fakeInfo) Mode() os.FileMode { return f.mode }
func (f fakeInfo) Sys() any          { return &syscall.Stat_t{Uid: f.uid} }

// Even owner-only files must be owned by root or this process.
func TestCredentialOwner(t *testing.T) {
	self := uint32(os.Geteuid())
	other := self + 1
	if self == 0 {
		other = 65534
	}
	for uid, want := range map[uint32]bool{0: true, self: true, other: false} {
		if got := privateCredential(nil, "/unused", fakeInfo{mode: 0400, uid: uid}); got != want {
			t.Fatalf("owner %d accepted=%v, want %v", uid, got, want)
		}
	}
}
