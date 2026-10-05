//go:build !linux && !windows

package ipc

import (
	"net"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
)

// peerOf says nothing here: the standard library exposes no peer credential
// outside Linux, and getpeereid/LOCAL_PEERCRED would need x/sys, which the
// SDK bans (ADR 0148 §Deferred). The directory is the gate.
func peerOf(_ *net.UnixConn) coreipc.PeerValue { return coreipc.PeerValue{UID: -1, GID: -1} }
