package ipc

import (
	"testing"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The admission rule, on peers no test process can be: another user, an
// allowed user, a member of an allowed group — by its effective group or, since
// #1092, by a supplementary group the kernel recorded —, and the unverified
// peer the directory alone admitted.
func TestAdmit(t *testing.T) {
	cfg := Config{AllowUIDs: []int{2000}, AllowGIDs: []int{3000}}
	for _, c := range []struct {
		peer   coreipc.PeerValue
		groups []int
		ok     bool
	}{
		{coreipc.PeerValue{UID: 1000, GID: 1000, Verified: true}, nil, true},
		{coreipc.PeerValue{UID: 2000, GID: 9, Verified: true}, nil, true},
		{coreipc.PeerValue{UID: 7, GID: 3000, Verified: true}, nil, true},
		{coreipc.PeerValue{UID: 7, GID: 9, Verified: true}, []int{9, 4000, 3000}, true},
		{coreipc.PeerValue{UID: 7, GID: 9, Verified: true}, []int{9, 4000}, false},
		{coreipc.PeerValue{UID: 7, GID: 9, Verified: true}, nil, false},
		{coreipc.PeerValue{UID: -1, GID: -1}, nil, !kernelNamesPeer},
	} {
		err := admit(c.peer, c.groups, 1000, &cfg)
		if c.ok != (err == nil) || (err != nil && !errs.HasCode(err, coreipc.CodePeerRefused)) {
			t.Errorf("admit(%+v, groups %v) = %v, want ok=%v", c.peer, c.groups, err, c.ok)
		}
	}
}

// TestAdmitAnUnnamedPeer pins #1093: where the kernel names the peer, a peer
// it did not name is refused with the reason; where it does not, the
// directory admitted it — groups or none.
func TestAdmitAnUnnamedPeer(t *testing.T) {
	cfg := Config{AllowGIDs: []int{3000}}
	unnamed := coreipc.PeerValue{UID: -1, GID: -1}
	if err := admitWhere(unnamed, []int{3000}, 1000, &cfg, true); !errs.HasCode(err, coreipc.CodePeerRefused) {
		t.Fatalf("an unnamed peer where the kernel names peers = %v, want PEER_REFUSED", err)
	}
	if err := admitWhere(unnamed, nil, 1000, &cfg, false); err != nil {
		t.Fatalf("an unnamed peer where the directory is the gate = %v, want nil", err)
	}
}
