package bsdabi

import "encoding/binary"

// FreeBSD's struct sockcred2 (sys/sys/socket.h), the data of the SCM_CREDS2
// control message LOCAL_CREDS_PERSISTENT attaches to every datagram: seven
// 32-bit fields — sc_version, sc_pid, sc_uid, sc_euid, sc_gid, sc_egid,
// sc_ngroups — then sc_ngroups group ids. Its size is SOCKCRED2SIZE(n),
// sizeof(struct sockcred2) + 4·(n − 1) = 28 + 4·n, and the kernel sends at
// most CMGROUP_MAX groups (uipc_usrreq.c, unp_addsockcred).
const (
	// sockcred2Version is sc_version's offset; the kernel writes 0.
	sockcred2Version int = 0
	// sockcred2Pid is sc_pid's offset.
	sockcred2Pid int = 4
	// sockcred2UID is sc_uid's (real) offset.
	sockcred2UID int = 8
	// sockcred2GID is sc_gid's (real) offset.
	sockcred2GID int = 16
	// sockcred2NGroups is sc_ngroups' offset.
	sockcred2NGroups int = 24
	// sockcred2Fixed is the size of the seven fields before the groups.
	sockcred2Fixed int = 28
	// sockcred2KnownVersion is the one sc_version this package reads.
	sockcred2KnownVersion int64 = 0
	// cmGroupMax is CMGROUP_MAX (sys/socket.h): the most groups sent.
	cmGroupMax int64 = 16
)

// Sockcred2 decodes the data of the SCM_CREDS2 control message FreeBSD's
// LOCAL_CREDS_PERSISTENT attaches to every datagram (struct sockcred2,
// sys/socket.h): the sender's PID and its real user and group, as the kernel
// wrote them. ok is false for data shorter than the structure and the groups
// it counts, a group count past CMGROUP_MAX, a version other than 0, or a PID
// below 1.
func Sockcred2(data []byte) (pid, uid, gid int, ok bool) {
	//: not even the fixed fields.
	if len(data) < sockcred2Fixed {
		//: refused.
		return 0, 0, 0, false
	}
	n := readInt32(data, sockcred2NGroups)
	sender := readInt32(data, sockcred2Pid)
	//: another version, a count no kernel sends, groups cut off, or no sender.
	if readInt32(data, sockcred2Version) != sockcred2KnownVersion || n < 0 || n > cmGroupMax ||
		len(data) < sockcred2Fixed+int(n)*int32Bytes || sender < 1 {
		//: refused, never misread.
		return 0, 0, 0, false
	}
	//: the sender, its real user and group.
	return int(sender), int(binary.NativeEndian.Uint32(data[sockcred2UID:])), int(binary.NativeEndian.Uint32(data[sockcred2GID:])), true
}
