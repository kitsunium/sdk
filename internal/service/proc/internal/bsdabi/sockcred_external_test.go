package bsdabi_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/proc/internal/bsdabi"
)

// sockcred2 is struct sockcred2 of FreeBSD's sys/sys/socket.h, transcribed
// member by member; its size with n groups is SOCKCRED2SIZE(n),
// sizeof(struct sockcred2) + sizeof(gid_t)·(n − 1).
var sockcred2 = cStruct(
	m("sc_version", i32), m("sc_pid", i32), m("sc_uid", i32), m("sc_euid", i32),
	m("sc_gid", i32), m("sc_egid", i32), m("sc_ngroups", i32), m("sc_groups", i32, 1),
)

// sockcred2Data is the control data the kernel writes (uipc_usrreq.c,
// unp_addsockcred): version 0, the sender, its real and effective ids, then
// n groups.
func sockcred2Data(version, pid, ngroups int) []byte {
	size := sockcred2.size + 4*(ngroups-1)
	buf := make([]byte, max(size, sockcred2.members["sc_groups"]))
	put(buf, sockcred2.members["sc_version"], 4, uint64(version))
	put(buf, sockcred2.members["sc_pid"], 4, uint64(pid))
	put(buf, sockcred2.members["sc_uid"], 4, 1001)
	put(buf, sockcred2.members["sc_euid"], 4, 2002)
	put(buf, sockcred2.members["sc_gid"], 4, 3003)
	put(buf, sockcred2.members["sc_egid"], 4, 4004)
	put(buf, sockcred2.members["sc_ngroups"], 4, uint64(uint32(int32(ngroups))))
	for i := range max(ngroups, 0) {
		put(buf, sockcred2.members["sc_groups"]+4*i, 4, uint64(5000+i))
	}
	return buf
}

// TestSockcred2 pins the decoding of SCM_CREDS2's data, built as the header
// lays struct sockcred2 out: the sender's pid and its REAL user and group —
// the ids Linux's SCM_CREDENTIALS carries by default —, never the effective
// ones; and the refusals of data that is not that structure.
func TestSockcred2(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 1, 16} {
		pid, uid, gid, ok := bsdabi.Sockcred2(sockcred2Data(0, 4242, n))
		if !ok || pid != 4242 || uid != 1001 || gid != 3003 {
			t.Errorf("%d groups: Sockcred2 = %d, %d, %d, %v; want 4242, 1001, 3003", n, pid, uid, gid, ok)
		}
	}
	type tc struct {
		name string
		data []byte
	}
	full := sockcred2Data(0, 4242, 3)
	tests := []tc{
		{"empty", nil},
		{"shorter than the fixed fields", full[:27]},
		{"its groups cut off", full[:len(full)-1]},
		{"version 1", sockcred2Data(1, 4242, 0)},
		{"17 groups", sockcred2Data(0, 4242, 17)},
		{"a negative count", append(sockcred2Data(0, 4242, -1), make([]byte, 8)...)},
		{"pid 0", sockcred2Data(0, 0, 0)},
	}
	for _, c := range tests {
		if pid, _, _, ok := bsdabi.Sockcred2(c.data); ok {
			t.Errorf("%s: Sockcred2 accepted pid %d", c.name, pid)
		}
	}
}
