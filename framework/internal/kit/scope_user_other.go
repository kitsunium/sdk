//go:build !windows

// Package kit — the current user outside Windows: the process's UID, read
// from the kernel. os/user would ask libc through cgo — linking the program
// dynamically — or read /etc/passwd, for a number the kernel gives at once.
package kit

import (
	"os"
	"strconv"
)

// currentUser is the current user's UID.
func currentUser() (string, error) { return strconv.Itoa(os.Getuid()), nil }
