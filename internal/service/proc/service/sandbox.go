package service

import (
	"os"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

const (
	// archNative is the one SystemCallArchitectures= identifier S2's filter
	// expresses: it kills every ABI but the native one.
	archNative string = "native"
	// errnoMax is the largest errno SECCOMP_RET_ERRNO carries.
	errnoMax int = 4095
	// sandboxDirective names a template's own sandbox in a refusal.
	sandboxDirective string = "Sandbox"
	// allCapabilities keeps every capability in the bounding set.
	allCapabilities uint64 = ^uint64(0)
)

var (
	// sandboxPlatform reports whether proc/exec applies a sandbox here: Linux on
	// amd64 and arm64 (ADR 0217's S2 amendment); a test plays another.
	sandboxPlatform func() bool = func() bool {
		//: the two architectures whose system call tables S2 carries.
		return runtime.GOOS == osLinux && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64")
	}
	// supervisorEUID is the supervisor's effective uid; a test plays another.
	supervisorEUID func() int = os.Geteuid
	// protectedSystem are ProtectSystem='s trees, by level: yes's, then full's
	// /etc; the ones a host may lack are mounted only where they exist.
	protectedSystem = []struct {
		path     string
		level    coresvc.ProtectSystem
		optional bool
	}{
		{"/usr", coresvc.ProtectSystemYes, false},
		{"/boot", coresvc.ProtectSystemYes, true},
		{"/efi", coresvc.ProtectSystemYes, true},
		{"/etc", coresvc.ProtectSystemFull, false},
	}
	// privateTmpDirs are the directories PrivateTmp= gives a tmpfs of their own.
	privateTmpDirs = []string{"/tmp", "/var/tmp"}
)

// sandboxRefusal is a check of the sandbox directives: when bad, the
// directive and the value it refuses, CONFIG_INVALID unless why names what an
// unsupported value lacks.
type sandboxRefusal struct {
	directive, value, why string
	bad                   bool
}

// resolveSandbox resolves the confinement directives of systemd.exec(5) into
// the sandbox every command is spawned under: the template's Spec.Sandbox,
// its namespaces and its mounts first, with the directives' added. A unit
// that sets none and whose template has none has none.
func resolveSandbox(cfg *Config, s *settings) error {
	set := sandboxDirectives(cfg)
	//: nothing to confine.
	if len(set) == 0 && cfg.Spec.Sandbox == nil {
		//: no sandbox.
		return nil
	}
	//: proc/exec applies a sandbox on Linux amd64 and arm64 alone.
	if !sandboxPlatform() {
		name := sandboxDirective
		//: the first directive set, else the template's sandbox.
		if len(set) > 0 {
			name = set[0]
		}
		//: DIRECTIVE_UNSUPPORTED naming it.
		return unsupported(name, "set", runtime.GOOS+"/"+runtime.GOARCH)
	}
	//: a value the engine refuses.
	if err := checkSandbox(cfg, s); err != nil {
		//: CONFIG_INVALID or DIRECTIVE_UNSUPPORTED.
		return err
	}
	s.sandbox = buildSandbox(cfg, cfg.RootDirectory)
	s.sandboxAway = s.sandbox
	//: RootDirectoryStartOnly=: every command but ExecStart= stays in the host's view.
	if cfg.RootDirectoryStartOnly && s.sandbox.Root != "" {
		s.put(setRootStartOnly, true)
		s.sandboxAway = buildSandbox(cfg, "")
		s.sandboxAway.Root = ""
	}
	s.put(setSandboxed, true)
	//: the sandbox's shape, against the commands it runs.
	return checkSandboxed(cfg, s)
}

// sandboxDirectives names the confinement directives cfg sets, in Config's
// order.
func sandboxDirectives(cfg *Config) []string {
	directives := []struct {
		name string
		on   bool
	}{
		{"CapabilityBoundingSet", cfg.CapabilityBoundingSet != nil},
		{"RootDirectory", cfg.RootDirectory != ""},
		{"ReadOnlyPaths", len(cfg.ReadOnlyPaths) > 0},
		{"BindPaths", len(cfg.BindPaths) > 0},
		{"BindReadOnlyPaths", len(cfg.BindReadOnlyPaths) > 0},
		{"TemporaryFileSystem", len(cfg.TemporaryFileSystem) > 0},
		{"SystemCallArchitectures", len(cfg.SystemCallArchitectures) > 0},
		{"SystemCallFilter", len(cfg.SystemCallFilter.Syscalls) > 0},
		{"AmbientCapabilities", cfg.AmbientCapabilities != 0},
		{"SecureBits", cfg.SecureBits != 0},
		{"ProtectSystem", cfg.ProtectSystem != coresvc.ProtectSystemNo},
		{"PrivateTmp", cfg.PrivateTmp != coresvc.PrivateTmpNo},
		{"NoNewPrivileges", cfg.NoNewPrivileges},
		{"PrivateUsers", cfg.PrivateUsers},
		{"PrivateNetwork", cfg.PrivateNetwork},
		{"PrivatePIDs", cfg.PrivatePIDs},
		{"ProtectHostname", cfg.ProtectHostname},
		{"PrivateIPC", cfg.PrivateIPC},
	}
	var set []string
	//: each directive set.
	for _, d := range directives {
		//: named.
		if d.on {
			set = append(set, d.name)
		}
	}
	//: the directives set.
	return set
}

// checkSandbox refuses a directive value the engine does not run, a
// malformed one, and one the template's own sandbox already decides.
func checkSandbox(cfg *Config, s *settings) error {
	//: the first refusal wins.
	if err := firstRefusal(slices.Concat(valueRefusals(cfg, s), templateRefusals(cfg))); err != nil {
		//: refused, naming the directive.
		return err
	}
	checks := []func(*Config) error{checkArchitectures, checkSyscallNames, checkSandboxPaths, checkNotifySocket}
	//: each concern in turn.
	for _, check := range checks {
		//: refused.
		if err := check(cfg); err != nil {
			//: the directive named.
			return err
		}
	}
	//: every value runnable.
	return nil
}

// valueRefusals are the checks of the directives' own values: a value past
// its last constant, one S2 cannot express, an errno out of range, a
// combination systemd refuses.
func valueRefusals(cfg *Config, s *settings) []sandboxRefusal {
	filtered := len(cfg.SystemCallFilter.Syscalls) > 0
	errno := cfg.SystemCallErrorNumber
	//: in Config's order of concern.
	return []sandboxRefusal{
		{"ProtectSystem", cfg.ProtectSystem.String(), "", cfg.ProtectSystem > coresvc.ProtectSystemStrict},
		{"ProtectSystem", "strict", "no until an issue asks: a read-only root whose /dev, /proc and /sys stay writable is a recursive read-only with exceptions, which S2's mounts do not make", cfg.ProtectSystem == coresvc.ProtectSystemStrict},
		{"PrivateTmp", cfg.PrivateTmp.String(), "", cfg.PrivateTmp > coresvc.PrivateTmpDisconnected},
		{"PrivateTmp", "yes", "no until an issue asks: directories on the host shared by every command and removed after the stop; disconnected, or BindPaths= of directories the caller makes", cfg.PrivateTmp == coresvc.PrivateTmpYes},
		{"SystemCallErrorNumber", strconv.Itoa(errno), "", filtered && (errno < 0 || errno > errnoMax)},
		{"PrivatePIDs", "with Type=forking", "", cfg.PrivatePIDs && s.typ == coresvc.TypeForking},
	}
}

// templateRefusals are the checks of what Spec.Sandbox decides already — a
// root, a capability state, a filter — and of an ambient set outside the
// bounding one.
func templateRefusals(cfg *Config) []sandboxRefusal {
	tpl := cfg.Spec.Sandbox
	//: no template: nothing it decides.
	if tpl == nil {
		tpl = &coreproc.SandboxValue{}
	}
	//: each one the template or the bounding set decides.
	return []sandboxRefusal{
		{"RootDirectory", cfg.RootDirectory, "", cfg.RootDirectory != "" && tpl.Root != "" && tpl.Root != cfg.RootDirectory},
		{"CapabilityBoundingSet", "beside Spec.Sandbox.Capabilities", "", tpl.Capabilities != nil && capabilityDirectives(cfg)},
		{"AmbientCapabilities", strconv.FormatUint(cfg.AmbientCapabilities, 16), "", cfg.AmbientCapabilities&^boundingSet(cfg) != 0},
		{"SystemCallFilter", "beside Spec.Sandbox.Seccomp", "", tpl.Seccomp != nil && len(cfg.SystemCallFilter.Syscalls) > 0},
	}
}

// firstRefusal is the first bad check's error: DIRECTIVE_UNSUPPORTED when it
// says why, CONFIG_INVALID otherwise.
func firstRefusal(refusals []sandboxRefusal) error {
	//: each check.
	for _, r := range refusals {
		//: refused.
		if !r.bad {
			continue
		}
		//: a value the engine does not run.
		if r.why != "" {
			//: DIRECTIVE_UNSUPPORTED.
			return unsupported(r.directive, r.value, r.why)
		}
		//: a malformed value.
		return invalid(r.directive, r.value)
	}
	//: none.
	return nil
}

// checkArchitectures refuses every SystemCallArchitectures= identifier but
// native, and native without the filter it is a property of.
func checkArchitectures(cfg *Config) error {
	//: each identifier.
	for _, a := range cfg.SystemCallArchitectures {
		//: another ABI's: S2's filter is the native one's.
		if a != archNative {
			//: DIRECTIVE_UNSUPPORTED.
			return unsupported("SystemCallArchitectures", a, "no until an issue asks: S2's filter admits the native ABI alone")
		}
		//: no filter to check the architecture.
		if len(cfg.SystemCallFilter.Syscalls) == 0 {
			//: DIRECTIVE_UNSUPPORTED.
			return unsupported("SystemCallArchitectures", a, "a SystemCallFilter=: S2's filter checks the architecture before any call it names")
		}
	}
	//: native, filtered.
	return nil
}

// checkSyscallNames refuses an empty name and an @group, which the caller
// expands (agent#331).
func checkSyscallNames(cfg *Config) error {
	//: each name.
	for _, name := range cfg.SystemCallFilter.Syscalls {
		//: nothing, or a group.
		if name == "" || strings.HasPrefix(name, "@") {
			//: CONFIG_INVALID naming it.
			return invalid("SystemCallFilter", name)
		}
	}
	//: names proc/exec checks against its table.
	return nil
}

// checkSandboxPaths refuses a path that is not absolute and clean.
func checkSandboxPaths(cfg *Config) error {
	paths := []struct {
		directive string
		values    []string
	}{
		{"RootDirectory", optional(cfg.RootDirectory)},
		{"ReadOnlyPaths", cfg.ReadOnlyPaths},
		{"BindPaths", bindPaths(cfg.BindPaths)},
		{"BindReadOnlyPaths", bindPaths(cfg.BindReadOnlyPaths)},
		{"TemporaryFileSystem", tmpfsPaths(cfg.TemporaryFileSystem)},
	}
	//: each directive's paths.
	for _, p := range paths {
		//: each path.
		for _, v := range p.values {
			//: relative, or not in its clean form — a Linux path, whatever the host.
			if !path.IsAbs(v) || path.Clean(v) != v {
				//: CONFIG_INVALID naming it.
				return invalid(p.directive, v)
			}
		}
	}
	//: every path absolute.
	return nil
}

// checkNotifySocket refuses a sandbox that hides the unit's notification
// socket from its processes: a tmpfs over it, or a root — NOTIFY_SOCKET is
// the host's path, which a pivot moves. systemd binds its socket into the
// root; S2 makes no mount point to bind it on.
func checkNotifySocket(cfg *Config) error {
	//: no socket.
	if cfg.Notify == nil {
		//: nothing hidden.
		return nil
	}
	socket := cfg.Notify.Socket()
	//: a root: the socket's path is no longer its own.
	if cfg.RootDirectory != "" {
		//: DIRECTIVE_UNSUPPORTED naming it.
		return unsupported("RootDirectory", cfg.RootDirectory, "no until an issue asks: the notification socket "+socket+" is the host's path, which S2 cannot bind into the root")
	}
	hiders := []struct {
		directive string
		paths     []string
	}{
		{"TemporaryFileSystem", tmpfsPaths(cfg.TemporaryFileSystem)},
		{"PrivateTmp", privateTmpOf(cfg)},
	}
	//: each directive that mounts a tmpfs.
	for _, h := range hiders {
		//: each tree.
		for _, p := range h.paths {
			//: the socket beneath it.
			if beneath(socket, p) {
				//: DIRECTIVE_UNSUPPORTED naming it.
				return unsupported(h.directive, p, "the notification socket "+socket+" would be hidden: open it (ListenNotify) with TMPDIR outside the tmpfs")
			}
		}
	}
	//: the socket stays reachable.
	return nil
}

// checkSandboxed refuses what proc/exec would refuse of the resolved sandbox
// against the unit's own spec and commands: an inherited descriptor table,
// supplementary groups in a user namespace, a command a root cannot find.
func checkSandboxed(cfg *Config, s *settings) error {
	sb := s.sandbox
	//: a sandboxed child gets only the descriptors it is given.
	if cfg.Spec.FDs == coreproc.FDsInherited {
		//: CONFIG_INVALID.
		return invalid("FDs", "inherited beside a sandbox")
	}
	//: a user namespace denies setgroups(2).
	if len(cfg.Spec.Groups) > 0 && sb.Namespaces&coreproc.NamespaceUser != 0 {
		//: CONFIG_INVALID.
		return invalid("SupplementaryGroups", "in a user namespace")
	}
	//: no root: every path is looked up as usual.
	if sb.Root == "" {
		//: nothing more.
		return nil
	}
	//: each command the root reaches.
	return checkRootCommands(s)
}

// checkRootCommands refuses a command the root reaches whose program is a bare
// name: proc/exec searches PATH on the host, not in the root.
func checkRootCommands(s *settings) error {
	//: each list the root reaches.
	for l := listCondition; l < listCount; l++ {
		//: under RootDirectoryStartOnly=, ExecStart= alone.
		if l != listStart && s.on(setRootStartOnly) {
			continue
		}
		//: one command at a time.
		for _, c := range s.lists[l] {
			path := c.Path
			//: "|" runs the shell.
			if c.Flags&coresvc.FlagShell != 0 {
				path = s.shell
			}
			//: a bare name inside a root, "+" aside: the PATH search is the host's.
			if c.Flags&coresvc.FlagFullPrivileges == 0 && !strings.Contains(path, "/") {
				//: CONFIG_INVALID naming the list.
				return invalid(listDirectives[l], path)
			}
		}
	}
	//: every command reachable.
	return nil
}

// buildSandbox is the sandbox of the template with the directives', its
// paths beneath root when root is set: a new value, the template never
// written to.
func buildSandbox(cfg *Config, root string) *coreproc.SandboxValue {
	sb := cloneSandbox(cfg.Spec.Sandbox)
	mounts := directiveMounts(cfg, root)
	sb.Namespaces |= directiveNamespaces(cfg, len(mounts) > 0)
	sb.Mounts = append(sb.Mounts, mounts...)
	//: RootDirectory=.
	if cfg.RootDirectory != "" {
		sb.Root = cfg.RootDirectory
	}
	sb.NoNewPrivileges = sb.NoNewPrivileges || cfg.NoNewPrivileges
	//: the capability directives.
	if c := directiveCapabilities(cfg); c != nil {
		sb.Capabilities = c
	}
	//: SystemCallFilter=.
	if sc := directiveSeccomp(cfg); sc != nil {
		sb.Seccomp = sc
	}
	//: the sandbox.
	return sb
}

// directiveNamespaces are the namespaces the Private…= directives and
// ProtectHostname= ask for, a mount namespace for mounts or a root; a
// supervisor that is not root reaches them through its own user namespace,
// as systemd's per-user manager implies PrivateUsers=.
func directiveNamespaces(cfg *Config, mounts bool) coreproc.Namespace {
	var ns coreproc.Namespace
	flags := []struct {
		ns coreproc.Namespace
		on bool
	}{
		{coreproc.NamespaceUser, cfg.PrivateUsers},
		{coreproc.NamespaceNet, cfg.PrivateNetwork},
		{coreproc.NamespacePID, cfg.PrivatePIDs},
		{coreproc.NamespaceUTS, cfg.ProtectHostname},
		{coreproc.NamespaceIPC, cfg.PrivateIPC},
		{coreproc.NamespaceMount, mounts || cfg.RootDirectory != ""},
	}
	//: each directive set.
	for _, f := range flags {
		//: its namespace.
		if f.on {
			ns |= f.ns
		}
	}
	//: an unprivileged supervisor's way in.
	if ns != 0 && supervisorEUID() != 0 {
		ns |= coreproc.NamespaceUser
	}
	//: the namespaces.
	return ns
}

// directiveMounts are the mounts the directives make, sorted by target so a
// tree is mounted before what lies beneath it, as systemd sorts them;
// ProtectSystem=, PrivateTmp=, TemporaryFileSystem= and BindPaths='s targets
// are beneath root.
func directiveMounts(cfg *Config, root string) []coreproc.MountValue {
	var mounts []coreproc.MountValue
	//: ProtectSystem='s trees, the optional ones where they exist.
	for _, p := range protectedSystem {
		//: below the level asked, or absent.
		if cfg.ProtectSystem < p.level || (p.optional && !exists(root+p.path)) {
			continue
		}
		mounts = append(mounts, coreproc.MountValue{Kind: coreproc.MountReadOnly, Target: root + p.path})
	}
	//: ReadOnlyPaths=, the host's.
	for _, p := range cfg.ReadOnlyPaths {
		mounts = append(mounts, coreproc.MountValue{Kind: coreproc.MountReadOnly, Target: p})
	}
	mounts = append(mounts, binds(cfg.BindPaths, root, false)...)
	mounts = append(mounts, binds(cfg.BindReadOnlyPaths, root, true)...)
	//: TemporaryFileSystem=.
	for _, t := range cfg.TemporaryFileSystem {
		mounts = append(mounts, coreproc.MountValue{Kind: coreproc.MountTmpfs, Target: root + t.Path, ReadOnly: t.ReadOnly})
	}
	//: PrivateTmp=disconnected: a /tmp everyone may write, as the host's is.
	for _, p := range privateTmpOf(cfg) {
		mounts = append(mounts, coreproc.MountValue{Kind: coreproc.MountTmpfsSticky, Target: root + p})
	}
	slices.SortStableFunc(mounts, func(a, b coreproc.MountValue) int {
		//: component by component: a parent first.
		return slices.Compare(strings.Split(a.Target, "/"), strings.Split(b.Target, "/"))
	})
	//: the mounts.
	return mounts
}

// binds are BindPaths='s or BindReadOnlyPaths='s mounts, each target beneath
// root, Source where it is empty.
func binds(paths []coresvc.BindPathValue, root string, readOnly bool) []coreproc.MountValue {
	out := make([]coreproc.MountValue, 0, len(paths))
	//: each entry.
	for _, b := range paths {
		target := b.Target
		//: the source's own path.
		if target == "" {
			target = b.Source
		}
		out = append(out, coreproc.MountValue{Kind: coreproc.MountBind, Source: b.Source, Target: root + target, ReadOnly: readOnly})
	}
	//: the binds.
	return out
}

// directiveCapabilities is the capability state the directives ask for, nil
// when they ask for none; an ambient set without a bounding one keeps every
// capability in the bounding set.
func directiveCapabilities(cfg *Config) *coreproc.CapabilitiesValue {
	//: no capability directive.
	if !capabilityDirectives(cfg) {
		//: inherited.
		return nil
	}
	//: the three sets.
	return &coreproc.CapabilitiesValue{Bounding: boundingSet(cfg), Ambient: cfg.AmbientCapabilities, SecureBits: cfg.SecureBits}
}

// capabilityDirectives reports whether cfg sets CapabilityBoundingSet=,
// AmbientCapabilities= or SecureBits=.
func capabilityDirectives(cfg *Config) bool {
	//: any of the three.
	return cfg.CapabilityBoundingSet != nil || cfg.AmbientCapabilities != 0 || cfg.SecureBits != 0
}

// boundingSet is CapabilityBoundingSet=, every capability when it is unset.
func boundingSet(cfg *Config) uint64 {
	//: unset: nothing dropped.
	if cfg.CapabilityBoundingSet == nil {
		//: every capability.
		return allCapabilities
	}
	//: the caller's.
	return *cfg.CapabilityBoundingSet
}

// directiveSeccomp is SystemCallFilter='s filter, nil without a name: an
// allow-list lets the calls named through and refuses every other, a
// deny-list refuses the calls named; a refusal kills, or fails the call with
// SystemCallErrorNumber=.
func directiveSeccomp(cfg *Config) *coreproc.SeccompValue {
	f := cfg.SystemCallFilter
	//: no filter.
	if len(f.Syscalls) == 0 {
		//: none.
		return nil
	}
	refuse := coreproc.SeccompKill
	//: SystemCallErrorNumber=.
	if cfg.SystemCallErrorNumber > 0 {
		refuse = coreproc.SeccompErrno
	}
	sc := &coreproc.SeccompValue{Default: refuse, Syscalls: slices.Clone(f.Syscalls), Action: coreproc.SeccompAllow, Errno: cfg.SystemCallErrorNumber}
	//: "~": the calls named are the ones refused.
	if f.Deny {
		sc.Default, sc.Action = coreproc.SeccompAllow, refuse
	}
	//: the filter.
	return sc
}

// cloneSandbox is a copy of sb no later change to sb reaches, an empty value
// for nil.
func cloneSandbox(sb *coreproc.SandboxValue) *coreproc.SandboxValue {
	//: none to copy.
	if sb == nil {
		//: an empty sandbox to add to.
		return &coreproc.SandboxValue{}
	}
	out := *sb
	out.UIDMap, out.GIDMap, out.Mounts = slices.Clone(sb.UIDMap), slices.Clone(sb.GIDMap), slices.Clone(sb.Mounts)
	//: the capability state.
	if sb.Capabilities != nil {
		c := *sb.Capabilities
		out.Capabilities = &c
	}
	//: the Landlock rules.
	if sb.Landlock != nil {
		l := *sb.Landlock
		l.Rules = slices.Clone(sb.Landlock.Rules)
		out.Landlock = &l
	}
	//: the filter.
	if sb.Seccomp != nil {
		sc := *sb.Seccomp
		sc.Syscalls = slices.Clone(sb.Seccomp.Syscalls)
		out.Seccomp = &sc
	}
	//: the copy.
	return &out
}

// sandboxFor is the sandbox a command of list l runs in: ExecStart='s, or
// every command's but under RootDirectoryStartOnly=.
func (s *settings) sandboxFor(l list) *coreproc.SandboxValue {
	//: RootDirectoryStartOnly=: the root is ExecStart='s alone.
	if l != listStart && s.on(setRootStartOnly) {
		//: the sandbox without the root.
		return s.sandboxAway
	}
	//: the unit's sandbox.
	return s.sandbox
}

// privateTmpOf are the directories PrivateTmp= covers.
func privateTmpOf(cfg *Config) []string {
	//: disconnected.
	if cfg.PrivateTmp == coresvc.PrivateTmpDisconnected {
		//: /tmp and /var/tmp.
		return privateTmpDirs
	}
	//: none.
	return nil
}

// bindPaths are the paths of BindPaths= entries: each source and target.
func bindPaths(paths []coresvc.BindPathValue) []string {
	out := make([]string, 0, 2*len(paths))
	//: each entry.
	for _, b := range paths {
		out = append(out, b.Source)
		//: a target of its own.
		if b.Target != "" {
			out = append(out, b.Target)
		}
	}
	//: the paths.
	return out
}

// tmpfsPaths are TemporaryFileSystem='s directories.
func tmpfsPaths(paths []coresvc.TemporaryFileSystemValue) []string {
	out := make([]string, 0, len(paths))
	//: each entry.
	for _, t := range paths {
		out = append(out, t.Path)
	}
	//: the directories.
	return out
}

// optional is v alone, or nothing when it is empty.
func optional(v string) []string {
	//: unset.
	if v == "" {
		//: nothing.
		return nil
	}
	//: the one value.
	return []string{v}
}

// beneath reports whether path is dir or lies below it.
func beneath(path, dir string) bool {
	//: dir itself, or below it — "/" holds everything.
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// exists reports whether path names anything, a link not followed.
func exists(path string) bool {
	_, err := os.Lstat(path)
	//: found.
	return err == nil
}

// keyWriter spells values for a key: each one length-prefixed, so no two
// sequences of values spell alike.
type keyWriter struct {
	b strings.Builder
}

// str writes strings.
func (w *keyWriter) str(vs ...string) {
	//: each value, its length first.
	for _, v := range vs {
		w.b.WriteString(strconv.Itoa(len(v)))
		w.b.WriteByte(':')
		w.b.WriteString(v)
	}
}

// num writes numbers.
func (w *keyWriter) num(vs ...uint64) {
	//: each number in decimal.
	for _, v := range vs {
		w.str(strconv.FormatUint(v, 10))
	}
}

// flag writes booleans.
func (w *keyWriter) flag(vs ...bool) {
	//: each as "true" or "false".
	for _, v := range vs {
		w.str(strconv.FormatBool(v))
	}
}

// sandboxKey spells sb for the store's key: every field, in a fixed order,
// a part absent told from an empty one — a deterministic digest input.
func sandboxKey(sb *coreproc.SandboxValue) string {
	//: no sandbox.
	if sb == nil {
		//: its own spelling.
		return "-"
	}
	w := &keyWriter{}
	w.num(uint64(sb.Namespaces), uint64(len(sb.UIDMap)), uint64(len(sb.GIDMap)), uint64(len(sb.Mounts)))
	//: the id maps, uid's then gid's.
	for _, m := range slices.Concat(sb.UIDMap, sb.GIDMap) {
		w.num(uint64(m.Inside), uint64(m.Outside), uint64(m.Size))
	}
	//: the mounts, in order.
	for _, m := range sb.Mounts {
		w.num(uint64(m.Kind))
		w.str(m.Source, m.Target)
		w.flag(m.ReadOnly)
	}
	w.str(sb.Root)
	w.flag(sb.NoNewPrivileges, sb.Capabilities != nil, sb.Landlock != nil, sb.Seccomp != nil)
	keyCapabilities(w, sb.Capabilities)
	keyLandlock(w, sb.Landlock)
	keySeccomp(w, sb.Seccomp)
	//: the spelling.
	return w.b.String()
}

// keyCapabilities spells a capability state, nothing for none.
func keyCapabilities(w *keyWriter, c *coreproc.CapabilitiesValue) {
	//: inherited.
	if c == nil {
		return
	}
	w.num(c.Bounding, c.Ambient, uint64(c.SecureBits))
}

// keyLandlock spells a Landlock restriction, nothing for none.
func keyLandlock(w *keyWriter, l *coreproc.LandlockValue) {
	//: none.
	if l == nil {
		return
	}
	w.num(uint64(l.Handled), uint64(l.BestEffort), uint64(len(l.Rules)))
	//: each rule.
	for _, r := range l.Rules {
		w.str(r.Path)
		w.num(uint64(r.Access))
	}
}

// keySeccomp spells a filter, nothing for none.
func keySeccomp(w *keyWriter, sc *coreproc.SeccompValue) {
	//: none.
	if sc == nil {
		return
	}
	w.num(uint64(sc.Default), uint64(sc.Action), uint64(len(sc.Syscalls)))
	w.str(strconv.Itoa(sc.Errno))
	w.str(sc.Syscalls...)
}
