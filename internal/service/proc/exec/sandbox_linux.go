//go:build linux && (amd64 || arm64)

package exec

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The kernel interfaces the parent reads before a sandboxed fork.
const (
	// procSelfExe is the path the first stage re-executes: the running binary's
	// own inode, whatever has since replaced its file on disk. No fallback.
	procSelfExe string = "/proc/self/exe"
	// procNSDir lists this process's namespaces, one link per kind the kernel has.
	procNSDir string = "/proc/self/ns"
	// procCapLastCap is the highest capability number the kernel knows.
	procCapLastCap string = "/proc/sys/kernel/cap_last_cap"
	// landlockCreateRulesetVersion is LANDLOCK_CREATE_RULESET_VERSION: the call returns the ABI.
	landlockCreateRulesetVersion uintptr = 1
	// clonenewtime is CLONE_NEWTIME, which only clone3 and unshare take.
	cloneNewTime uintptr = 0x80
	// maxCapability bounds a capability mask: bit 63.
	maxCapability uint32 = 63
	// mountKindLast is the highest MountKind.
	mountKindLast coreproc.MountKind = coreproc.MountTmpfsSticky
	// namespaceAll is every Namespace bit.
	namespaceAll coreproc.Namespace = 255
	// landlockNewestABI is the newest Landlock ABI whose rights this build names.
	landlockNewestABI int = 6
)

var (
	// landlockRights are the Landlock rights by the ABI that introduced them:
	// ABI 1 has the first thirteen, 2 adds REFER, 3 TRUNCATE, 5 IOCTL_DEV.
	landlockRights = [...]struct {
		right coreproc.LandlockAccess
		abi   int
		name  string
	}{
		{coreproc.LandlockExecute, 1, "execute"},
		{coreproc.LandlockWriteFile, 1, "write_file"},
		{coreproc.LandlockReadFile, 1, "read_file"},
		{coreproc.LandlockReadDir, 1, "read_dir"},
		{coreproc.LandlockRemoveDir, 1, "remove_dir"},
		{coreproc.LandlockRemoveFile, 1, "remove_file"},
		{coreproc.LandlockMakeChar, 1, "make_char"},
		{coreproc.LandlockMakeDir, 1, "make_dir"},
		{coreproc.LandlockMakeReg, 1, "make_reg"},
		{coreproc.LandlockMakeSock, 1, "make_sock"},
		{coreproc.LandlockMakeFifo, 1, "make_fifo"},
		{coreproc.LandlockMakeBlock, 1, "make_block"},
		{coreproc.LandlockMakeSym, 1, "make_sym"},
		{coreproc.LandlockRefer, 2, "refer"},
		{coreproc.LandlockTruncate, 3, "truncate"},
		{coreproc.LandlockIoctlDev, 5, "ioctl_dev"},
	}

	// sandboxNamespaces maps each Namespace to its clone flag and its name
	// under /proc/self/ns.
	sandboxNamespaces = [...]struct {
		ns   coreproc.Namespace
		flag uintptr
		name string
	}{
		{coreproc.NamespaceUser, syscall.CLONE_NEWUSER, "user"},
		{coreproc.NamespacePID, syscall.CLONE_NEWPID, "pid"},
		{coreproc.NamespaceMount, syscall.CLONE_NEWNS, "mnt"},
		{coreproc.NamespaceIPC, syscall.CLONE_NEWIPC, "ipc"},
		{coreproc.NamespaceUTS, syscall.CLONE_NEWUTS, "uts"},
		{coreproc.NamespaceNet, syscall.CLONE_NEWNET, "net"},
		{coreproc.NamespaceCgroup, syscall.CLONE_NEWCGROUP, "cgroup"},
		{coreproc.NamespaceTime, cloneNewTime, "time"},
	}

	// hostKernel is the running kernel, as production reads it.
	hostKernel = sandboxHost{
		procSelfExe:  procSelfExe,
		nsDir:        procNSDir,
		capLastCap:   procCapLastCap,
		landlockABI:  landlockABIProbe,
		seccomp:      seccompAvailable,
		mountSetattr: mountSetattrProbe,
		traceFD:      sandboxNoTrace,
	}

	// sandboxHostOverride, when set, replaces hostKernel: the tests' simulated
	// kernels, injected failures and trace. Never set outside a test.
	sandboxHostOverride atomic.Pointer[sandboxHost]
)

// sandboxHost is what a sandboxed spawn reads of this kernel, and the two
// hooks the package's tests drive the second stage with. Production uses
// hostKernel; a test swaps it through sandboxHostOverride.
type sandboxHost struct {
	procSelfExe  string
	nsDir        string
	capLastCap   string
	landlockABI  func() (int, syscall.Errno)
	seccomp      func() syscall.Errno
	mountSetattr func() syscall.Errno
	failStep     uint8
	traceFD      int32
}

// currentHost returns the kernel a sandboxed spawn reads.
func currentHost() *sandboxHost {
	//: a test's simulated kernel.
	if h := sandboxHostOverride.Load(); h != nil {
		return h
	}
	//: the running one.
	return &hostKernel
}

// LandlockABI returns the Landlock ABI version the running kernel implements:
// 1 on Linux 5.13, 2 on 5.19, 3 on 6.2, 4 on 6.7, 5 on 6.10, 6 on 6.12. A
// LandlockValue's rights newer than it are refused with SandboxUnavailable
// unless named best-effort. A kernel without Landlock, or with Landlock
// disabled at boot, is SandboxUnavailable; every platform but Linux on amd64
// and arm64 is UnsupportedPlatform.
func LandlockABI() (abi int, err error) {
	abi, errno := currentHost().landlockABI()
	//: ENOSYS without Landlock, EOPNOTSUPP when it is disabled.
	if errno != 0 {
		return 0, wrapSandboxUnavailable(errno, errs.String("feature", "landlock"))
	}
	//: the ABI in effect.
	return abi, nil
}

// landlockABIProbe is landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION).
func landlockABIProbe() (int, syscall.Errno) {
	r, _, errno := syscall.Syscall(sysLandlockCreateRuleset, 0, 0, landlockCreateRulesetVersion)
	//: the ABI, or why there is none.
	return int(r), errno
}

// mountSetattrProbe calls mount_setattr(2) with a size it refuses: EINVAL
// where the call exists, ENOSYS before Linux 5.12.
func mountSetattrProbe() syscall.Errno {
	_, _, errno := syscall.Syscall6(sysMountSetattr, 0, 0, 0, 0, 0, 0)
	//: present.
	if errno == syscall.EINVAL || errno == syscall.EBADF || errno == syscall.EFAULT {
		return 0
	}
	//: ENOSYS, or a refusal nobody expects.
	return errno
}

// checkSandbox refuses, before the Path is resolved and before any fork, a
// Spec.Sandbox this spawn cannot apply: InvalidSpec for a malformed value,
// SandboxUnavailable for a part this kernel lacks — the function is named in
// the feature field. A nil Sandbox passes.
func checkSandbox(spec coreproc.Spec) error {
	sb := spec.Sandbox
	//: nothing asked.
	if sb == nil {
		return nil
	}
	host := currentHost()
	checks := [...]func() error{
		func() error { return checkSandboxShape(spec) },
		func() error { return checkSandboxMounts(sb, host) },
		func() error { return checkCapabilities(sb.Capabilities, host) },
		func() error { _, _, err := landlockPlan(sb.Landlock, host); return err },
		func() error { return checkSeccomp(sb.Seccomp, host.seccomp) },
		func() error { return checkSandboxKernel(sb, host) },
	}
	//: the first refusal wins.
	for _, check := range checks {
		//: refused.
		if err := check(); err != nil {
			return err
		}
	}
	//: applicable here.
	return nil
}

// checkSandboxShape refuses the combinations no kernel could apply: an
// inherited descriptor table, unknown namespaces, id maps without a user
// namespace or mapping nothing, groups in a user namespace, a root without a
// mount namespace or a Path without a '/'.
func checkSandboxShape(spec coreproc.Spec) error {
	sb := spec.Sandbox
	contradictions := [...]struct {
		bad bool
		why string
	}{
		{spec.FDs == coreproc.FDsInherited, "a sandboxed child inherits only the descriptors it is given"},
		{sb.Namespaces&^namespaceAll != 0, "unknown namespace"},
		{!idMapsUsable(sb), "id maps need NamespaceUser, and each line maps an id"},
		{len(spec.Groups) > 0 && sb.Namespaces&coreproc.NamespaceUser != 0, "a user namespace denies setgroups: no supplementary group can be set"},
		{!rootUsable(spec), "a root is an absolute path in a mount namespace, and Path a path inside it"},
	}
	//: the first contradiction, if any.
	for _, c := range contradictions {
		//: INVALID_SPEC naming it.
		if c.bad {
			return wrapInvalidSpec(errs.String("sandbox", c.why))
		}
	}
	//: coherent.
	return nil
}

// idMapsUsable reports whether sb's id maps can be written: none, or lines of
// at least one id in a user namespace.
func idMapsUsable(sb *coreproc.SandboxValue) bool {
	//: no map: the default line.
	if len(sb.UIDMap) == 0 && len(sb.GIDMap) == 0 {
		return true
	}
	//: maps belong to a user namespace, and map something.
	return sb.Namespaces&coreproc.NamespaceUser != 0 && idMapsSized(sb.UIDMap) && idMapsSized(sb.GIDMap)
}

// rootUsable reports whether spec's root can be pivoted into: none, or an
// absolute clean path in a mount namespace with a Path that is a path.
func rootUsable(spec coreproc.Spec) bool {
	sb := spec.Sandbox
	//: no root.
	if sb.Root == "" {
		return true
	}
	//: a mount namespace, an absolute root, a path to execute inside it.
	return sb.Namespaces&coreproc.NamespaceMount != 0 && absClean(sb.Root) && strings.Contains(spec.Path, "/")
}

// idMapsSized reports whether every line of maps maps at least one id.
func idMapsSized(maps []coreproc.IDMapValue) bool {
	//: each line.
	for _, m := range maps {
		//: a line of nothing.
		if m.Size == 0 {
			return false
		}
	}
	return true
}

// absClean reports whether p is an absolute path in its clean form.
func absClean(p string) bool {
	//: "/a/b", never "a", "/a/../b" or "/a/".
	return filepath.IsAbs(p) && filepath.Clean(p) == p
}

// checkSandboxMounts refuses a mount the stage cannot make: no mount
// namespace, an unknown kind, a relative path, a source on a kind that takes
// none; and SandboxUnavailable for a read-only mount on a kernel without
// mount_setattr(2).
func checkSandboxMounts(sb *coreproc.SandboxValue, host *sandboxHost) error {
	readOnly := false
	//: each mount.
	for _, m := range sb.Mounts {
		//: malformed.
		if sb.Namespaces&coreproc.NamespaceMount == 0 || !mountWellFormed(m) {
			return wrapInvalidSpec(errs.String("sandbox", "a mount needs a mount namespace, a known kind and absolute paths"),
				errs.String("target", m.Target))
		}
		readOnly = readOnly || m.ReadOnly || m.Kind == coreproc.MountReadOnly
	}
	//: a recursive read-only needs mount_setattr (Linux 5.12).
	if !readOnly {
		return nil
	}
	errno := host.mountSetattr()
	//: refused before the fork.
	if errno != 0 {
		return wrapSandboxUnavailable(errno, errs.String("feature", "mount_setattr"))
	}
	return nil
}

// mountWellFormed reports whether m has a known kind, an absolute clean
// target, and a source exactly when it binds one, absolute and clean.
func mountWellFormed(m coreproc.MountValue) bool {
	bind := m.Kind == coreproc.MountBind
	//: a source for a bind alone.
	if bind != (m.Source != "") || (bind && !absClean(m.Source)) {
		return false
	}
	//: a kind and a target.
	return m.Kind <= mountKindLast && absClean(m.Target)
}

// checkCapabilities refuses an ambient set outside the bounding set, or naming
// a capability this kernel does not know.
func checkCapabilities(c *coreproc.CapabilitiesValue, host *sandboxHost) error {
	//: inherited unchanged.
	if c == nil {
		return nil
	}
	//: the inheritable set the ambient one needs must survive the cut.
	if c.Ambient&^c.Bounding != 0 {
		return wrapInvalidSpec(errs.String("sandbox", "an ambient capability lies outside the bounding set"))
	}
	last, err := capLastCap(host)
	//: the kernel's highest capability is unreadable.
	if err != nil {
		return wrapSandboxUnavailable(err, errs.String("feature", "cap_last_cap"))
	}
	//: a capability the kernel lacks cannot be raised.
	if last < maxCapability && c.Ambient>>(last+1) != 0 {
		return wrapSandboxUnavailable(nil, errs.String("feature", "capability"), errs.Int("cap_last_cap", int(last)))
	}
	return nil
}

// capLastCap reads the highest capability number the kernel knows.
func capLastCap(host *sandboxHost) (uint32, error) {
	raw, err := os.ReadFile(host.capLastCap)
	//: no procfs, or no such file.
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 32)
	//: a value nobody wrote.
	if err != nil || uint32(n) > maxCapability {
		return 0, syscall.EINVAL
	}
	return uint32(n), nil
}

// landlockPlan returns the rights restricted and the rules, trimmed to the
// kernel's ABI: a right it lacks is dropped when ll names it best-effort and
// refused with SandboxUnavailable otherwise; a rule left granting nothing is
// dropped. InvalidSpec for no handled right, an unknown right, a relative
// path or a rule granting what is not handled. A nil ll plans nothing.
func landlockPlan(ll *coreproc.LandlockValue, host *sandboxHost) (uint64, []sandboxRule, error) {
	//: no Landlock asked.
	if ll == nil {
		return 0, nil, nil
	}
	//: malformed.
	if err := checkLandlockShape(ll); err != nil {
		return 0, nil, err
	}
	abi, errno := host.landlockABI()
	//: no Landlock in this kernel.
	if errno != 0 {
		return 0, nil, wrapSandboxUnavailable(errno, errs.String("feature", "landlock"))
	}
	supported := landlockSupported(abi)
	//: a right newer than the ABI, not named best-effort.
	if missing := ll.Handled &^ supported &^ ll.BestEffort; missing != 0 {
		return 0, nil, wrapSandboxUnavailable(nil, errs.String("feature", "landlock:"+landlockRightName(missing)), errs.Int("abi", abi))
	}
	return uint64(ll.Handled & supported), trimLandlockRules(ll.Rules, supported), nil
}

// checkLandlockShape refuses an inert or unknown handled set and a rule with a
// relative path or a right that is not handled.
func checkLandlockShape(ll *coreproc.LandlockValue) error {
	known := landlockSupported(landlockNewestABI)
	//: inert, or naming a right nobody defined.
	if ll.Handled == 0 || ll.Handled&^known != 0 {
		return wrapInvalidSpec(errs.String("sandbox", "landlock handles no right, or an unknown one"))
	}
	//: each rule's path and rights.
	for _, r := range ll.Rules {
		//: malformed.
		if !absClean(r.Path) || r.Access == 0 || r.Access&^ll.Handled != 0 {
			return wrapInvalidSpec(errs.String("sandbox", "a landlock rule needs an absolute path and handled rights"), errs.String("path", r.Path))
		}
	}
	return nil
}

// trimLandlockRules keeps each rule's supported rights, and the rules left
// granting any.
func trimLandlockRules(rules []coreproc.LandlockRuleValue, supported coreproc.LandlockAccess) []sandboxRule {
	out := make([]sandboxRule, 0, len(rules))
	//: each rule, its rights trimmed.
	for _, r := range rules {
		//: a rule left granting nothing is no rule.
		if access := r.Access & supported; access != 0 {
			out = append(out, sandboxRule{access: uint64(access), path: r.Path})
		}
	}
	return out
}

// landlockSupported returns the rights an ABI implements.
func landlockSupported(abi int) coreproc.LandlockAccess {
	var set coreproc.LandlockAccess
	//: each right no newer than abi.
	for _, r := range landlockRights {
		//: implemented.
		if r.abi <= abi {
			set |= r.right
		}
	}
	return set
}

// landlockRightName names the lowest right of set.
func landlockRightName(set coreproc.LandlockAccess) string {
	//: in bit order.
	for _, r := range landlockRights {
		//: the first one set.
		if set&r.right != 0 {
			return r.name
		}
	}
	return "unknown"
}

// checkSandboxKernel refuses a sandbox this kernel cannot start: no
// /proc/self/exe to re-execute — no fallback is taken — or a namespace kind it
// lacks.
func checkSandboxKernel(sb *coreproc.SandboxValue, host *sandboxHost) error {
	//: the first stage execs /proc/self/exe and nothing else.
	if _, err := os.Stat(host.procSelfExe); err != nil {
		return wrapSandboxUnavailable(err, errs.String("feature", "procfs"), errs.String("path", host.procSelfExe))
	}
	//: each namespace asked.
	for _, n := range sandboxNamespaces {
		//: one the kernel was built without.
		if sb.Namespaces&n.ns != 0 {
			//: refused before the clone.
			if _, err := os.Lstat(filepath.Join(host.nsDir, n.name)); err != nil {
				return wrapSandboxUnavailable(err, errs.String("feature", "namespace:"+n.name))
			}
		}
	}
	return nil
}

// trampolineSelf returns the binary the trampoline re-executes: /proc/self/exe
// for a sandboxed spawn, which the stdlib's path-only StartProcess reaches,
// checked by checkSandbox; os.Executable's answer otherwise.
func trampolineSelf(spec coreproc.Spec) (string, error) {
	//: the running inode, never a path that may now hold another binary.
	if spec.Sandbox != nil {
		return currentHost().procSelfExe, nil
	}
	//: the limits trampoline's historical self.
	return os.Executable()
}

// attachSandbox prepares a sandboxed fork on attr: the namespaces on the
// clone, the identity taken from the clone and handed to the second stage, and
// the payload sealed in a memfd placed after the handshake pipe, its number in
// trampolineSandboxEnv. It returns the memfd, which the caller closes once the
// fork is done; nil, and attr untouched, for a spawn with no sandbox.
func attachSandbox(spec coreproc.Spec, attr *os.ProcAttr) (*os.File, error) {
	sb := spec.Sandbox
	//: an ordinary spawn.
	if sb == nil {
		return nil, nil
	}
	p, err := buildSandboxPayload(spec, attr.Sys.Credential)
	//: the same refusals checkSandbox made, should the kernel have changed.
	if err != nil {
		return nil, err
	}
	data := encodeSandbox(p)
	fd, errno := sealedPayload(data)
	//: no memfd (Linux before 3.17) or no seal.
	if errno != 0 {
		return nil, wrapSandboxUnavailable(errno, errs.String("feature", "memfd_create"))
	}
	setSandboxClone(attr.Sys, sb)
	//: the stage takes the identity itself, after the bounding set is cut.
	attr.Sys.Credential = nil
	//: a Dir inside the new root is the stage's to enter.
	if sb.Root != "" {
		attr.Dir = ""
	}
	payload := os.NewFile(uintptr(fd), sandboxMemfdName)
	attr.Env = append(attr.Env, trampolineSandboxEnv+"="+strconv.Itoa(len(attr.Files)))
	attr.Files = append(attr.Files, payload)
	return payload, nil
}

// setSandboxClone puts the namespaces on the clone: every kind but the mount
// and cgroup namespaces on the clone flags, the mount namespace unshared after
// the clone — the stdlib then makes every mount private, so nothing propagates
// back —, the cgroup namespace left to the second stage, and the user
// namespace's maps, setgroups denied.
func setSandboxClone(sys *syscall.SysProcAttr, sb *coreproc.SandboxValue) {
	//: each namespace asked.
	for _, n := range sandboxNamespaces {
		//: not asked, or the stage's: a cgroup namespace taken at the clone is
		//: rooted at the supervisor's group when the trampoline, not the clone,
		//: places the child (a kernel before 5.7, clone3 filtered), and the
		//: trampoline's write into a group outside that root is then refused.
		if sb.Namespaces&n.ns == 0 || n.ns == coreproc.NamespaceCgroup {
			continue
		}
		//: unshared, then made private.
		if n.ns == coreproc.NamespaceMount {
			sys.Unshareflags |= n.flag
			continue
		}
		sys.Cloneflags |= n.flag
	}
	//: no user namespace, no maps.
	if sb.Namespaces&coreproc.NamespaceUser == 0 {
		return
	}
	sys.UidMappings = idMappings(sb.UIDMap, os.Geteuid())
	sys.GidMappings = idMappings(sb.GIDMap, os.Getegid())
	sys.GidMappingsEnableSetgroups = false
}

// idMappings converts maps, or — empty — the namespace's 0 mapped to self.
func idMappings(maps []coreproc.IDMapValue, self int) []syscall.SysProcIDMap {
	//: the one line an unprivileged supervisor may write.
	if len(maps) == 0 {
		return []syscall.SysProcIDMap{{ContainerID: 0, HostID: self, Size: 1}}
	}
	out := make([]syscall.SysProcIDMap, len(maps))
	//: each line.
	for i, m := range maps {
		out[i] = syscall.SysProcIDMap{ContainerID: int(m.Inside), HostID: int(m.Outside), Size: int(m.Size)}
	}
	return out
}

// buildSandboxPayload assembles what the second stage applies: the cgroup
// namespace, the mounts, root and directory, the identity cred carried, the capability sets, the
// Landlock plan, the assembled filter, the parent-death signal to arm again
// after the identity changes, and the test hooks.
func buildSandboxPayload(spec coreproc.Spec, cred *syscall.Credential) (*sandboxPayload, error) {
	sb := spec.Sandbox
	host := currentHost()
	handled, rules, err := landlockPlan(sb.Landlock, host)
	//: the ABI changed under the spawn.
	if err != nil {
		return nil, err
	}
	caps, err := sandboxCapsOf(sb.Capabilities, host)
	//: cap_last_cap unreadable since checkSandbox read it.
	if err != nil {
		return nil, err
	}
	p := &sandboxPayload{
		cgroupNS:   sb.Namespaces&coreproc.NamespaceCgroup != 0,
		mounts:     sandboxMounts(sb.Mounts),
		root:       sb.Root,
		dir:        spec.Dir,
		identity:   sandboxIdentityOf(cred, sb.Namespaces&coreproc.NamespaceUser == 0),
		caps:       caps,
		noNewPrivs: sb.NoNewPrivileges || sb.Landlock != nil || sb.Seccomp != nil,
		handled:    handled,
		rules:      rules,
		pdeathsig:  uint32(spec.DieWithParent.Int()),
		parentPID:  parentToCheck(spec),
		failStep:   host.failStep,
		traceFD:    host.traceFD,
	}
	//: the filter, written against the handshake's descriptor.
	if sb.Seccomp != nil {
		p.filter = assembleSeccomp(sb.Seccomp, firstSealedFD+len(spec.ExtraFiles))
	}
	return p, nil
}

// parentToCheck is the supervisor's pid the stage compares its parent with
// after arming the parent-death signal again: none without the signal, nor in
// a PID namespace, where a child sees no parent's pid.
func parentToCheck(spec coreproc.Spec) uint32 {
	//: nothing to check.
	if spec.DieWithParent == 0 || spec.Sandbox.Namespaces&coreproc.NamespacePID != 0 {
		return 0
	}
	return uint32(os.Getpid())
}

// sandboxCapsOf converts the capability sets, with the kernel's highest
// capability; nil leaves them inherited.
func sandboxCapsOf(c *coreproc.CapabilitiesValue, host *sandboxHost) (*sandboxCaps, error) {
	//: inherited unchanged.
	if c == nil {
		return nil, nil
	}
	last, err := capLastCap(host)
	//: unreadable.
	if err != nil {
		return nil, wrapSandboxUnavailable(err, errs.String("feature", "cap_last_cap"))
	}
	return &sandboxCaps{bounding: c.Bounding, ambient: c.Ambient, secureBits: c.SecureBits, lastCap: last}, nil
}

// sandboxMounts converts the mounts to the payload's form.
func sandboxMounts(mounts []coreproc.MountValue) []sandboxMount {
	out := make([]sandboxMount, len(mounts))
	//: each mount.
	for i, m := range mounts {
		out[i] = sandboxMount{kind: uint8(m.Kind), readOnly: m.ReadOnly, source: m.Source, target: m.Target}
	}
	return out
}

// sandboxIdentityOf converts the credential the spec resolved to, nil when it
// names none; setGroups is false in the spawn's own user namespace.
func sandboxIdentityOf(cred *syscall.Credential, setGroups bool) *sandboxIdentity {
	//: the parent's identity stands.
	if cred == nil {
		return nil
	}
	//: the ids and the supplementary groups.
	return &sandboxIdentity{uid: cred.Uid, gid: cred.Gid, setGroups: setGroups, groups: slices.Clone(cred.Groups)}
}
