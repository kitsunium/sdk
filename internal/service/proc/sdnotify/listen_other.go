//go:build !linux

// Package sdnotify — the supervisor (listener) side off Linux: unsupported.
package sdnotify

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// Listen returns UnsupportedPlatform on non-Linux platforms: the credential
// check the listener relies on (SO_PASSCRED / SCM_CREDENTIALS) is a Linux
// facility. The notifier side (Notify and its shorthands) and WatchdogInterval
// remain available on every platform.
func Listen() (l coreproc.Listener, socketPath string, err error) {
	//: the supervisor side needs SO_PASSCRED, which only Linux provides.
	return nil, "", coreproc.UnsupportedPlatform
}
