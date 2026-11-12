//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package server

// hostSomaxconn is unknown: Windows' backlog is SOMAXCONN_HINT's, and illumos
// and Solaris name it in a tunable this engine does not read.
func hostSomaxconn() int { return 0 }
