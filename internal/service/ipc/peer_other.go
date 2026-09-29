//go:build !linux && !windows

// Package ipc — no peer credentials outside Linux without x/sys: the peer is
// unverified and the directory is the gate.
package ipc

import "net"

// peerOf says nothing here: the standard library exposes no peer credential
// outside Linux, and getpeereid/LOCAL_PEERCRED would need x/sys, which the
// SDK bans (ADR 0148 §Deferred). The directory is the gate.
func peerOf(*net.UnixConn) PeerValue { return PeerValue{UID: -1, GID: -1} }
