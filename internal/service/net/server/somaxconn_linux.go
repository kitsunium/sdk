//go:build linux

package server

import (
	"os"
	"strconv"
	"strings"
)

// hostSomaxconn is /proc/sys/net/core/somaxconn, the clamp of every listen
// backlog; zero when it cannot be read.
func hostSomaxconn() int {
	raw, err := os.ReadFile("/proc/sys/net/core/somaxconn")
	//: the standard library falls back on its own constant; the report says
	//: nothing rather than guess.
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	//: unreadable counts as unknown.
	if err != nil || n < 0 {
		return 0
	}
	return n
}
