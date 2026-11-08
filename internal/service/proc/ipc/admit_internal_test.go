package ipc

import (
	"testing"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The admission rule, on peers no test process can be: another user, an
// allowed user, a member of an allowed group, and the unverified peer the
// directory alone admitted.
func TestAdmit(t *testing.T) {
	cfg := Config{AllowUIDs: []int{2000}, AllowGIDs: []int{3000}}
	for _, c := range []struct {
		peer coreipc.PeerValue
		ok   bool
	}{
		{coreipc.PeerValue{UID: 1000, GID: 1000, Verified: true}, true},
		{coreipc.PeerValue{UID: 2000, GID: 9, Verified: true}, true},
		{coreipc.PeerValue{UID: 7, GID: 3000, Verified: true}, true},
		{coreipc.PeerValue{UID: 7, GID: 9, Verified: true}, false},
		{coreipc.PeerValue{UID: -1, GID: -1}, !kernelNamesPeer},
	} {
		err := admit(c.peer, 1000, &cfg)
		if c.ok != (err == nil) || (err != nil && !errs.HasCode(err, coreipc.CodePeerRefused)) {
			t.Errorf("admit(%+v) = %v, want ok=%v", c.peer, err, c.ok)
		}
	}
}

// TestAdmitAnUnnamedPeer pins #1093: where the kernel names the peer, a peer
// it did not name is refused with the reason; where it does not, the
// directory admitted it.
func TestAdmitAnUnnamedPeer(t *testing.T) {
	cfg := Config{}
	unnamed := coreipc.PeerValue{UID: -1, GID: -1}
	if err := admitWhere(unnamed, 1000, &cfg, true); !errs.HasCode(err, coreipc.CodePeerRefused) {
		t.Fatalf("an unnamed peer where the kernel names peers = %v, want PEER_REFUSED", err)
	}
	if err := admitWhere(unnamed, 1000, &cfg, false); err != nil {
		t.Fatalf("an unnamed peer where the directory is the gate = %v, want nil", err)
	}
}
