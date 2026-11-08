//go:build linux && (amd64 || arm64)

package exec

import (
	"encoding/binary"
	"slices"
	"syscall"
	"unsafe"
)

// The sandbox payload's framing: a magic, a version, a length that must be the
// sealed file's size, then the fields in one fixed order. A reader refuses any
// other version, any byte past the last field, and any count or string longer
// than its bound — so a payload is either the one the parent wrote or nothing.
const (
	// sandboxMagic opens every payload.
	sandboxMagic string = "KSBX"
	// sandboxVersion is the one layout this build reads and writes: 2 opens
	// the body with the cgroup namespace's flag.
	sandboxVersion uint16 = 2
	// sandboxHeaderLen is the magic, the version, a reserved half-word and the length.
	sandboxHeaderLen int = 12
	// sandboxMaxPayload bounds the whole payload: 1 MiB.
	sandboxMaxPayload int = 1 << 20
	// sandboxMaxString bounds one path: PATH_MAX.
	sandboxMaxString int = 4096
	// sandboxMaxItems bounds a count of mounts, groups or rules.
	sandboxMaxItems int = 65536
	// sandboxMaxInsns is BPF_MAXINSNS, the longest filter the kernel loads.
	sandboxMaxInsns int = 4096
	// sandboxNoTrace is the trace descriptor of a payload that records nothing.
	sandboxNoTrace int32 = -1
)

// The memfd the payload travels in (linux/memfd.h, linux/fcntl.h): created
// close-on-exec and sealable, then sealed against any change before the fork.
const (
	mfdCloexec       uintptr = 0x1
	mfdAllowSealing  uintptr = 0x2
	fAddSeals        uintptr = 1033
	fGetSeals        uintptr = 1034
	fSealSeal        uintptr = 0x1
	fSealShrink      uintptr = 0x2
	fSealGrow        uintptr = 0x4
	fSealWrite       uintptr = 0x8
	sandboxSealSet   uintptr = fSealSeal | fSealShrink | fSealGrow | fSealWrite
	sandboxMemfdName string  = "kitsunium-sandbox"
)

// sandboxMount is one MountValue as the child applies it.
type sandboxMount struct {
	kind     uint8
	readOnly bool
	source   string
	target   string
}

// sandboxIdentity is the credential the child takes itself (Spec.User, Group,
// Groups resolved by the parent). setGroups is false in a user namespace the
// spawn created, where setgroups(2) is denied and the groups stay as they are.
type sandboxIdentity struct {
	uid       uint32
	gid       uint32
	setGroups bool
	groups    []uint32
}

// sandboxCaps is CapabilitiesValue with the highest capability the kernel
// knows (/proc/sys/kernel/cap_last_cap), read by the parent.
type sandboxCaps struct {
	bounding   uint64
	ambient    uint64
	secureBits uint32
	lastCap    uint32
}

// sandboxRule is one Landlock rule with its rights trimmed to the kernel's ABI.
type sandboxRule struct {
	access uint64
	path   string
}

// sockFilter is struct sock_filter, one classic BPF instruction.
type sockFilter struct {
	code uint16
	jt   uint8
	jf   uint8
	k    uint32
}

// sandboxPayload is everything the second stage applies, in its order.
type sandboxPayload struct {
	// cgroupNS asks the stage to unshare a cgroup namespace (NamespaceCgroup),
	// which the clone does not: the child must be in its group first.
	cgroupNS bool
	mounts   []sandboxMount
	root     string
	// dir is Spec.Dir, entered inside root after the pivot; unused without one.
	dir        string
	identity   *sandboxIdentity
	caps       *sandboxCaps
	noNewPrivs bool
	// handled is the Landlock rights restricted; zero means no Landlock.
	handled uint64
	rules   []sandboxRule
	// filter is the assembled seccomp program; empty means no filter.
	filter    []sockFilter
	pdeathsig uint32
	// parentPID is the supervisor's pid, checked after the parent-death signal
	// is armed again; zero (a child in its own PID namespace) checks nothing.
	parentPID uint32
	// failStep and traceFD serve the package's tests alone: the step failed by
	// injection (zero none) and the descriptor each step records itself on.
	failStep uint8
	traceFD  int32
}

// payloadWriter appends the payload's fields little-endian.
type payloadWriter struct {
	buf []byte
}

// u8 appends one byte.
func (w *payloadWriter) u8(v uint8) {
	w.buf = append(w.buf, v)
}

// u32 appends a 32-bit word.
func (w *payloadWriter) u32(v uint32) {
	w.buf = binary.LittleEndian.AppendUint32(w.buf, v)
}

// u64 appends a 64-bit word.
func (w *payloadWriter) u64(v uint64) {
	w.buf = binary.LittleEndian.AppendUint64(w.buf, v)
}

// flag appends a boolean as one byte.
func (w *payloadWriter) flag(v bool) {
	//: one for true.
	if v {
		w.u8(1)
		return
	}
	w.u8(0)
}

// str appends a length-prefixed string.
func (w *payloadWriter) str(s string) {
	w.u32(uint32(len(s)))
	w.buf = append(w.buf, s...)
}

// encodeSandbox writes p in the payload layout.
func encodeSandbox(p *sandboxPayload) []byte {
	w := &payloadWriter{buf: make([]byte, 0, sandboxHeaderLen)}
	w.buf = append(w.buf, sandboxMagic...)
	w.buf = binary.LittleEndian.AppendUint16(w.buf, sandboxVersion)
	w.buf = binary.LittleEndian.AppendUint16(w.buf, 0)
	//: the length, filled in once the body is written.
	w.u32(0)
	w.flag(p.cgroupNS)
	encodeMounts(w, p)
	encodeCredentials(w, p)
	encodeLandlock(w, p)
	w.u32(uint32(len(p.filter)))
	//: the seccomp program, one instruction at a time.
	for _, ins := range p.filter {
		w.buf = binary.LittleEndian.AppendUint16(w.buf, ins.code)
		w.u8(ins.jt)
		w.u8(ins.jf)
		w.u32(ins.k)
	}
	w.u32(p.pdeathsig)
	w.u32(p.parentPID)
	w.u8(p.failStep)
	w.u32(uint32(p.traceFD))
	binary.LittleEndian.PutUint32(w.buf[8:], uint32(len(w.buf)))
	//: the framed payload.
	return w.buf
}

// encodeMounts writes the mounts, the root and the working directory.
func encodeMounts(w *payloadWriter, p *sandboxPayload) {
	w.u32(uint32(len(p.mounts)))
	//: each mount, in order.
	for _, m := range p.mounts {
		w.u8(m.kind)
		w.flag(m.readOnly)
		w.str(m.source)
		w.str(m.target)
	}
	w.str(p.root)
	w.str(p.dir)
}

// encodeCredentials writes the identity, the capabilities and no_new_privs.
func encodeCredentials(w *payloadWriter, p *sandboxPayload) {
	w.flag(p.identity != nil)
	//: the identity the child takes.
	if id := p.identity; id != nil {
		w.u32(id.uid)
		w.u32(id.gid)
		w.flag(id.setGroups)
		w.u32(uint32(len(id.groups)))
		//: each supplementary group.
		for _, g := range id.groups {
			w.u32(g)
		}
	}
	w.flag(p.caps != nil)
	//: the capability sets.
	if c := p.caps; c != nil {
		w.u64(c.bounding)
		w.u64(c.ambient)
		w.u32(c.secureBits)
		w.u32(c.lastCap)
	}
	w.flag(p.noNewPrivs)
}

// encodeLandlock writes the handled rights and the rules.
func encodeLandlock(w *payloadWriter, p *sandboxPayload) {
	w.u64(p.handled)
	w.u32(uint32(len(p.rules)))
	//: each rule.
	for _, r := range p.rules {
		w.u64(r.access)
		w.str(r.path)
	}
}

// payloadReader reads the payload's fields, refusing any read past its end.
type payloadReader struct {
	buf []byte
	bad bool
}

// take returns the next n bytes, or nil once the payload is short.
func (r *payloadReader) take(n int) []byte {
	//: a read past the end marks the payload malformed.
	if r.bad || n < 0 || n > len(r.buf) {
		r.bad = true
		return nil
	}
	out := r.buf[:n]
	r.buf = r.buf[n:]
	//: the bytes asked for.
	return out
}

// u8 reads one byte.
func (r *payloadReader) u8() uint8 {
	b := r.take(1)
	//: short.
	if b == nil {
		return 0
	}
	return b[0]
}

// u16 reads a 16-bit word.
func (r *payloadReader) u16() uint16 {
	b := r.take(2)
	//: short.
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

// u32 reads a 32-bit word.
func (r *payloadReader) u32() uint32 {
	b := r.take(4)
	//: short.
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

// u64 reads a 64-bit word.
func (r *payloadReader) u64() uint64 {
	b := r.take(8)
	//: short.
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

// flag reads a boolean byte, refusing any value but 0 and 1.
func (r *payloadReader) flag() bool {
	v := r.u8()
	//: a byte that is no boolean.
	if v > 1 {
		r.bad = true
	}
	return v == 1
}

// count reads a count and refuses one past limit.
func (r *payloadReader) count(limit int) int {
	n := r.u32()
	//: more items than any payload carries.
	if int64(n) > int64(limit) {
		r.bad = true
		return 0
	}
	return int(n)
}

// str reads a length-prefixed string without NUL, at most sandboxMaxString long.
func (r *payloadReader) str() string {
	b := r.take(r.count(sandboxMaxString))
	//: a NUL would cut the path the kernel reads.
	if slices.Contains(b, 0) {
		r.bad = true
		return ""
	}
	return string(b)
}

// decodeSandbox parses a payload, refusing with EINVAL anything but the exact
// layout of sandboxVersion: a wrong magic, an unknown version, a length that is
// not the data's, a count or string past its bound, a byte left over.
func decodeSandbox(data []byte) (*sandboxPayload, syscall.Errno) {
	//: too short or too long for any payload, or a header that is not this one's.
	if len(data) < sandboxHeaderLen || len(data) > sandboxMaxPayload || !sandboxHeaderOK(data) {
		return nil, syscall.EINVAL
	}
	r := &payloadReader{buf: data[sandboxHeaderLen:]}
	p := &sandboxPayload{cgroupNS: r.flag()}
	decodeMounts(r, p)
	decodeCredentials(r, p)
	decodeLandlock(r, p)
	n := r.count(sandboxMaxInsns)
	p.filter = make([]sockFilter, 0, n)
	//: each instruction.
	for range n {
		p.filter = append(p.filter, sockFilter{code: r.u16(), jt: r.u8(), jf: r.u8(), k: r.u32()})
	}
	p.pdeathsig = r.u32()
	p.parentPID = r.u32()
	p.failStep = r.u8()
	p.traceFD = int32(r.u32())
	//: short, out of bounds, or longer than its fields.
	if r.bad || len(r.buf) != 0 {
		return nil, syscall.EINVAL
	}
	//: the payload as written.
	return p, 0
}

// sandboxHeaderOK reports whether data opens with the magic, this version, a
// zero reserved half-word and its own length.
func sandboxHeaderOK(data []byte) bool {
	//: every field of the header.
	return string(data[:len(sandboxMagic)]) == sandboxMagic && binary.LittleEndian.Uint16(data[4:]) == sandboxVersion &&
		binary.LittleEndian.Uint16(data[6:]) == 0 && int(binary.LittleEndian.Uint32(data[8:])) == len(data)
}

// decodeMounts reads the mounts, the root and the working directory.
func decodeMounts(r *payloadReader, p *sandboxPayload) {
	n := r.count(sandboxMaxItems)
	p.mounts = make([]sandboxMount, 0, n)
	//: each mount.
	for range n {
		p.mounts = append(p.mounts, sandboxMount{kind: r.u8(), readOnly: r.flag(), source: r.str(), target: r.str()})
	}
	p.root = r.str()
	p.dir = r.str()
}

// decodeCredentials reads the identity, the capabilities and no_new_privs.
func decodeCredentials(r *payloadReader, p *sandboxPayload) {
	//: an identity to take.
	if r.flag() {
		uid, gid, set := r.u32(), r.u32(), r.flag()
		n := r.count(sandboxMaxItems)
		groups := make([]uint32, 0, n)
		//: each supplementary group.
		for range n {
			groups = append(groups, r.u32())
		}
		p.identity = &sandboxIdentity{uid: uid, gid: gid, setGroups: set, groups: groups}
	}
	//: capability sets to apply.
	if r.flag() {
		p.caps = &sandboxCaps{bounding: r.u64(), ambient: r.u64(), secureBits: r.u32(), lastCap: r.u32()}
	}
	p.noNewPrivs = r.flag()
}

// decodeLandlock reads the handled rights and the rules.
func decodeLandlock(r *payloadReader, p *sandboxPayload) {
	p.handled = r.u64()
	n := r.count(sandboxMaxItems)
	p.rules = make([]sandboxRule, 0, n)
	//: each rule.
	for range n {
		p.rules = append(p.rules, sandboxRule{access: r.u64(), path: r.str()})
	}
}

// sealedPayload writes data into a new memfd and seals it against any write,
// growth, shrink or further seal change, so what the child reads is what the
// parent wrote. The descriptor is close-on-exec in the parent; the fork's dup2
// gives the child its own copy without the flag.
func sealedPayload(data []byte) (fd int, errno syscall.Errno) {
	name, err := syscall.BytePtrFromString(sandboxMemfdName)
	//: never: the name is a constant without NUL.
	if err != nil {
		return -1, syscall.EINVAL
	}
	r, _, errno := syscall.Syscall(sysMemfdCreate, uintptr(unsafe.Pointer(name)), mfdCloexec|mfdAllowSealing, 0)
	//: no memfd: ENOSYS before Linux 3.17.
	if errno != 0 {
		return -1, errno
	}
	fd = int(r)
	//: the bytes, then the seals.
	if wErr := writePayload(fd, data); wErr != 0 {
		swallowErr(syscall.Close(fd))
		return -1, wErr
	}
	_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), fAddSeals, sandboxSealSet)
	//: a memfd that cannot be sealed is not sent.
	if errno != 0 {
		swallowErr(syscall.Close(fd))
		return -1, errno
	}
	//: the sealed payload.
	return fd, 0
}

// writePayload writes data to fd until every byte is written.
func writePayload(fd int, data []byte) syscall.Errno {
	//: a short write continues where it stopped.
	for len(data) > 0 {
		n, err := syscall.Write(fd, data)
		//: the write failed.
		if err != nil {
			//: the kernel's errno, or EIO.
			return errnoOf(err)
		}
		data = data[n:]
	}
	//: every byte written.
	return 0
}

// readSealedPayload reads the payload the parent sealed in fd: every seal must
// be set and the file no longer than sandboxMaxPayload, and its bytes must
// decode. The descriptor is closed whatever the outcome.
func readSealedPayload(fd int) (*sandboxPayload, syscall.Errno) {
	defer func() { swallowErr(syscall.Close(fd)) }()
	seals, _, errno := syscall.RawSyscall(syscall.SYS_FCNTL, uintptr(fd), fGetSeals, 0)
	//: not a memfd, or one a writer could still change.
	if errno != 0 || seals&sandboxSealSet != sandboxSealSet {
		return nil, syscall.EPERM
	}
	var st syscall.Stat_t
	//: the size the header must state.
	if err := syscall.Fstat(fd, &st); err != nil || st.Size < int64(sandboxHeaderLen) || st.Size > int64(sandboxMaxPayload) {
		return nil, syscall.EINVAL
	}
	data := make([]byte, st.Size)
	n, err := syscall.Pread(fd, data, 0)
	//: a short read cannot be the sealed size.
	if err != nil || n != len(data) {
		return nil, syscall.EIO
	}
	p, errno := decodeSandbox(data)
	//: a layout this build does not read.
	if errno != 0 {
		return nil, errno
	}
	//: the payload as sealed.
	return p, 0
}
