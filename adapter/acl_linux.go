package main

import (
	"os"
	"syscall"
	"unsafe"
)

// accessACL returns the open file's raw system.posix_acl_access value.
func accessACL(f *os.File) ([]byte, bool) {
	name, err := syscall.BytePtrFromString("system.posix_acl_access")
	if err != nil {
		return nil, false
	}
	buf := make([]byte, 1024)
	n, _, errno := syscall.Syscall6(syscall.SYS_FGETXATTR, f.Fd(), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	if errno != 0 {
		return nil, false
	}
	return buf[:n], true
}
