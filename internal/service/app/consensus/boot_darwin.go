package consensus

import (
	"encoding/hex"
	"syscall"
)

// bootID reads kern.boottime: a reboot changes it.
func bootID() string {
	v, err := syscall.Sysctl("kern.boottime")
	if err != nil {
		return ""
	}
	return hex.EncodeToString([]byte(v))
}
