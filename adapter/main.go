package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	backend := flag.String("backend", "jev", "primary classifier: jev or ocg")
	compare := flag.Bool("compare-ocg", false, "compare OCG alongside Jev; requires explicit router shadow")
	jevURL := flag.String("jev-url", zenEndpoint, "Zen Jev endpoint or local mock /v1/systemone")
	jevModel := flag.String("jev-model", "jev-1.13-free", "upstream Jev model")
	zenCredential := flag.String("zen-credential-file", "", "protected Zen credential path")
	listen := flag.String("listen", "127.0.0.1:8765", "literal loopback listen address")
	cpa := flag.String("cpa-url", "http://127.0.0.1:8317", "CPA loopback origin")
	model := flag.String("model", "ocg/deepseek-v4.1-flash", "OCG alias")
	credential := flag.String("credential-file", "", "systemd credential path (never a key argument)")
	timeout := flag.Duration("timeout", 1200*time.Millisecond, "classifier deadline, shorter than plugin timeout")
	flag.Parse()
	credentialPath := func(explicit, name string) string {
		if explicit != "" {
			return explicit
		}
		if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
			return filepath.Join(dir, name)
		}
		return ""
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		fmt.Fprintln(os.Stderr, "listen must use a literal loopback IP")
		os.Exit(1)
	}
	// Read only service credentials required by the selected backend. In Jev-only
	// mode no CPA client key is loaded or added to the CPA config.
	logger := log.New(os.Stdout, "", 0)
	var handler *server
	switch *backend {
	case "jev":
		var key string
		key, err = readCredential(credentialPath(*zenCredential, "zen-key"))
		if err == nil {
			handler, err = newJevServer(*jevURL, *jevModel, key, *timeout, logger)
		}
	case "ocg":
		if *compare {
			err = fmt.Errorf("comparison requires Jev primary")
			break
		}
		var key string
		key, err = readCredential(credentialPath(*credential, "cpa-key"))
		if err == nil {
			handler, err = newServer(*cpa, *model, key, *timeout, logger)
		}
	default:
		err = fmt.Errorf("invalid backend")
	}
	if err == nil && *compare {
		var key string
		key, err = readCredential(credentialPath(*credential, "cpa-key"))
		if err == nil {
			handler.comparison, err = newServer(*cpa, *model, key, *timeout, nil)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid configuration or credential unavailable")
		os.Exit(1)
	}
	srv := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: *timeout + time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "adapter listener failed")
		os.Exit(1)
	}
}

func readCredential(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("credential unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !privateCredential(f, path, info) {
		return "", fmt.Errorf("unsafe credential")
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(b) > 4096 {
		return "", fmt.Errorf("invalid credential")
	}
	key := strings.TrimSpace(string(b))
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return "", fmt.Errorf("invalid credential")
	}
	return key, nil
}

// privateCredential accepts files owned by root or this process that are
// owner-only. For a non-root User=, systemd's LoadCredential copy is root-owned
// 0400 plus a read ACL for the service user, whose mask then shows as group
// read (0440, group::---). So the group read bit alone is also accepted for a
// file directly inside $CREDENTIALS_DIRECTORY whose ACL grants nothing beyond
// the owner and this process's user.
func privateCredential(f *os.File, path string, info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (st.Uid != 0 && int(st.Uid) != os.Geteuid()) {
		return false
	}
	perm := info.Mode().Perm()
	if perm&0o077 == 0 {
		return true
	}
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if dir == "" || perm&0o077 != 0o040 || filepath.Dir(filepath.Clean(path)) != filepath.Clean(dir) {
		return false
	}
	acl, ok := accessACL(f)
	return ok && aclGrantsOnly(acl, os.Geteuid())
}

// POSIX ACL entry tags, in the order Linux requires them.
const (
	aclUserObj  = 0x01
	aclUser     = 0x02
	aclGroupObj = 0x04
	aclGroup    = 0x08
	aclMask     = 0x10
	aclOther    = 0x20
)

// aclGrantsOnly reports whether a well-formed Linux system.posix_acl_access
// value (version 2; one owner, owning-group, mask and other entry; sorted by
// tag then id; rwx bits only) gives access to nobody but the file owner and
// uid: a named entry for uid alone, no named group, and empty owning-group and
// other entries.
func aclGrantsOnly(acl []byte, uid int) bool {
	if len(acl) < 4 || (len(acl)-4)%8 != 0 || binary.LittleEndian.Uint32(acl) != 2 {
		return false
	}
	count := map[uint16]int{}
	var lastTag uint16
	var lastID uint32
	for i, e := 0, acl[4:]; len(e) > 0; i, e = i+1, e[8:] {
		tag, perm, id := binary.LittleEndian.Uint16(e), binary.LittleEndian.Uint16(e[2:]), binary.LittleEndian.Uint32(e[4:])
		if perm&^7 != 0 || i > 0 && (tag < lastTag || tag == lastTag && (tag != aclUser || id <= lastID)) {
			return false
		}
		lastTag, lastID = tag, id
		count[tag]++
		switch tag {
		case aclUserObj, aclMask:
		case aclUser:
			if int(id) != uid {
				return false
			}
		case aclGroupObj, aclOther:
			if perm != 0 {
				return false
			}
		default: // named group or unknown tag
			return false
		}
	}
	return count[aclUserObj] == 1 && count[aclUser] == 1 && count[aclGroupObj] == 1 && count[aclMask] == 1 && count[aclOther] == 1
}
