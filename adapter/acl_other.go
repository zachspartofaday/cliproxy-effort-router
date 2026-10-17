//go:build !linux

package main

import "os"

// accessACL has no POSIX ACL support off Linux, so only owner-only credentials pass.
func accessACL(*os.File) ([]byte, bool) { return nil, false }
