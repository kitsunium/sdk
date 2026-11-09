// Package bsdabi_test — the records decoded from bytes laid out as each
// kernel's header declares them. The fixtures are not built from the
// decoder's offsets: every structure's prefix is transcribed below from its
// header, member by member, and laid out by cLayout under the C rules of the
// architecture's ABI, so a wrong offset in the decoder reads the wrong bytes.
package bsdabi_test

import (
	"encoding/binary"
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/service/proc/internal/bsdabi"
)

// cType is a C type as an ABI lays it out: its size and alignment, and the
// offsets of its own members when it is an aggregate.
type cType struct {
	size, align int
	members     map[string]int
}

// cMember is one member of a structure as its header declares it.
type cMember struct {
	name  string
	typ   cType
	count int
}

// scalar is a C scalar of size bytes and alignment align.
func scalar(size, align int) cType {
	return cType{size: size, align: align}
}

// m declares one member; count is 1 for a scalar, the length of an array.
func m(name string, typ cType, count ...int) cMember {
	n := 1
	if len(count) > 0 {
		n = count[0]
	}
	return cMember{name: name, typ: typ, count: n}
}

// cStruct lays members out under C's rules: each at the next multiple of its
// alignment, the structure aligned to its widest member and padded to it.
// Nested members are addressed "outer.inner".
func cStruct(members ...cMember) cType {
	out := cType{align: 1, members: map[string]int{}}
	off := 0
	for _, mb := range members {
		off = roundUp(off, mb.typ.align)
		out.members[mb.name] = off
		for inner, o := range mb.typ.members {
			out.members[mb.name+"."+inner] = off + o
		}
		off += mb.typ.size * mb.count
		out.align = max(out.align, mb.typ.align)
	}
	out.size = roundUp(off, out.align)
	return out
}

// cUnion overlays members at offset 0.
func cUnion(members ...cMember) cType {
	out := cType{align: 1, members: map[string]int{}}
	for _, mb := range members {
		out.members[mb.name] = 0
		for inner, o := range mb.typ.members {
			out.members[mb.name+"."+inner] = o
		}
		out.size = max(out.size, mb.typ.size*mb.count)
		out.align = max(out.align, mb.typ.align)
	}
	out.size = roundUp(out.size, out.align)
	return out
}

// roundUp rounds n up to a multiple of a.
func roundUp(n, a int) int {
	return (n + a - 1) / a * a
}

// abi is the C data model of one architecture.
type abi struct {
	ptr, long, timeT, i64 cType
}

var (
	// lp64 is amd64, arm64 and riscv64.
	lp64 = abi{ptr: scalar(8, 8), long: scalar(8, 8), timeT: scalar(8, 8), i64: scalar(8, 8)}
	// i386 aligns a 64-bit integer to 4, and FreeBSD's i386 time_t is 32 bits.
	i386 = abi{ptr: scalar(4, 4), long: scalar(4, 4), timeT: scalar(4, 4), i64: scalar(8, 4)}
	// armEABI aligns a 64-bit integer to 8, and FreeBSD's arm time_t is 64 bits.
	armEABI = abi{ptr: scalar(4, 4), long: scalar(4, 4), timeT: scalar(8, 8), i64: scalar(8, 8)}
	// The fixed-width scalars.
	i8, i16, i32 = scalar(1, 1), scalar(2, 2), scalar(4, 4)
)

// freeBSDKinfoProc is struct kinfo_proc of sys/sys/user.h, transcribed up to
// ki_start — every member before it, in order — for a.
func freeBSDKinfoProc(a abi) cType {
	timeval := cStruct(m("tv_sec", a.timeT), m("tv_usec", a.long))
	sigset := cStruct(m("__bits", i32, 4))
	return cStruct(
		m("ki_structsize", i32), m("ki_layout", i32),
		m("ki_args", a.ptr), m("ki_paddr", a.ptr), m("ki_addr", a.ptr), m("ki_tracep", a.ptr),
		m("ki_textvp", a.ptr), m("ki_fd", a.ptr), m("ki_vmspace", a.ptr), m("ki_wchan", a.ptr),
		m("ki_pid", i32), m("ki_ppid", i32), m("ki_pgid", i32), m("ki_tpgid", i32), m("ki_sid", i32), m("ki_tsid", i32),
		m("ki_jobc", i16), m("ki_spare_short1", i16), m("ki_tdev_freebsd11", i32),
		m("ki_siglist", sigset), m("ki_sigmask", sigset), m("ki_sigignore", sigset), m("ki_sigcatch", sigset),
		m("ki_uid", i32), m("ki_ruid", i32), m("ki_svuid", i32), m("ki_rgid", i32), m("ki_svgid", i32),
		m("ki_ngroups", i16), m("ki_spare_short2", i16), m("ki_groups", i32, 16),
		m("ki_size", a.ptr), m("ki_rssize", a.long), m("ki_swrss", a.long), m("ki_tsize", a.long),
		m("ki_dsize", a.long), m("ki_ssize", a.long),
		m("ki_xstat", i16), m("ki_acflag", i16), m("ki_pctcpu", i32), m("ki_estcpu", i32),
		m("ki_slptime", i32), m("ki_swtime", i32), m("ki_cow", i32), m("ki_runtime", a.i64),
		m("ki_start", timeval),
	)
}

// dragonFlyKinfoProc is struct kinfo_proc of sys/sys/kinfo.h (amd64),
// transcribed up to kp_pid.
func dragonFlyKinfoProc() cType {
	a := lp64
	timeval := cStruct(m("tv_sec", a.long), m("tv_usec", a.long))
	sigset := cStruct(m("__bits", i32, 4))
	return cStruct(
		m("kp_paddr", a.ptr), m("kp_flags", i32), m("kp_stat", i32), m("kp_lock", i32), m("kp_acflag", i32),
		m("kp_traceflag", i32), m("kp_fd", a.ptr),
		m("kp_siglist", sigset), m("kp_sigignore", sigset), m("kp_sigcatch", sigset), m("kp_sigflag", i32),
		m("kp_start", timeval), m("kp_comm", i8, 17),
		m("kp_uid", i32), m("kp_ngroups", i16), m("kp_groups", i32, 16),
		m("kp_ruid", i32), m("kp_svuid", i32), m("kp_rgid", i32), m("kp_svgid", i32), m("kp_pid", i32),
	)
}

// darwinKinfoProc is struct kinfo_proc of xnu's bsd/sys/sysctl.h — struct
// extern_proc (bsd/sys/proc.h), then struct eproc up to e_ucred — on LP64.
func darwinKinfoProc() cType {
	a := lp64
	timeval := cStruct(m("tv_sec", a.long), m("tv_usec", i32))
	itimerval := cStruct(m("it_interval", timeval), m("it_value", timeval))
	pun := cUnion(m("p_st1", cStruct(m("__p_forw", a.ptr), m("__p_back", a.ptr))), m("__p_starttime", timeval))
	externProc := cStruct(
		m("p_un", pun), m("p_vmspace", a.ptr), m("p_sigacts", a.ptr), m("p_flag", i32), m("p_stat", i8),
		m("p_pid", i32), m("p_oppid", i32), m("p_dupfd", i32), m("user_stack", a.ptr), m("exit_thread", a.ptr),
		m("p_debugger", i32), m("sigwait", i32), m("p_estcpu", i32), m("p_cpticks", i32), m("p_pctcpu", i32),
		m("p_wchan", a.ptr), m("p_wmesg", a.ptr), m("p_swtime", i32), m("p_slptime", i32),
		m("p_realtimer", itimerval), m("p_rtime", timeval),
		m("p_uticks", a.i64), m("p_sticks", a.i64), m("p_iticks", a.i64),
		m("p_traceflag", i32), m("p_tracep", a.ptr), m("p_siglist", i32), m("p_textvp", a.ptr), m("p_holdcnt", i32),
		m("p_sigmask", i32), m("p_sigignore", i32), m("p_sigcatch", i32),
		m("p_priority", i8), m("p_usrpri", i8), m("p_nice", i8), m("p_comm", i8, 17),
		m("p_pgrp", a.ptr), m("p_addr", a.ptr), m("p_xstat", i16), m("p_acflag", i16), m("p_ru", a.ptr),
	)
	pcred := cStruct(m("pc_lock", i8, 72), m("pc_ucred", a.ptr), m("p_ruid", i32), m("p_svuid", i32),
		m("p_rgid", i32), m("p_svgid", i32), m("p_refcnt", i32))
	ucred := cStruct(m("cr_ref", i32), m("cr_uid", i32), m("cr_ngroups", i16), m("cr_groups", i32, 16))
	eproc := cStruct(m("e_paddr", a.ptr), m("e_sess", a.ptr), m("e_pcred", pcred), m("e_ucred", ucred))
	return cStruct(m("kp_proc", externProc), m("kp_eproc", eproc))
}

// openBSDKinfoProc is struct kinfo_proc of sys/sys/sysctl.h, transcribed up
// to p_ustart_usec: fixed-width members, laid out with 64-bit integers
// aligned to i64's alignment.
func openBSDKinfoProc(i64 cType) cType {
	members := []cMember{}
	for _, n := range []string{
		"p_forw", "p_back", "p_paddr", "p_addr", "p_fd", "p_stats", "p_limit",
		"p_vmspace", "p_sigacts", "p_sess", "p_tsess", "p_ru",
	} {
		members = append(members, m(n, i64))
	}
	members = append(members,
		m("p_eflag", i32), m("p_exitsig", i32), m("p_flag", i32),
		m("p_pid", i32), m("p_ppid", i32), m("p_sid", i32), m("p__pgid", i32), m("p_tpgid", i32),
		m("p_uid", i32), m("p_ruid", i32), m("p_gid", i32), m("p_rgid", i32), m("p_groups", i32, 16),
		m("p_ngroups", i16), m("p_jobc", i16), m("p_tdev", i32),
		m("p_estcpu", i32), m("p_rtime_sec", i32), m("p_rtime_usec", i32), m("p_cpticks", i32),
		m("p_pctcpu", i32), m("p_swtime", i32), m("p_slptime", i32), m("p_schedflags", i32),
		m("p_uticks", i64), m("p_sticks", i64), m("p_iticks", i64), m("p_tracep", i64),
		m("p_traceflag", i32), m("p_holdcnt", i32), m("p_siglist", i32), m("p_sigmask", i32),
		m("p_sigignore", i32), m("p_sigcatch", i32),
		m("p_stat", i8), m("p_priority", i8), m("p_usrpri", i8), m("p_nice", i8),
		m("p_xstat", i16), m("p_spare", i16), m("p_comm", i8, 24), m("p_wmesg", i8, 8), m("p_wchan", i64),
		m("p_login", i8, 32), m("p_vm_rssize", i32), m("p_vm_tsize", i32), m("p_vm_dsize", i32),
		m("p_vm_ssize", i32), m("p_uvalid", i64), m("p_ustart_sec", i64), m("p_ustart_usec", i32),
	)
	return cStruct(members...)
}

// netBSDKinfoProc2 is struct kinfo_proc2 of sys/sys/sysctl.h, transcribed up
// to p_ustart_usec.
func netBSDKinfoProc2(i64 cType) cType {
	members := []cMember{}
	for _, n := range []string{
		"p_forw", "p_back", "p_paddr", "p_addr", "p_fd", "p_cwdi", "p_stats",
		"p_limit", "p_vmspace", "p_sigacts", "p_sess", "p_tsess", "p_ru",
	} {
		members = append(members, m(n, i64))
	}
	sigset := cStruct(m("__bits", i32, 4))
	members = append(members,
		m("p_eflag", i32), m("p_exitsig", i32), m("p_flag", i32),
		m("p_pid", i32), m("p_ppid", i32), m("p_sid", i32), m("p__pgid", i32), m("p_tpgid", i32),
		m("p_uid", i32), m("p_ruid", i32), m("p_gid", i32), m("p_rgid", i32), m("p_groups", i32, 16),
		m("p_ngroups", i16), m("p_jobc", i16), m("p_tdev", i32),
		m("p_estcpu", i32), m("p_rtime_sec", i32), m("p_rtime_usec", i32), m("p_cpticks", i32),
		m("p_pctcpu", i32), m("p_swtime", i32), m("p_slptime", i32), m("p_schedflags", i32),
		m("p_uticks", i64), m("p_sticks", i64), m("p_iticks", i64), m("p_tracep", i64),
		m("p_traceflag", i32), m("p_holdcnt", i32),
		m("p_siglist", sigset), m("p_sigmask", sigset), m("p_sigignore", sigset), m("p_sigcatch", sigset),
		m("p_stat", i8), m("p_priority", i8), m("p_usrpri", i8), m("p_nice", i8),
		m("p_xstat", i16), m("p_acflag", i16), m("p_comm", i8, 24), m("p_wmesg", i8, 8), m("p_wchan", i64),
		m("p_login", i8, 24), m("p_vm_rssize", i32), m("p_vm_tsize", i32), m("p_vm_dsize", i32),
		m("p_vm_ssize", i32), m("p_uvalid", i64), m("p_ustart_sec", i32), m("p_ustart_usec", i32),
	)
	return cStruct(members...)
}

// recordCase is one kernel's record: its layout from the header, the sysctl
// name and record size the header gives, and where each value lies.
type recordCase struct {
	goos, goarch string
	st           cType
	// size is the record as the kernel copies it out (KINFO_PROC_SIZE,
	// sizeof, or the prefix asked); mibTail the name after the pid.
	size                       int
	mibOp                      int32
	mibTail                    []int32
	structSize                 string
	pid, ruid, euid, sec, usec string
	secWidth, usecWidth        int
}

// recordCases are the layouts ProcRecord must read, one per ABI.
func recordCases() []recordCase {
	fbsd := func(goarch string, a abi, size, secW int) recordCase {
		return recordCase{
			goos: "freebsd", goarch: goarch, st: freeBSDKinfoProc(a), size: size, mibOp: 14,
			structSize: "ki_structsize", pid: "ki_pid", ruid: "ki_ruid", euid: "ki_uid",
			sec: "ki_start.tv_sec", usec: "ki_start.tv_usec", secWidth: secW, usecWidth: a.long.size,
		}
	}
	openbsd := func(goarch string) recordCase {
		return recordCase{
			goos: "openbsd", goarch: goarch, st: openBSDKinfoProc(lp64.i64), size: 424, mibOp: 66, mibTail: []int32{424, 1},
			pid: "p_pid", ruid: "p_ruid", euid: "p_uid", sec: "p_ustart_sec", usec: "p_ustart_usec", secWidth: 8, usecWidth: 4,
		}
	}
	netbsd := func(goarch string) recordCase {
		return recordCase{
			goos: "netbsd", goarch: goarch, st: netBSDKinfoProc2(lp64.i64), size: 464, mibOp: 47, mibTail: []int32{464, 1},
			pid: "p_pid", ruid: "p_ruid", euid: "p_uid", sec: "p_ustart_sec", usec: "p_ustart_usec", secWidth: 4, usecWidth: 4,
		}
	}
	darwin := func(goarch string) recordCase {
		return recordCase{
			goos: "darwin", goarch: goarch, st: darwinKinfoProc(), size: 648, mibOp: 14,
			pid: "kp_proc.p_pid", ruid: "kp_eproc.e_pcred.p_ruid", euid: "kp_eproc.e_ucred.cr_uid",
			sec: "kp_proc.p_un.__p_starttime.tv_sec", usec: "kp_proc.p_un.__p_starttime.tv_usec", secWidth: 8, usecWidth: 4,
		}
	}
	return []recordCase{
		fbsd("amd64", lp64, 1088, 8), fbsd("arm64", lp64, 1088, 8), fbsd("riscv64", lp64, 1088, 8),
		fbsd("386", i386, 768, 4), fbsd("arm", armEABI, 816, 8),
		{
			goos: "dragonfly", goarch: "amd64", st: dragonFlyKinfoProc(), size: 1200, mibOp: 14,
			pid: "kp_pid", ruid: "kp_ruid", euid: "kp_uid", sec: "kp_start.tv_sec", usec: "kp_start.tv_usec", secWidth: 8, usecWidth: 8,
		},
		darwin("amd64"), darwin("arm64"),
		openbsd("amd64"), openbsd("arm64"), openbsd("386"), openbsd("riscv64"),
		netbsd("amd64"), netbsd("arm64"), netbsd("386"), netbsd("arm"),
	}
}

// put writes v, width bytes wide, at off.
func put(buf []byte, off, width int, v uint64) {
	if width == 8 {
		binary.NativeEndian.PutUint64(buf[off:], v)
		return
	}
	binary.NativeEndian.PutUint32(buf[off:], uint32(v))
}

// fixture is c's record for pid, users 1001/1002 and a start of
// 1700000000.123456 s, every other byte 0xA5.
func fixture(t *testing.T, c recordCase, pid int) []byte {
	t.Helper()
	buf := make([]byte, c.size)
	for i := range buf {
		buf[i] = 0xA5
	}
	for _, name := range []string{c.pid, c.ruid, c.euid, c.sec, c.usec} {
		if _, ok := c.st.members[name]; !ok {
			t.Fatalf("%s/%s: the transcription has no member %s", c.goos, c.goarch, name)
		}
	}
	if c.structSize != "" {
		put(buf, c.st.members[c.structSize], 4, uint64(c.size))
	}
	put(buf, c.st.members[c.pid], 4, uint64(pid))
	put(buf, c.st.members[c.ruid], 4, 1001)
	put(buf, c.st.members[c.euid], 4, 1002)
	put(buf, c.st.members[c.sec], c.secWidth, 1700000000)
	put(buf, c.st.members[c.usec], c.usecWidth, 123456)
	return buf
}

// TestProcRecordReadsTheHeadersLayout pins every layout against its header:
// the record built at the transcription's offsets decodes to its values, and
// the sysctl name is the header's.
func TestProcRecordReadsTheHeadersLayout(t *testing.T) {
	t.Parallel()
	const pid = 4242
	for _, c := range recordCases() {
		t.Run(c.goos+"/"+c.goarch, func(t *testing.T) {
			t.Parallel()
			if c.mibTail != nil && c.size != c.st.size {
				t.Fatalf("the prefix asked is %d bytes, the transcription up to p_ustart_usec %d", c.size, c.st.size)
			}
			mib, size, ok := bsdabi.ProcRequest(c.goos, c.goarch, pid)
			want := append([]int32{1, c.mibOp, 1, pid}, c.mibTail...)
			if !ok || !slices.Equal(mib, want) {
				t.Fatalf("ProcRequest = %v, %d, %v, want %v", mib, size, ok, want)
			}
			if c.goos != "dragonfly" && size != c.size {
				t.Fatalf("ProcRequest size %d, want the header's %d", size, c.size)
			}
			if c.goos == "dragonfly" && size < c.size {
				t.Fatalf("ProcRequest size %d holds no record of %d", size, c.size)
			}
			start, ruid, euid, ok := bsdabi.ProcRecord(c.goos, c.goarch, pid, fixture(t, c, pid))
			if !ok || start != 1700000000*1_000_000+123456 || ruid != 1001 || euid != 1002 {
				t.Fatalf("ProcRecord = %d, %d, %d, %v; want 1700000000123456, 1001, 1002", start, ruid, euid, ok)
			}
		})
	}
}

// TestProcRecordRefusals pins what is never misread: another process's
// record, a truncated or longer one, a FreeBSD record stating another size, a
// kernel or an architecture without a layout, a pid outside pid_t.
func TestProcRecordRefusals(t *testing.T) {
	t.Parallel()
	const pid = 4242
	for _, c := range recordCases() {
		good := fixture(t, c, pid)
		if _, _, _, ok := bsdabi.ProcRecord(c.goos, c.goarch, pid+1, good); ok {
			t.Errorf("%s/%s: another pid's record accepted", c.goos, c.goarch)
		}
		if _, _, _, ok := bsdabi.ProcRecord(c.goos, c.goarch, pid, good[:c.st.members[c.pid]]); ok {
			t.Errorf("%s/%s: a record cut before its pid accepted", c.goos, c.goarch)
		}
		if c.goos != "dragonfly" {
			if _, _, _, ok := bsdabi.ProcRecord(c.goos, c.goarch, pid, append(slices.Clone(good), 0)); ok {
				t.Errorf("%s/%s: a record one byte too long accepted", c.goos, c.goarch)
			}
		}
		if c.structSize != "" {
			bad := slices.Clone(good)
			put(bad, c.st.members[c.structSize], 4, uint64(c.size+8))
			if _, _, _, ok := bsdabi.ProcRecord(c.goos, c.goarch, pid, bad); ok {
				t.Errorf("%s/%s: a record stating another size accepted", c.goos, c.goarch)
			}
		}
	}
	for _, p := range [][2]string{
		{"linux", "amd64"},
		{"illumos", "amd64"},
		{"solaris", "amd64"},
		{"windows", "amd64"},
		{"freebsd", "mips64"},
		{"dragonfly", "arm64"},
		{"darwin", "386"},
	} {
		if _, _, ok := bsdabi.ProcRequest(p[0], p[1], pid); ok {
			t.Errorf("ProcRequest(%s/%s) has a layout", p[0], p[1])
		}
		if _, _, _, ok := bsdabi.ProcRecord(p[0], p[1], pid, make([]byte, 4096)); ok {
			t.Errorf("ProcRecord(%s/%s) decoded a record", p[0], p[1])
		}
	}
	bads := []int{0, -1}
	if strconv.IntSize == 64 {
		past := int64(math.MaxInt32)
		past++
		bads = append(bads, int(past))
	}
	for _, bad := range bads {
		if _, _, ok := bsdabi.ProcRequest("freebsd", "amd64", bad); ok {
			t.Errorf("ProcRequest(pid %d) asked", bad)
		}
	}
}
