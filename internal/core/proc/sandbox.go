package proc

// SandboxValue is the isolation a child is spawned under (Spec.Sandbox). The
// spawn applies it in two stages: the clone puts the child in its namespaces,
// then the child — re-executed from /proc/self/exe, on one locked thread —
// makes its mounts, pivots into its root, opens its Landlock paths, cuts its
// capability bounding set, sets its securebits, takes its identity, sets its
// capabilities, marks every descriptor past the ones it is given
// close-on-exec, sets no_new_privs, restricts itself with Landlock and
// installs its seccomp filter last, just before execve. A step that fails
// stops the spawn before the program runs (SandboxFailed); a part the kernel
// cannot apply is refused before the fork (SandboxUnavailable), and a platform
// without the mechanism refuses the whole value (UnsupportedPlatform). Linux
// on amd64 and arm64 applies it; every other platform refuses it.
type SandboxValue struct {
	// Namespaces are the namespaces the child is cloned into (systemd
	// PrivateUsers=, PrivatePIDs=, PrivateMounts=, PrivateIPC=, ProtectHostname=,
	// PrivateNetwork=, ProtectControlGroups=). A mount namespace is unshared
	// with every mount of it made private first, so nothing the child mounts
	// propagates back. Without NamespaceUser, an unprivileged supervisor's
	// clone is refused by the kernel.
	Namespaces Namespace
	// UIDMap maps the user namespace's uids to the supervisor's (NamespaceUser).
	// Empty maps the namespace's uid 0 to the supervisor's effective uid, one id
	// wide — the one mapping an unprivileged supervisor may write.
	UIDMap []IDMapValue
	// GIDMap maps the user namespace's gids, as UIDMap maps the uids; setgroups(2)
	// is denied in the namespace.
	GIDMap []IDMapValue
	// Mounts are made in order in the child's mount namespace, which they
	// require (NamespaceMount).
	Mounts []MountValue
	// Root, when not empty, is the absolute directory the child pivots into
	// after its Mounts (systemd RootDirectory=, through pivot_root(2)); the old
	// root is detached. Spec.Path and Spec.Dir are then paths inside it, and
	// Spec.Path must hold a '/'. It requires NamespaceMount.
	Root string
	// Capabilities, when non-nil, bounds the child's capabilities; nil leaves
	// them as inherited.
	Capabilities *CapabilitiesValue
	// NoNewPrivileges sets no_new_privs (systemd NoNewPrivileges=): no execve of
	// the child or its descendants gains a privilege. Landlock and Seccomp set
	// it whatever this says, as the kernel requires of an unprivileged caller.
	NoNewPrivileges bool
	// Landlock, when non-nil, restricts the child's access to the filesystem.
	Landlock *LandlockValue
	// Seccomp, when non-nil, filters the child's system calls.
	Seccomp *SeccompValue
}

// Namespace is a set of Linux namespaces (SandboxValue.Namespaces), one bit per
// namespace. The zero value is no namespace.
type Namespace uint32

// IDMapValue is one line of a user namespace's uid_map or gid_map: Size ids
// from Inside in the namespace are Outside and up in the supervisor's.
type IDMapValue struct {
	// Inside is the first id in the namespace.
	Inside uint32
	// Outside is the id Inside maps to in the supervisor's namespace.
	Outside uint32
	// Size is the number of ids mapped; it must be at least 1.
	Size uint32
}

// MountKind is what a MountValue mounts. The zero value MountReadOnly makes an
// existing tree read-only.
type MountKind uint8

// MountValue is one mount the child makes in its mount namespace
// (SandboxValue.Mounts).
type MountValue struct {
	// Kind is what is mounted.
	Kind MountKind
	// Source is the directory or file a MountBind binds; empty for every other
	// kind.
	Source string
	// Target is the absolute path mounted on; it must exist.
	Target string
	// ReadOnly makes a MountBind or a MountTmpfs read-only, recursively (a
	// MountReadOnly always is).
	ReadOnly bool
}

// CapabilitiesValue is the capability state a sandboxed child execs with
// (SandboxValue.Capabilities). A mask's bit n is capability n
// (linux/capability.h: 0 CAP_CHOWN … 40 CAP_CHECKPOINT_RESTORE).
type CapabilitiesValue struct {
	// Bounding is the bounding set the child keeps (systemd
	// CapabilityBoundingSet=): every capability the kernel knows outside it is
	// dropped with PR_CAPBSET_DROP, which needs CAP_SETPCAP — as uid 0 in a user
	// namespace has. The zero value drops them all.
	Bounding uint64
	// Ambient is raised in the child's ambient set (systemd
	// AmbientCapabilities=), so a program that is not root keeps them across
	// execve; its inheritable set becomes this one. It must lie within Bounding.
	Ambient uint64
	// SecureBits, when non-zero, is set with PR_SET_SECUREBITS (systemd
	// SecureBits=), its bits those of linux/securebits.h.
	SecureBits uint32
}

// LandlockAccess is a set of Landlock filesystem access rights, one bit per
// right, numbered as LANDLOCK_ACCESS_FS_* (linux/landlock.h).
type LandlockAccess uint64

// LandlockValue restricts a child's filesystem access (SandboxValue.Landlock):
// every right in Handled is denied beneath every path but the Rules' and the
// rights they grant there; a right outside Handled is not restricted. The
// kernel's Landlock ABI decides which rights exist: one it lacks is refused
// with SandboxUnavailable unless BestEffort names it, in which case it is left
// unrestricted. The ABI in effect is LandlockABI's answer.
type LandlockValue struct {
	// Handled is the set of rights restricted; it must not be empty.
	Handled LandlockAccess
	// Rules grant rights beneath paths.
	Rules []LandlockRuleValue
	// BestEffort is the set of rights dropped rather than refused when the
	// kernel's ABI lacks them.
	BestEffort LandlockAccess
}

// LandlockRuleValue grants Access beneath Path (LANDLOCK_RULE_PATH_BENEATH): to
// the file or directory itself and to everything below it.
type LandlockRuleValue struct {
	// Path is the absolute file or directory the rule covers, opened by the
	// child after its mounts and its pivot.
	Path string
	// Access is the set of rights granted beneath Path; it must lie within
	// LandlockValue.Handled.
	Access LandlockAccess
}

// SeccompAction is what a seccomp filter does with a system call. The zero
// value SeccompKill kills the process.
type SeccompAction uint8

// SeccompValue is a child's system call filter (SandboxValue.Seccomp, systemd
// SystemCallFilter= and SystemCallErrorNumber=): the calls named in Syscalls
// take Action, every other call Default. A call from another architecture's
// ABI — or the x32 ABI on amd64 — kills the process. execve, exit, exit_group,
// rt_sigreturn and the spawn's own failure report always pass, since the
// filter is installed before the child's program is executed: a Syscalls entry
// naming one of the first four with an action other than SeccompAllow is
// InvalidSpec.
type SeccompValue struct {
	// Default is the action of every call Syscalls does not name.
	Default SeccompAction
	// Syscalls are the calls Action applies to, by their Linux names ("mkdirat").
	// A name no supported architecture knows is InvalidSpec; one this
	// architecture lacks matches nothing.
	Syscalls []string
	// Action is the action of the calls Syscalls names; it must differ from
	// Default.
	Action SeccompAction
	// Errno is the error number SeccompErrno returns, 1 to 4095; zero is EPERM.
	Errno int
}

const (
	// NamespaceUser clones the child into a new user namespace (CLONE_NEWUSER).
	NamespaceUser Namespace = 1
	// NamespacePID clones the child into a new PID namespace, as its PID 1 (CLONE_NEWPID).
	NamespacePID Namespace = 2
	// NamespaceMount gives the child a private copy of the mount namespace (CLONE_NEWNS).
	NamespaceMount Namespace = 4
	// NamespaceIPC clones the child into a new IPC namespace (CLONE_NEWIPC).
	NamespaceIPC Namespace = 8
	// NamespaceUTS clones the child into a new UTS namespace (CLONE_NEWUTS).
	NamespaceUTS Namespace = 16
	// NamespaceNet clones the child into a new network namespace, with a loopback only (CLONE_NEWNET).
	NamespaceNet Namespace = 32
	// NamespaceCgroup puts the child in a new cgroup namespace (CLONE_NEWCGROUP),
	// rooted at the group it runs in, Spec.CgroupPath's when one is set — unshared
	// once the child is placed there.
	NamespaceCgroup Namespace = 64
	// NamespaceTime clones the child into a new time namespace (CLONE_NEWTIME, Linux 5.6).
	NamespaceTime Namespace = 128
	// MountReadOnly binds Target onto itself and makes it read-only, recursively (systemd ReadOnlyPaths=) — the zero value.
	MountReadOnly MountKind = 0
	// MountBind binds Source onto Target, recursively (systemd BindPaths=).
	MountBind MountKind = 1
	// MountTmpfs mounts an empty tmpfs on Target, nosuid and nodev (systemd TemporaryFileSystem=).
	MountTmpfs MountKind = 2
	// MountTmpfsSticky mounts an empty tmpfs on Target, mode 1777 — sticky, writable by all, as /tmp is —, nosuid and nodev (systemd PrivateTmp=).
	MountTmpfsSticky MountKind = 3
	// LandlockExecute is LANDLOCK_ACCESS_FS_EXECUTE (ABI 1).
	LandlockExecute LandlockAccess = 1
	// LandlockWriteFile is LANDLOCK_ACCESS_FS_WRITE_FILE (ABI 1).
	LandlockWriteFile LandlockAccess = 2
	// LandlockReadFile is LANDLOCK_ACCESS_FS_READ_FILE (ABI 1).
	LandlockReadFile LandlockAccess = 4
	// LandlockReadDir is LANDLOCK_ACCESS_FS_READ_DIR (ABI 1).
	LandlockReadDir LandlockAccess = 8
	// LandlockRemoveDir is LANDLOCK_ACCESS_FS_REMOVE_DIR (ABI 1).
	LandlockRemoveDir LandlockAccess = 16
	// LandlockRemoveFile is LANDLOCK_ACCESS_FS_REMOVE_FILE (ABI 1).
	LandlockRemoveFile LandlockAccess = 32
	// LandlockMakeChar is LANDLOCK_ACCESS_FS_MAKE_CHAR (ABI 1).
	LandlockMakeChar LandlockAccess = 64
	// LandlockMakeDir is LANDLOCK_ACCESS_FS_MAKE_DIR (ABI 1).
	LandlockMakeDir LandlockAccess = 128
	// LandlockMakeReg is LANDLOCK_ACCESS_FS_MAKE_REG (ABI 1).
	LandlockMakeReg LandlockAccess = 256
	// LandlockMakeSock is LANDLOCK_ACCESS_FS_MAKE_SOCK (ABI 1).
	LandlockMakeSock LandlockAccess = 512
	// LandlockMakeFifo is LANDLOCK_ACCESS_FS_MAKE_FIFO (ABI 1).
	LandlockMakeFifo LandlockAccess = 1024
	// LandlockMakeBlock is LANDLOCK_ACCESS_FS_MAKE_BLOCK (ABI 1).
	LandlockMakeBlock LandlockAccess = 2048
	// LandlockMakeSym is LANDLOCK_ACCESS_FS_MAKE_SYM (ABI 1).
	LandlockMakeSym LandlockAccess = 4096
	// LandlockRefer is LANDLOCK_ACCESS_FS_REFER (ABI 2, Linux 5.19).
	LandlockRefer LandlockAccess = 8192
	// LandlockTruncate is LANDLOCK_ACCESS_FS_TRUNCATE (ABI 3, Linux 6.2).
	LandlockTruncate LandlockAccess = 16384
	// LandlockIoctlDev is LANDLOCK_ACCESS_FS_IOCTL_DEV (ABI 5, Linux 6.10).
	LandlockIoctlDev LandlockAccess = 32768
	// SeccompKill kills the whole process (SECCOMP_RET_KILL_PROCESS, Linux 4.14) — the zero value.
	SeccompKill SeccompAction = 0
	// SeccompErrno fails the call with SeccompValue.Errno (SECCOMP_RET_ERRNO).
	SeccompErrno SeccompAction = 1
	// SeccompAllow lets the call through (SECCOMP_RET_ALLOW).
	SeccompAllow SeccompAction = 2
)
