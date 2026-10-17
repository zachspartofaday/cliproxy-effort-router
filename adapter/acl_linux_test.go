package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// setACL writes a raw access ACL; the kernel derives the group mode bits from its mask.
func setACL(t *testing.T, path string, acl []byte) {
	t.Helper()
	if err := syscall.Setxattr(path, "system.posix_acl_access", acl, 0); err != nil {
		if errors.Is(err, syscall.EOPNOTSUPP) {
			t.Skip("filesystem has no POSIX ACL support")
		}
		t.Fatal(err)
	}
}

// A file shaped like systemd's LoadCredential copy (0400 plus a read ACL for
// this process's user) passes only directly inside $CREDENTIALS_DIRECTORY.
func TestSystemdCredentialACL(t *testing.T) {
	uid := uint32(os.Geteuid())
	creds := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", creds)
	systemdCopy := func(dir string, acl []byte) string {
		path := writeCredential(t, dir, 0400)
		setACL(t, path, acl)
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o440 {
			t.Fatalf("ACL fixture mode %v, %v", info.Mode().Perm(), err)
		}
		return path
	}
	if key, err := readCredential(systemdCopy(creds, systemdACL(uid))); err != nil || key != "fake-only" {
		t.Fatal("systemd credential copy rejected")
	}
	if _, err := readCredential(systemdCopy(creds, systemdACL(uid+1))); err == nil {
		t.Fatal("credential readable by another user accepted")
	}
	nested := filepath.Join(creds, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredential(systemdCopy(nested, systemdACL(uid))); err == nil {
		t.Fatal("credential below the credentials directory accepted")
	}
	if _, err := readCredential(systemdCopy(t.TempDir(), systemdACL(uid))); err == nil {
		t.Fatal("credential outside the credentials directory accepted")
	}
	path := systemdCopy(creds, systemdACL(uid))
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	if _, err := readCredential(path); err == nil {
		t.Fatal("group-readable credential accepted without CREDENTIALS_DIRECTORY")
	}
}
