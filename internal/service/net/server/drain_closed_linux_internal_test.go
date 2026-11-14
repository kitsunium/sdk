//go:build linux

// Package server — how TestDrain_requestHeardInTheDrainIsAnswered sees the
// listener close on Linux: by the descriptors this process holds, never by
// the network (#1385).
package server

import (
	"testing"
	"time"
)

// listenerCloseWatch reads, before the drain, the inodes of this process's
// listening sockets on addr, and returns a check that reports whether this
// process holds none of them any more: the drain's last taker closed them.
// A refused dial would answer only if the kernel answers the SYN, and the
// bazel job's host does not always: the four net suites side by side fill
// the conntrack table (#1377), whose drop of a SYN reads as no refusal at
// all, and a dial bounded at drainProbeDial times out on a starved CPU
// before it reads its refusal. /proc/self/fd is this process's alone, so
// its read neither waits on the network nor on the host's socket tables.
func listenerCloseWatch(t *testing.T, addr string) func() bool {
	t.Helper()
	want, own := procHexAddrPort(t, addr), ownSocketInodes(t)
	var inodes []string
	for _, s := range procSockets(t, "tcp") {
		if s.state == "0A" && s.local == want && own[s.inode] {
			inodes = append(inodes, s.inode)
		}
	}
	if len(inodes) == 0 {
		t.Fatalf("no listening socket of this process on %s", addr)
	}
	return func() bool {
		held := ownSocketInodes(t)
		for _, inode := range inodes {
			//: still held: a taker has yet to hand it back; read again in a
			//: millisecond.
			if held[inode] {
				time.Sleep(time.Millisecond)
				return false
			}
		}
		return true
	}
}
