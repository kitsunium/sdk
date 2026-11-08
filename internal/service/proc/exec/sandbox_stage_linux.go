//go:build linux && (amd64 || arm64)

package exec

import (
	"encoding/binary"
	"errors"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// The kernel constants the second stage passes (linux/prctl.h, linux/mount.h,
// linux/capability.h, linux/landlock.h, linux/seccomp.h, linux/fcntl.h).
const (
	prSetPdeathsig         uintptr       = 1
	prSetKeepCaps          uintptr       = 8
	prSetSeccomp           uintptr       = 22
	prCapbsetRead          uintptr       = 23
	prCapbsetDrop          uintptr       = 24
	prSetSecureBits        uintptr       = 28
	prSetNoNewPrivs        uintptr       = 38
	prCapAmbient           uintptr       = 47
	prCapAmbientRaise      uintptr       = 2
	prCapAmbientClearAll   uintptr       = 4
	seccompModeFilter      uintptr       = 2
	capabilityVersion3     uint32        = 0x20080522
	msRdonly               uintptr       = 0x1
	msNosuid               uintptr       = 0x2
	msNodev                uintptr       = 0x4
	msBind                 uintptr       = 0x1000
	msRec                  uintptr       = 0x4000
	mntDetach              uintptr       = 0x2
	mountAttrRdonly        uint64        = 0x1
	mountAttrSizeVer0      uintptr       = 32
	atRecursive            uintptr       = 0x8000
	atFDCWD                uintptr       = ^uintptr(99) // AT_FDCWD, -100
	landlockRulePathBeneat uintptr       = 1
	landlockRulesetAttrLen uintptr       = 8
	stageInjectedErrno     syscall.Errno = syscall.ECANCELED
	tmpfsOptions           string        = "mode=0755"
	tmpfsStickyOptions     string        = "mode=1777"
	procSelfFD             string        = "/proc/self/fd"
	traceRecordLen         int           = 5
)

// capHeader is struct __user_cap_header_struct.
type capHeader struct {
	version uint32
	pid     int32
}

// capData is struct __user_cap_data_struct, two of which hold 64 capabilities.
type capData struct {
	effective   uint32
	permitted   uint32
	inheritable uint32
}

// mountAttr is struct mount_attr, MOUNT_ATTR_SIZE_VER0 long.
type mountAttr struct {
	attrSet     uint64
	attrClr     uint64
	propagation uint64
	usernsFD    uint64
}

// landlockPathBeneath is struct landlock_path_beneath_attr: packed in C, 12
// bytes, which this layout's first 12 bytes are.
type landlockPathBeneath struct {
	allowedAccess uint64
	parentFD      int32
}

// sockFprog is struct sock_fprog.
type sockFprog struct {
	length uint16
	filter *sockFilter
}

// sandboxStage is the second stage of a sandboxed spawn: the payload, the
// handshake descriptor its failures are reported on, the Landlock ruleset
// between its two steps, and the target's argv and environment, converted
// before the filter so nothing is allocated after it.
type sandboxStage struct {
	p       *sandboxPayload
	hsfd    int
	ruleset int
	argv0   *byte
	argv    []*byte
	envv    []*byte
}

// execSandboxed runs the second stage and execs target, never returning: it
// reads the sealed payload at the descriptor fdText names, applies each step
// in order on this thread — locked to it, as the runtime keeps the main
// goroutine during package initialisation — and installs the seccomp filter
// immediately before the execve. A step that fails reports itself on the
// handshake pipe and exits: the target never runs.
func execSandboxed(fdText, target string, args, env []string) {
	runtime.LockOSThread()
	st := &sandboxStage{hsfd: childHandshakeFD(), ruleset: -1}
	fd, err := strconv.Atoi(fdText)
	//: a descriptor number the parent never wrote.
	if err != nil {
		sandboxFail(st.hsfd, stepPayload, syscall.EINVAL)
	}
	p, errno := readSealedPayload(fd)
	//: an unsealed, unknown or mis-sized payload.
	if errno != 0 {
		sandboxFail(st.hsfd, stepPayload, errno)
	}
	st.p = p
	//: the target's strings, before any step: a NUL in one is an exec failure.
	if st.prepareExec(target, args, env) != nil {
		reportHandshake(handshakeExecFail)
		os.Exit(trampolineExecExit)
	}
	steps := [...]func(*sandboxStage) syscall.Errno{
		(*sandboxStage).cgroupNamespace,
		(*sandboxStage).mount, (*sandboxStage).pivot, (*sandboxStage).landlockPaths,
		(*sandboxStage).bounding, (*sandboxStage).secureBits, (*sandboxStage).identity,
		(*sandboxStage).capabilities, (*sandboxStage).sweep, (*sandboxStage).noNewPrivs,
		(*sandboxStage).landlock,
	}
	//: each step, in the kernel's order.
	for i, step := range steps {
		st.run(uint8(i)+stepCgroupNamespace, step)
	}
	st.run(stepSeccomp, func(*sandboxStage) syscall.Errno { return 0 })
	st.filterAndExec()
}

// prepareExec converts the target's path, argv and environment.
func (st *sandboxStage) prepareExec(target string, args, env []string) error {
	var err error
	st.argv0, err = syscall.BytePtrFromString(target)
	//: a NUL in the path.
	if err != nil {
		return err
	}
	st.argv, err = syscall.SlicePtrFromStrings(args)
	//: a NUL in an argument.
	if err != nil {
		return err
	}
	st.envv, err = syscall.SlicePtrFromStrings(env)
	return err
}

// run traces step, fails it when a test injects a failure there, and runs it.
func (st *sandboxStage) run(step uint8, fn func(*sandboxStage) syscall.Errno) {
	st.trace(step)
	//: the tests' injected failure.
	if st.p.failStep == step {
		sandboxFail(st.hsfd, step, stageInjectedErrno)
	}
	//: the step's own failure.
	if errno := fn(st); errno != 0 {
		sandboxFail(st.hsfd, step, errno)
	}
}

// trace records step and the thread running it on the tests' trace descriptor.
func (st *sandboxStage) trace(step uint8) {
	//: production records nothing.
	if st.p.traceFD < 0 {
		return
	}
	var rec [traceRecordLen]byte
	rec[0] = step
	binary.LittleEndian.PutUint32(rec[1:], uint32(syscall.Gettid()))
	swallowErr(rawWrite(int(st.p.traceFD), rec[:]))
}

// sandboxFail reports step's failure with errno on the handshake descriptor
// and exits: the target never runs.
func sandboxFail(hsfd int, step uint8, errno syscall.Errno) {
	report := sandboxReport(step, errno)
	swallowErr(rawWrite(hsfd, report[:]))
	stderrLine("sdk sandbox: " + sandboxStepName(step) + ": " + errno.Error())
	os.Exit(trampolineApplyExit)
}

// cgroupNamespace unshares a cgroup namespace on this thread, the one that
// execs the target, when the sandbox asks for one. The child is already in
// its group — the clone placed it, or the trampoline wrote its PID — so the
// namespace is rooted there, and the target sees that group as its root on
// either path; the stage still holds the capabilities unshare needs.
func (st *sandboxStage) cgroupNamespace() syscall.Errno {
	//: not asked.
	if !st.p.cgroupNS {
		return 0
	}
	_, _, errno := syscall.RawSyscall(syscall.SYS_UNSHARE, syscall.CLONE_NEWCGROUP, 0, 0)
	return errno
}

// mount makes each mount in order.
func (st *sandboxStage) mount() syscall.Errno {
	//: each mount.
	for _, m := range st.p.mounts {
		//: the first refusal stops the stage.
		if errno := applyMount(m); errno != 0 {
			return errno
		}
	}
	return 0
}

// applyMount makes one mount, then makes it read-only, recursively, when asked.
func applyMount(m sandboxMount) syscall.Errno {
	var errno syscall.Errno
	//: what is mounted.
	switch m.kind {
	//: an empty tmpfs, read-only from the start when asked.
	case uint8(coreproc.MountTmpfs), uint8(coreproc.MountTmpfsSticky):
		flags, options := msNosuid|msNodev, tmpfsOptions
		//: read-only.
		if m.readOnly {
			flags |= msRdonly
		}
		//: a /tmp every user writes to, sticky.
		if m.kind == uint8(coreproc.MountTmpfsSticky) {
			options = tmpfsStickyOptions
		}
		return rawMount("tmpfs", m.target, "tmpfs", flags, options)
	//: another tree, recursively.
	case uint8(coreproc.MountBind):
		errno = rawMount(m.source, m.target, "", msBind|msRec, "")
	//: the tree onto itself, to make it read-only below.
	default:
		errno = rawMount(m.target, m.target, "", msBind|msRec, "")
	}
	//: the bind failed, or nothing more to do.
	if errno != 0 || (m.kind == uint8(coreproc.MountBind) && !m.readOnly) {
		return errno
	}
	//: read-only, the whole tree.
	return mountReadOnly(m.target)
}

// rawMount is mount(2); an empty fstype or data passes NULL.
func rawMount(source, target, fstype string, flags uintptr, data string) syscall.Errno {
	src, e1 := syscall.BytePtrFromString(source)
	dst, e2 := syscall.BytePtrFromString(target)
	//: the payload refused NULs; never reached.
	if e1 != nil || e2 != nil {
		return syscall.EINVAL
	}
	fs, opts := optionalCString(fstype), optionalCString(data)
	_, _, errno := syscall.RawSyscall6(syscall.SYS_MOUNT, uintptr(unsafe.Pointer(src)), uintptr(unsafe.Pointer(dst)),
		uintptr(unsafe.Pointer(fs)), flags, uintptr(unsafe.Pointer(opts)), 0)
	return errno
}

// optionalCString is s as a C string, nil — NULL to the kernel — when empty
// or holding a NUL, which the stage's own constants never do.
func optionalCString(s string) *byte {
	p, err := syscall.BytePtrFromString(s)
	//: empty, or not a C string.
	if s == "" || err != nil {
		return nil
	}
	return p
}

// mountReadOnly sets MOUNT_ATTR_RDONLY on target and every mount below it
// with mount_setattr(2): a remount would need each one's locked flags.
func mountReadOnly(target string) syscall.Errno {
	path, err := syscall.BytePtrFromString(target)
	//: never reached: the payload refused NULs.
	if err != nil {
		return syscall.EINVAL
	}
	_, _, errno := syscall.RawSyscall6(sysMountSetattr, atFDCWD, uintptr(unsafe.Pointer(path)), atRecursive,
		uintptr(unsafe.Pointer(new(mountAttr{attrSet: mountAttrRdonly}))), mountAttrSizeVer0, 0)
	return errno
}

// pivot moves the child into its root: the root bound onto itself so it is a
// mount point, pivot_root(".", ".") stacking the old root under the new one,
// the old root detached, then the working directory entered inside.
func (st *sandboxStage) pivot() syscall.Errno {
	root := st.p.root
	//: no root asked.
	if root == "" {
		return 0
	}
	//: a mount point of its own.
	if errno := rawMount(root, root, "", msBind|msRec, ""); errno != 0 {
		return errno
	}
	//: pivot_root(".", ".") needs the new root as the working directory.
	if err := syscall.Chdir(root); err != nil {
		return errnoOf(err)
	}
	dot := optionalCString(".")
	_, _, errno := syscall.RawSyscall(syscall.SYS_PIVOT_ROOT, uintptr(unsafe.Pointer(dot)), uintptr(unsafe.Pointer(dot)), 0)
	//: the kernel refused the pivot.
	if errno != 0 {
		return errno
	}
	_, _, errno = syscall.RawSyscall(syscall.SYS_UMOUNT2, uintptr(unsafe.Pointer(dot)), mntDetach, 0)
	//: the old root stays reachable otherwise.
	if errno != 0 {
		return errno
	}
	dir := st.p.dir
	//: the root itself when no directory is asked.
	if dir == "" {
		dir = "/"
	}
	return errnoOf(syscall.Chdir(dir))
}

// errnoOf returns err's errno, EIO for any other error, zero for nil.
func errnoOf(err error) syscall.Errno {
	//: success.
	if err == nil {
		return 0
	}
	//: the kernel's answer.
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		return errno
	}
	return syscall.EIO
}

// landlockPaths creates the ruleset and adds each rule, its path opened
// O_PATH while the stage still holds its privileges and after its mounts and
// pivot, so the rules name what the child will see.
func (st *sandboxStage) landlockPaths() syscall.Errno {
	//: no Landlock asked, or none the kernel restricts.
	if st.p.handled == 0 {
		return 0
	}
	handled := st.p.handled
	rs, _, errno := syscall.RawSyscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&handled)), landlockRulesetAttrLen, 0)
	//: no ruleset.
	if errno != 0 {
		return errno
	}
	st.ruleset = int(rs)
	//: each rule.
	for _, r := range st.p.rules {
		//: the first refusal stops the stage.
		if errno := addLandlockRule(st.ruleset, r); errno != 0 {
			return errno
		}
	}
	return 0
}

// addLandlockRule opens r's path O_PATH and adds it to ruleset.
func addLandlockRule(ruleset int, r sandboxRule) syscall.Errno {
	path, err := syscall.BytePtrFromString(r.path)
	//: never reached: the payload refused NULs.
	if err != nil {
		return syscall.EINVAL
	}
	fd, _, errno := syscall.RawSyscall6(syscall.SYS_OPENAT, atFDCWD, uintptr(unsafe.Pointer(path)), uintptr(oPath|syscall.O_CLOEXEC), 0, 0, 0)
	//: a path the child cannot reach.
	if errno != 0 {
		return errno
	}
	attr := new(landlockPathBeneath{allowedAccess: r.access, parentFD: int32(fd)})
	_, _, errno = syscall.RawSyscall6(sysLandlockAddRule, uintptr(ruleset), landlockRulePathBeneat, uintptr(unsafe.Pointer(attr)), 0, 0, 0)
	swallowErr(syscall.Close(int(fd)))
	return errno
}

// bounding drops from the bounding set every capability the kernel knows that
// is present and not kept.
func (st *sandboxStage) bounding() syscall.Errno {
	c := st.p.caps
	//: inherited unchanged.
	if c == nil {
		return 0
	}
	//: each capability the kernel knows.
	for cp := uintptr(0); cp <= uintptr(c.lastCap); cp++ {
		//: kept.
		if c.bounding&(1<<cp) != 0 {
			continue
		}
		in, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prCapbsetRead, cp, 0)
		//: already out of the set.
		if errno == 0 && in == 0 {
			continue
		}
		//: CAP_SETPCAP is needed to drop it.
		if _, _, errno = syscall.RawSyscall(syscall.SYS_PRCTL, prCapbsetDrop, cp, 0); errno != 0 {
			return errno
		}
	}
	return 0
}

// secureBits sets the securebits asked.
func (st *sandboxStage) secureBits() syscall.Errno {
	c := st.p.caps
	//: none asked.
	if c == nil || c.secureBits == 0 {
		return 0
	}
	_, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetSecureBits, uintptr(c.secureBits), 0)
	return errno
}

// identity takes the credential the spec names, on this thread alone — the
// thread that execs —, keeping the permitted set when ambient capabilities are
// to be raised, then arms the parent-death signal again, which the kernel
// clears when the effective ids change.
func (st *sandboxStage) identity() syscall.Errno {
	id := st.p.identity
	//: the parent's identity stands.
	if id == nil {
		return 0
	}
	//: ambient capabilities need the permitted set past the uid change.
	if c := st.p.caps; c != nil && c.ambient != 0 {
		//: refused once SECBIT_KEEP_CAPS_LOCKED is set.
		if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetKeepCaps, 1, 0); errno != 0 {
			return errno
		}
	}
	//: the groups, the gid, the uid: the order that keeps the right to set them.
	if errno := setGroups(id); errno != 0 {
		return errno
	}
	//: real, effective and saved gid.
	if _, _, errno := syscall.RawSyscall(syscall.SYS_SETRESGID, uintptr(id.gid), uintptr(id.gid), uintptr(id.gid)); errno != 0 {
		return errno
	}
	//: real, effective and saved uid.
	if _, _, errno := syscall.RawSyscall(syscall.SYS_SETRESUID, uintptr(id.uid), uintptr(id.uid), uintptr(id.uid)); errno != 0 {
		return errno
	}
	return st.rearmDeathSignal()
}

// setGroups sets the supplementary groups — cleared when none — unless the
// spawn's own user namespace denies setgroups(2).
func setGroups(id *sandboxIdentity) syscall.Errno {
	//: denied in the namespace: the groups stay.
	if !id.setGroups {
		return 0
	}
	var groups uintptr
	//: the list, or NULL for none.
	if len(id.groups) > 0 {
		groups = uintptr(unsafe.Pointer(&id.groups[0]))
	}
	_, _, errno := syscall.RawSyscall(syscall.SYS_SETGROUPS, uintptr(len(id.groups)), groups, 0)
	return errno
}

// rearmDeathSignal sets the parent-death signal again and, outside a PID
// namespace, checks the supervisor did not die before it was set.
func (st *sandboxStage) rearmDeathSignal() syscall.Errno {
	//: none asked.
	if st.p.pdeathsig == 0 {
		return 0
	}
	//: armed again.
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetPdeathsig, uintptr(st.p.pdeathsig), 0); errno != 0 {
		return errno
	}
	//: the supervisor died in the window: the child must not outlive it.
	if st.p.parentPID != 0 && uint32(syscall.Getppid()) != st.p.parentPID {
		return syscall.ESRCH
	}
	return 0
}

// capabilities sets the thread's sets — permitted and effective cut to the
// bounding set, inheritable to the ambient one — then clears the ambient set
// and raises each capability asked.
func (st *sandboxStage) capabilities() syscall.Errno {
	c := st.p.caps
	//: inherited unchanged.
	if c == nil {
		return 0
	}
	hdr := capHeader{version: capabilityVersion3}
	var data [2]capData
	//: the thread's sets.
	if _, _, errno := syscall.RawSyscall(syscall.SYS_CAPGET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0); errno != 0 {
		return errno
	}
	permitted := (uint64(data[0].permitted) | uint64(data[1].permitted)<<32) & c.bounding
	//: the two words of each set.
	for i := range data {
		shift := 32 * uint(i)
		data[i] = capData{effective: uint32(permitted >> shift), permitted: uint32(permitted >> shift), inheritable: uint32(c.ambient >> shift)}
	}
	//: an inheritable capability outside the permitted set is EPERM.
	if _, _, errno := syscall.RawSyscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0); errno != 0 {
		return errno
	}
	//: no ambient capability but those asked.
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prCapAmbient, prCapAmbientClearAll, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	//: each ambient capability asked.
	for cp := uintptr(0); cp <= uintptr(c.lastCap); cp++ {
		//: raised.
		if c.ambient&(1<<cp) != 0 {
			//: refused when not permitted and inheritable.
			if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prCapAmbient, prCapAmbientRaise, cp, 0, 0, 0); errno != 0 {
				return errno
			}
		}
	}
	return 0
}

// sweep marks close-on-exec every descriptor past the handshake pipe — the
// target keeps its stdio and ExtraFiles, below it — with one
// close_range(CLOSE_RANGE_CLOEXEC), or the walk of /proc/self/fd before Linux
// 5.11.
func (st *sandboxStage) sweep() syscall.Errno {
	_, _, errno := syscall.RawSyscall(sysCloseRange, uintptr(st.hsfd+1), closeRangeLast, closeRangeCloexec)
	//: marked in one call.
	if errno == 0 {
		return 0
	}
	//: a real refusal.
	if errno != syscall.ENOSYS && errno != syscall.EINVAL {
		return errno
	}
	entries, err := os.ReadDir(procSelfFD)
	//: nothing says which descriptors are open.
	if err != nil {
		return errnoOf(err)
	}
	//: each listed descriptor past the handshake.
	for _, entry := range entries {
		//: marked.
		if fd, aErr := strconv.Atoi(entry.Name()); aErr == nil && fd > st.hsfd {
			syscall.CloseOnExec(fd)
		}
	}
	return 0
}

// noNewPrivs sets no_new_privs, which Landlock and seccomp require.
func (st *sandboxStage) noNewPrivs() syscall.Errno {
	//: not asked, nor needed.
	if !st.p.noNewPrivs {
		return 0
	}
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0)
	return errno
}

// landlock restricts this thread — and the target it execs — to the ruleset.
func (st *sandboxStage) landlock() syscall.Errno {
	//: no ruleset was built.
	if st.ruleset < 0 {
		return 0
	}
	_, _, errno := syscall.RawSyscall(sysLandlockRestrictSelf, uintptr(st.ruleset), 0, 0)
	swallowErr(syscall.Close(st.ruleset))
	return errno
}

// filterAndExec installs the seccomp filter, if any, and execs the target in
// the next system call: nothing is allocated between the two. A filter the
// kernel refuses is the seccomp step's failure; an execve that fails is
// reported as an exec failure through the write the filter lets through, and
// the stage exits with the call the filter lets through.
func (st *sandboxStage) filterAndExec() {
	//: the filter, last of all.
	if filter := st.p.filter; len(filter) > 0 {
		prog := new(sockFprog{length: uint16(len(filter)), filter: &filter[0]})
		//: refused: the target never runs unfiltered.
		if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetSeccomp, seccompModeFilter, uintptr(unsafe.Pointer(prog))); errno != 0 {
			sandboxFail(st.hsfd, stepSeccomp, errno)
		}
	}
	_, _, errno := syscall.RawSyscall(syscall.SYS_EXECVE, uintptr(unsafe.Pointer(st.argv0)), uintptr(unsafe.Pointer(&st.argv[0])), uintptr(unsafe.Pointer(&st.envv[0])))
	//: returned: the target was not executed; the report, then the exit.
	swallowErr(errnoError(errno))
	swallowErr(rawWrite(st.hsfd, []byte{handshakeExecFail}))
	exitGroup(trampolineExecExit)
}

// rawWrite writes b to fd in one raw call, the one write the filter lets
// through for the handshake descriptor.
func rawWrite(fd int, b []byte) error {
	_, _, errno := syscall.RawSyscall(syscall.SYS_WRITE, uintptr(fd), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	//: nil on success.
	return errnoError(errno)
}

// errnoError is errno as an error, nil for zero.
func errnoError(errno syscall.Errno) error {
	//: success.
	if errno == 0 {
		return nil
	}
	return errno
}

// exitGroup ends the process with code through exit_group(2), which the filter
// lets through, never returning.
func exitGroup(code int) {
	//: exit_group does not return; the loop holds if a kernel ever did.
	for {
		_, _, errno := syscall.RawSyscall(syscall.SYS_EXIT_GROUP, uintptr(code), 0, 0)
		swallowErr(errnoError(errno))
	}
}
