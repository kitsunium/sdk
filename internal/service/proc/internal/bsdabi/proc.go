package bsdabi

import (
	"encoding/binary"
	"math"
)

// The sysctl(3) names a process record is asked by (sys/sysctl.h of each
// kernel): CTL_KERN, then KERN_PROC on FreeBSD, DragonFly and darwin (14),
// KERN_PROC on OpenBSD (66), KERN_PROC2 on NetBSD (47), then KERN_PROC_PID (1).
const (
	// ctlKern is CTL_KERN.
	ctlKern int32 = 1
	// kernProc is KERN_PROC on FreeBSD, DragonFly and darwin.
	kernProc int32 = 14
	// kernProcOpenBSD is KERN_PROC on OpenBSD.
	kernProcOpenBSD int32 = 66
	// kernProc2 is KERN_PROC2 on NetBSD.
	kernProc2 int32 = 47
	// kernProcPID is KERN_PROC_PID on every one of them.
	kernProcPID int32 = 1
	// oneRecord is the element count OpenBSD and NetBSD take after the size.
	oneRecord int32 = 1
)

// Widths and units.
const (
	// int32Bytes is the width of a pid_t, a uid_t and a structure size.
	int32Bytes int = 4
	// int64Bytes is the width of a 64-bit field.
	int64Bytes int = 8
	// usecPerSec converts the seconds of a start time to microseconds.
	usecPerSec int64 = 1_000_000
	// noStructSize marks a record without a field stating its own size.
	noStructSize int = -1
)

// FreeBSD's struct kinfo_proc (sys/sys/user.h): its size is KINFO_PROC_SIZE
// (machine/proc.h), stated again in ki_structsize at offset 0. The offsets are
// the header's under each ABI: LP64 (amd64, arm64, riscv64); i386, whose
// time_t has 32 bits; arm, whose time_t has 64 bits aligned to 8.
const (
	// freeBSDStructSize is ki_structsize's offset.
	freeBSDStructSize int = 0
	// freeBSDLP64Size is KINFO_PROC_SIZE on amd64, arm64 and riscv.
	freeBSDLP64Size int = 1088
	// freeBSDLP64Pid is ki_pid's offset on LP64.
	freeBSDLP64Pid int = 72
	// freeBSDLP64Euid is ki_uid's (effective) offset on LP64.
	freeBSDLP64Euid int = 168
	// freeBSDLP64Ruid is ki_ruid's offset on LP64.
	freeBSDLP64Ruid int = 172
	// freeBSDLP64Sec is ki_start.tv_sec's offset on LP64 (64 bits).
	freeBSDLP64Sec int = 336
	// freeBSDLP64Usec is ki_start.tv_usec's offset on LP64 (64 bits).
	freeBSDLP64Usec int = 344
	// freeBSD386Size is KINFO_PROC_SIZE on i386.
	freeBSD386Size int = 768
	// freeBSDArmSize is KINFO_PROC_SIZE on arm.
	freeBSDArmSize int = 816
	// freeBSD32Pid is ki_pid's offset on i386 and arm.
	freeBSD32Pid int = 40
	// freeBSD32Euid is ki_uid's offset on i386 and arm.
	freeBSD32Euid int = 136
	// freeBSD32Ruid is ki_ruid's offset on i386 and arm.
	freeBSD32Ruid int = 140
	// freeBSD32Sec is ki_start.tv_sec's offset on i386 (32 bits) and arm (64).
	freeBSD32Sec int = 280
	// freeBSD386Usec is ki_start.tv_usec's offset on i386 (32 bits).
	freeBSD386Usec int = 284
	// freeBSDArmUsec is ki_start.tv_usec's offset on arm (32 bits).
	freeBSDArmUsec int = 288
)

// DragonFly's struct kinfo_proc (sys/sys/kinfo.h), amd64, the only
// architecture DragonFly runs on. The record has grown between releases
// (kp_syscaps), so the buffer is generous and the prefix read is checked by
// its pid.
const (
	// dragonFlyBuffer holds the whole record.
	dragonFlyBuffer int = 4096
	// dragonFlyPid is kp_pid's offset.
	dragonFlyPid int = 220
	// dragonFlyEuid is kp_uid's (effective) offset.
	dragonFlyEuid int = 132
	// dragonFlyRuid is kp_ruid's offset.
	dragonFlyRuid int = 204
	// dragonFlySec is kp_start.tv_sec's offset (64 bits).
	dragonFlySec int = 96
	// dragonFlyUsec is kp_start.tv_usec's offset (64 bits).
	dragonFlyUsec int = 104
)

// darwin's struct kinfo_proc (sys/sysctl.h): struct extern_proc, then struct
// eproc, on its two 64-bit architectures.
const (
	// darwinSize is sizeof(struct kinfo_proc).
	darwinSize int = 648
	// darwinPid is kp_proc.p_pid's offset.
	darwinPid int = 40
	// darwinEuid is kp_eproc.e_ucred.cr_uid's (effective) offset.
	darwinEuid int = 420
	// darwinRuid is kp_eproc.e_pcred.p_ruid's offset.
	darwinRuid int = 392
	// darwinSec is kp_proc.p_starttime.tv_sec's offset (64 bits).
	darwinSec int = 0
	// darwinUsec is kp_proc.p_starttime.tv_usec's offset (32 bits).
	darwinUsec int = 8
)

// OpenBSD's struct kinfo_proc (sys/sys/sysctl.h), fixed-width fields laid out
// alike on every architecture. The element size asked is the prefix read,
// rounded to 8: the kernel copies min(asked, sizeof) (kern_sysctl.c,
// sysctl_doproc).
const (
	// openBSDPrefix is the element size asked.
	openBSDPrefix int = 424
	// openBSDPid is p_pid's offset.
	openBSDPid int = 108
	// openBSDEuid is p_uid's (effective) offset.
	openBSDEuid int = 128
	// openBSDRuid is p_ruid's offset.
	openBSDRuid int = 132
	// openBSDSec is p_ustart_sec's offset (64 bits).
	openBSDSec int = 408
	// openBSDUsec is p_ustart_usec's offset (32 bits).
	openBSDUsec int = 416
)

// NetBSD's struct kinfo_proc2 (sys/sys/sysctl.h), fixed-width fields laid out
// alike on every architecture. The element size asked is the prefix read,
// rounded to 8: the kernel copies min(asked, sizeof) (kern_proc.c,
// sysctl_doeproc).
const (
	// netBSDPrefix is the element size asked.
	netBSDPrefix int = 464
	// netBSDPid is p_pid's offset.
	netBSDPid int = 116
	// netBSDEuid is p_uid's (effective) offset.
	netBSDEuid int = 136
	// netBSDRuid is p_ruid's offset.
	netBSDRuid int = 140
	// netBSDSec is p_ustart_sec's offset (32 bits).
	netBSDSec int = 456
	// netBSDUsec is p_ustart_usec's offset (32 bits).
	netBSDUsec int = 460
)

// field is one integer of a record: its offset and its width in bytes.
type field struct {
	// off is the offset from the record's start.
	off int
	// width is 4 or 8.
	width int
}

// procLayout is where one kernel's process record keeps what ProcRecord
// reads: the record's size, a structure-size field to check it by, the pid
// to check it against, the users and the start time.
type procLayout struct {
	// op is the second name of the sysctl: KERN_PROC or KERN_PROC2.
	op int32
	// sized is true where the name ends with the element size and count.
	sized bool
	// size is the buffer the record fills.
	size int
	// exact is true where the record is exactly size bytes; false where it is
	// at least the prefix read (DragonFly).
	exact bool
	// structSize is the offset of a field holding the record's own size,
	// noStructSize when there is none.
	structSize int
	// pid, ruid and euid are 32-bit fields.
	pid, ruid, euid int
	// sec and usec are the start time's halves.
	sec, usec field
}

var (
	// freeBSDLP64 is struct kinfo_proc on amd64, arm64 and riscv64.
	freeBSDLP64 = procLayout{
		op: kernProc, size: freeBSDLP64Size, exact: true, structSize: freeBSDStructSize,
		pid: freeBSDLP64Pid, ruid: freeBSDLP64Ruid, euid: freeBSDLP64Euid,
		sec: field{freeBSDLP64Sec, int64Bytes}, usec: field{freeBSDLP64Usec, int64Bytes},
	}
	// freeBSD386 is struct kinfo_proc on i386.
	freeBSD386 = procLayout{
		op: kernProc, size: freeBSD386Size, exact: true, structSize: freeBSDStructSize,
		pid: freeBSD32Pid, ruid: freeBSD32Ruid, euid: freeBSD32Euid,
		sec: field{freeBSD32Sec, int32Bytes}, usec: field{freeBSD386Usec, int32Bytes},
	}
	// freeBSDArm is struct kinfo_proc on arm.
	freeBSDArm = procLayout{
		op: kernProc, size: freeBSDArmSize, exact: true, structSize: freeBSDStructSize,
		pid: freeBSD32Pid, ruid: freeBSD32Ruid, euid: freeBSD32Euid,
		sec: field{freeBSD32Sec, int64Bytes}, usec: field{freeBSDArmUsec, int32Bytes},
	}
	// dragonFly is struct kinfo_proc's prefix on amd64.
	dragonFly = procLayout{
		op: kernProc, size: dragonFlyBuffer, structSize: noStructSize,
		pid: dragonFlyPid, ruid: dragonFlyRuid, euid: dragonFlyEuid,
		sec: field{dragonFlySec, int64Bytes}, usec: field{dragonFlyUsec, int64Bytes},
	}
	// darwin is struct kinfo_proc on amd64 and arm64.
	darwin = procLayout{
		op: kernProc, size: darwinSize, exact: true, structSize: noStructSize,
		pid: darwinPid, ruid: darwinRuid, euid: darwinEuid,
		sec: field{darwinSec, int64Bytes}, usec: field{darwinUsec, int32Bytes},
	}
	// openBSD is struct kinfo_proc's prefix on every architecture.
	openBSD = procLayout{
		op: kernProcOpenBSD, sized: true, size: openBSDPrefix, exact: true, structSize: noStructSize,
		pid: openBSDPid, ruid: openBSDRuid, euid: openBSDEuid,
		sec: field{openBSDSec, int64Bytes}, usec: field{openBSDUsec, int32Bytes},
	}
	// netBSD is struct kinfo_proc2's prefix on every architecture.
	netBSD = procLayout{
		op: kernProc2, sized: true, size: netBSDPrefix, exact: true, structSize: noStructSize,
		pid: netBSDPid, ruid: netBSDRuid, euid: netBSDEuid,
		sec: field{netBSDSec, int32Bytes}, usec: field{netBSDUsec, int32Bytes},
	}
)

// ProcRequest returns the sysctl(3) name that asks the kernel of goos for the
// record of process pid — kern.proc.pid's struct kinfo_proc on FreeBSD,
// DragonFly and darwin, KERN_PROC's on OpenBSD and KERN_PROC2's struct
// kinfo_proc2 on NetBSD, each of those two asked for the prefix ProcRecord
// reads — and the size of the buffer it fills. ok is false for a pid outside
// pid_t's range and for a goos and goarch whose layout this package does not
// hold.
func ProcRequest(goos, goarch string, pid int) (mib []int32, size int, ok bool) {
	l := layoutOf(goos, goarch)
	//: a kernel this package cannot read, or no pid_t.
	if l == nil || pid < 1 || pid > math.MaxInt32 {
		//: nothing to ask.
		return nil, 0, false
	}
	mib = []int32{ctlKern, l.op, kernProcPID, int32(pid)}
	//: OpenBSD and NetBSD take the element's size and count.
	if l.sized {
		mib = append(mib, int32(l.size), oneRecord)
	}
	//: the name and the buffer.
	return mib, l.size, true
}

// ProcRecord decodes the record ProcRequest asked for: the process's start
// time, in microseconds since the epoch as its kernel states it, and its real
// and effective users. ok is false when buf is not that record as the layout
// of goos and goarch lays it out — shorter than it, of another size than the
// record states, or naming another process than pid.
func ProcRecord(goos, goarch string, pid int, buf []byte) (start int64, ruid, euid int, ok bool) {
	l := layoutOf(goos, goarch)
	//: a kernel this package cannot read, a record of another size, or of
	//: another process.
	if l == nil || !l.fits(buf) || readInt32(buf, l.pid) != int64(pid) {
		//: refused, never misread.
		return 0, 0, 0, false
	}
	start = int64(readField(buf, l.sec))*usecPerSec + int64(readField(buf, l.usec))
	//: the start and the two users.
	return start, int(binary.NativeEndian.Uint32(buf[l.ruid:])), int(binary.NativeEndian.Uint32(buf[l.euid:])), true
}

// fits reports whether buf is a whole record of l: its size, and the size it
// states where it states one.
func (l *procLayout) fits(buf []byte) bool {
	//: DragonFly: at least the prefix read.
	if !l.exact {
		//: every field read lies in buf.
		return len(buf) >= l.end()
	}
	//: exactly the record, and the record it says it is.
	return len(buf) == l.size && (l.structSize == noStructSize || readInt32(buf, l.structSize) == int64(l.size))
}

// end is the offset just past the farthest field l reads.
func (l *procLayout) end() int {
	//: the three 32-bit fields and the two halves of the start.
	return max(l.pid+int32Bytes, l.ruid+int32Bytes, l.euid+int32Bytes, l.sec.off+l.sec.width, l.usec.off+l.usec.width)
}

// readInt32 reads the signed 32-bit integer at off.
func readInt32(buf []byte, off int) int64 {
	//: the kernel's own byte order, sign kept.
	return int64(int32(binary.NativeEndian.Uint32(buf[off:])))
}

// readField reads an unsigned integer of f's width at f's offset.
func readField(buf []byte, f field) uint64 {
	//: a 64-bit field.
	if f.width == int64Bytes {
		//: the kernel's own byte order.
		return binary.NativeEndian.Uint64(buf[f.off:])
	}
	//: a 32-bit field.
	return uint64(binary.NativeEndian.Uint32(buf[f.off:]))
}

// layoutOf returns the record layout of goos and goarch, nil where this
// package holds none: Linux reads /proc, and illumos, Solaris and Windows
// have no such record.
func layoutOf(goos, goarch string) *procLayout {
	//: by kernel.
	switch goos {
	//: three ABIs.
	case "freebsd":
		//: by architecture.
		return freeBSDLayout(goarch)
	//: amd64 only.
	case "dragonfly":
		//: the one port.
		return pick(goarch == "amd64", &dragonFly)
	//: the 64-bit ports.
	case "darwin", "ios":
		//: amd64 and arm64.
		return pick(goarch == "amd64" || goarch == "arm64", &darwin)
	//: one layout everywhere.
	case "openbsd":
		//: fixed-width fields.
		return &openBSD
	//: one layout everywhere.
	case "netbsd":
		//: fixed-width fields.
		return &netBSD
	}
	//: no record.
	return nil
}

// freeBSDLayout returns FreeBSD's struct kinfo_proc on goarch, nil on an
// architecture FreeBSD has no Go port for.
func freeBSDLayout(goarch string) *procLayout {
	//: by ABI.
	switch goarch {
	//: LP64.
	case "amd64", "arm64", "riscv64":
		//: 1088 bytes.
		return &freeBSDLP64
	//: ILP32, time_t of 32 bits.
	case "386":
		//: 768 bytes.
		return &freeBSD386
	//: ILP32, time_t of 64 bits aligned to 8.
	case "arm":
		//: 816 bytes.
		return &freeBSDArm
	}
	//: unknown.
	return nil
}

// pick returns l when ok, nil otherwise.
func pick(ok bool, l *procLayout) *procLayout {
	//: a port this package reads.
	if ok {
		//: its layout.
		return l
	}
	//: none.
	return nil
}
