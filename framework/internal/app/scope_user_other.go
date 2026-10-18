//go:build !windows

package app

import (
	"os"
	"strconv"
)

// currentUser is the current user's UID.
func currentUser() (string, error) { return strconv.Itoa(os.Getuid()), nil }
