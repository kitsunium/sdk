//go:build !windows

package kit

import (
	"os"
	"strconv"
)

// currentUser is the current user's UID.
func currentUser() (string, error) { return strconv.Itoa(os.Getuid()), nil }
