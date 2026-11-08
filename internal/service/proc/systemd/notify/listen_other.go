//go:build !linux && !freebsd

package notify

import (
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Listen returns UnsupportedPlatform off Linux and FreeBSD: the credential
// check the listener relies on — the kernel stamping every datagram with its
// sender (SO_PASSCRED, LOCAL_CREDS_PERSISTENT) — is theirs alone. The notifier
// side (Notify and its shorthands) and WatchdogInterval remain available on
// every platform.
func Listen() (l coreproc.Listener, socketPath string, err error) {
	//: the supervisor side needs SO_PASSCRED or LOCAL_CREDS_PERSISTENT: Linux and FreeBSD only.
	return nil, "", coreproc.UnsupportedPlatform
}

// ListenFor returns UnsupportedPlatform off Linux and FreeBSD, as Listen does.
func ListenFor(_, _ int) (l coreproc.Listener, socketPath string, err error) {
	//: the supervisor side needs SO_PASSCRED or LOCAL_CREDS_PERSISTENT: Linux and FreeBSD only.
	return nil, "", coreproc.UnsupportedPlatform
}

// Detach returns UnsupportedPlatform off Linux and FreeBSD, as Listen does.
func Detach(_ coreproc.Listener) (f *os.File, err error) {
	//: no listener to detach here.
	return nil, coreproc.UnsupportedPlatform
}

// Adopt returns UnsupportedPlatform off Linux and FreeBSD, as Listen does.
func Adopt(_ *os.File, _ string) (l coreproc.Listener, err error) {
	//: no listener to adopt here.
	return nil, coreproc.UnsupportedPlatform
}
