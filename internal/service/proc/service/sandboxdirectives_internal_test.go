//go:build !windows

package service

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The confinement directives of systemd.exec(5) resolved into the sandbox
// every command is spawned under (S2 wired into the engine).

// sandboxHere plays a platform proc/exec sandboxes on, and a supervisor that
// is root — no implicit user namespace —, for the test's length.
func sandboxHere(t *testing.T) {
	t.Helper()
	platform, euid := sandboxPlatform, supervisorEUID
	sandboxPlatform = func() bool { return true }
	supervisorEUID = func() int { return 0 }
	t.Cleanup(func() { sandboxPlatform, supervisorEUID = platform, euid })
}

// resolved is the sandbox cfg's ExecStart= runs in.
func resolved(t *testing.T, cfg Config) *coreproc.SandboxValue {
	t.Helper()
	if cfg.Name == "" {
		cfg.Name = "unit"
	}
	if cfg.ExecStart == nil {
		cfg.ExecStart = []coresvc.CommandValue{cmd(t, "/bin/main")}
	}
	s, err := resolve(&cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return s.sandboxFor(listStart)
}

// fakeRoot is a root directory holding usr and boot, and no efi.
func fakeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"usr", "boot", "etc", "srv", "tmp", "var/tmp", "data"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestSandboxDirectivesResolve: each directive lands on the field of the
// sandbox that carries it.
func TestSandboxDirectivesResolve(t *testing.T) {
	sandboxHere(t)
	ro := func(p string) coreproc.MountValue {
		return coreproc.MountValue{Kind: coreproc.MountReadOnly, Target: p}
	}
	root := fakeRoot(t)
	bounding, nnp := uint64(1<<10|1<<12), true
	cases := []struct {
		name string
		cfg  Config
		want coreproc.SandboxValue
	}{
		{"NoNewPrivileges", Config{NoNewPrivileges: nnp}, coreproc.SandboxValue{NoNewPrivileges: true}},
		{"PrivateUsers", Config{PrivateUsers: true}, coreproc.SandboxValue{Namespaces: coreproc.NamespaceUser}},
		{"PrivateNetwork", Config{PrivateNetwork: true}, coreproc.SandboxValue{Namespaces: coreproc.NamespaceNet}},
		{"PrivatePIDs", Config{PrivatePIDs: true}, coreproc.SandboxValue{Namespaces: coreproc.NamespacePID}},
		{"ProtectHostname", Config{ProtectHostname: true}, coreproc.SandboxValue{Namespaces: coreproc.NamespaceUTS}},
		{"PrivateIPC", Config{PrivateIPC: true}, coreproc.SandboxValue{Namespaces: coreproc.NamespaceIPC}},
		{"ProtectSystem=yes", Config{ProtectSystem: coresvc.ProtectSystemYes, RootDirectory: root}, coreproc.SandboxValue{
			Namespaces: coreproc.NamespaceMount, Root: root, Mounts: []coreproc.MountValue{ro(root + "/boot"), ro(root + "/usr")},
		}},
		{"ProtectSystem=full", Config{ProtectSystem: coresvc.ProtectSystemFull, RootDirectory: root}, coreproc.SandboxValue{
			Namespaces: coreproc.NamespaceMount, Root: root, Mounts: []coreproc.MountValue{ro(root + "/boot"), ro(root + "/etc"), ro(root + "/usr")},
		}},
		{"ReadOnlyPaths", Config{ReadOnlyPaths: []string{"/srv/b", "/srv/a"}}, coreproc.SandboxValue{
			Namespaces: coreproc.NamespaceMount, Mounts: []coreproc.MountValue{ro("/srv/a"), ro("/srv/b")},
		}},
		{"BindPaths", Config{
			BindPaths:         []coresvc.BindPathValue{{Source: "/srv/data", Target: "/data"}, {Source: "/opt"}},
			BindReadOnlyPaths: []coresvc.BindPathValue{{Source: "/etc/ssl", Target: "/data/ssl"}},
		}, coreproc.SandboxValue{Namespaces: coreproc.NamespaceMount, Mounts: []coreproc.MountValue{
			{Kind: coreproc.MountBind, Source: "/srv/data", Target: "/data"},
			{Kind: coreproc.MountBind, Source: "/etc/ssl", Target: "/data/ssl", ReadOnly: true},
			{Kind: coreproc.MountBind, Source: "/opt", Target: "/opt"},
		}}},
		{
			"TemporaryFileSystem",
			Config{TemporaryFileSystem: []coresvc.TemporaryFileSystemValue{{Path: "/var", ReadOnly: true}, {Path: "/srv"}}},
			coreproc.SandboxValue{Namespaces: coreproc.NamespaceMount, Mounts: []coreproc.MountValue{
				{Kind: coreproc.MountTmpfs, Target: "/srv"}, {Kind: coreproc.MountTmpfs, Target: "/var", ReadOnly: true},
			}},
		},
		{"PrivateTmp=disconnected", Config{PrivateTmp: coresvc.PrivateTmpDisconnected}, coreproc.SandboxValue{
			Namespaces: coreproc.NamespaceMount, Mounts: []coreproc.MountValue{
				{Kind: coreproc.MountTmpfsSticky, Target: "/tmp"}, {Kind: coreproc.MountTmpfsSticky, Target: "/var/tmp"},
			},
		}},
		{"RootDirectory", Config{
			RootDirectory: root, PrivateTmp: coresvc.PrivateTmpDisconnected, ReadOnlyPaths: []string{"/srv"},
			BindPaths:           []coresvc.BindPathValue{{Source: "/srv", Target: "/data"}},
			TemporaryFileSystem: []coresvc.TemporaryFileSystemValue{{Path: "/srv"}},
		}, coreproc.SandboxValue{Namespaces: coreproc.NamespaceMount, Root: root, Mounts: rootMounts(root)}},
		{
			"CapabilityBoundingSet",
			Config{CapabilityBoundingSet: &bounding, AmbientCapabilities: 1 << 10, SecureBits: 1 << 4},
			coreproc.SandboxValue{Capabilities: &coreproc.CapabilitiesValue{Bounding: bounding, Ambient: 1 << 10, SecureBits: 1 << 4}},
		},
		{
			"AmbientCapabilities",
			Config{AmbientCapabilities: 1 << 10},
			coreproc.SandboxValue{Capabilities: &coreproc.CapabilitiesValue{Bounding: ^uint64(0), Ambient: 1 << 10}},
		},
		{
			"SecureBits",
			Config{SecureBits: 1},
			coreproc.SandboxValue{Capabilities: &coreproc.CapabilitiesValue{Bounding: ^uint64(0), SecureBits: 1}},
		},
		{
			"SystemCallFilter",
			Config{SystemCallFilter: coresvc.SystemCallFilterValue{Syscalls: []string{"read", "write"}}, SystemCallArchitectures: []string{"native"}},
			coreproc.SandboxValue{Seccomp: &coreproc.SeccompValue{Default: coreproc.SeccompKill, Syscalls: []string{"read", "write"}, Action: coreproc.SeccompAllow}},
		},
		{
			"SystemCallFilter+ErrorNumber",
			Config{SystemCallFilter: coresvc.SystemCallFilterValue{Syscalls: []string{"read"}}, SystemCallErrorNumber: int(syscall.EACCES)},
			coreproc.SandboxValue{Seccomp: &coreproc.SeccompValue{Default: coreproc.SeccompErrno, Syscalls: []string{"read"}, Action: coreproc.SeccompAllow, Errno: int(syscall.EACCES)}},
		},
		{
			"SystemCallFilter=~",
			Config{SystemCallFilter: coresvc.SystemCallFilterValue{Syscalls: []string{"mkdirat"}, Deny: true}},
			coreproc.SandboxValue{Seccomp: &coreproc.SeccompValue{Default: coreproc.SeccompAllow, Syscalls: []string{"mkdirat"}, Action: coreproc.SeccompKill}},
		},
		{
			"SystemCallFilter=~+ErrorNumber",
			Config{SystemCallFilter: coresvc.SystemCallFilterValue{Syscalls: []string{"mkdirat"}, Deny: true}, SystemCallErrorNumber: int(syscall.EPERM)},
			coreproc.SandboxValue{Seccomp: &coreproc.SeccompValue{Default: coreproc.SeccompAllow, Syscalls: []string{"mkdirat"}, Action: coreproc.SeccompErrno, Errno: int(syscall.EPERM)}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolved(t, c.cfg)
			if got == nil || !reflect.DeepEqual(*got, c.want) {
				t.Fatalf("sandbox\n got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

// rootMounts are the mounts of TestSandboxDirectivesResolve's RootDirectory
// case, sorted by path: the root's own beneath it, the host's /srv where it
// falls.
func rootMounts(root string) []coreproc.MountValue {
	beneath := []coreproc.MountValue{
		{Kind: coreproc.MountBind, Source: "/srv", Target: root + "/data"},
		{Kind: coreproc.MountTmpfs, Target: root + "/srv"},
		{Kind: coreproc.MountTmpfsSticky, Target: root + "/tmp"},
		{Kind: coreproc.MountTmpfsSticky, Target: root + "/var/tmp"},
	}
	host := coreproc.MountValue{Kind: coreproc.MountReadOnly, Target: "/srv"}
	if root < "/srv" {
		return append(beneath, host)
	}
	return append([]coreproc.MountValue{host}, beneath...)
}

// TestSandboxImpliesAUserNamespace: a supervisor that is not root reaches a
// namespace through its own user namespace, as systemd's per-user manager
// implies PrivateUsers=; a directive that asks for none implies none.
func TestSandboxImpliesAUserNamespace(t *testing.T) {
	sandboxHere(t)
	supervisorEUID = func() int { return 1000 }
	if got := resolved(t, Config{PrivateTmp: coresvc.PrivateTmpDisconnected}); got.Namespaces != coreproc.NamespaceMount|coreproc.NamespaceUser {
		t.Errorf("namespaces %b, want mount and user", got.Namespaces)
	}
	if got := resolved(t, Config{NoNewPrivileges: true}); got.Namespaces != 0 {
		t.Errorf("NoNewPrivileges alone took namespaces %b", got.Namespaces)
	}
}

// TestSandboxMergesTheTemplate: Spec.Sandbox is the base — its namespaces,
// its mounts first, its Landlock — and is copied: a later change to the
// caller's value reaches no unit.
func TestSandboxMergesTheTemplate(t *testing.T) {
	sandboxHere(t)
	tpl := &coreproc.SandboxValue{
		Namespaces: coreproc.NamespaceUser | coreproc.NamespaceMount,
		Mounts:     []coreproc.MountValue{{Kind: coreproc.MountTmpfs, Target: "/zz"}},
		Landlock:   &coreproc.LandlockValue{Handled: coreproc.LandlockWriteFile, Rules: []coreproc.LandlockRuleValue{{Path: "/srv", Access: coreproc.LandlockWriteFile}}},
	}
	got := resolved(t, Config{Spec: coreproc.Spec{Sandbox: tpl}, PrivateNetwork: true, NoNewPrivileges: true, ReadOnlyPaths: []string{"/a"}})
	want := coreproc.SandboxValue{
		Namespaces: coreproc.NamespaceUser | coreproc.NamespaceMount | coreproc.NamespaceNet,
		Mounts:     []coreproc.MountValue{{Kind: coreproc.MountTmpfs, Target: "/zz"}, {Kind: coreproc.MountReadOnly, Target: "/a"}},
		Landlock:   tpl.Landlock, NoNewPrivileges: true,
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("merged\n got %+v\nwant %+v", *got, want)
	}
	tpl.Mounts[0].Target, tpl.Landlock.Rules[0].Path = "/changed", "/changed"
	if got.Mounts[0].Target != "/zz" || got.Landlock.Rules[0].Path != "/srv" {
		t.Errorf("the caller's value reached the unit: %+v", *got)
	}
}

// TestNoSandboxWithoutOne: a unit that sets no confinement directive and no
// Spec.Sandbox spawns as before — no sandbox, no trampoline —, its uid 0
// keeping MAINPID='s exemption.
func TestNoSandboxWithoutOne(t *testing.T) {
	cfg := Config{Name: "unit", ExecStart: []coresvc.CommandValue{cmd(t, "/bin/main")}, SystemCallErrorNumber: int(syscall.EPERM)}
	s, err := resolve(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if spec := specOf(t, &cfg, s, listStart, cfg.ExecStart[0]); spec.Sandbox != nil || s.on(setSandboxed) {
		t.Fatalf("a unit with no sandbox spawns under %+v", spec.Sandbox)
	}
}

// TestRootDirectoryStartOnly: with RootDirectoryStartOnly= the root is
// ExecStart='s alone, systemd's EXEC_APPLY_CHROOT; every other command keeps
// the rest of the sandbox in the host's view, its paths not beneath the root.
func TestRootDirectoryStartOnly(t *testing.T) {
	sandboxHere(t)
	root := fakeRoot(t)
	cfg := Config{
		Name: "unit", RootDirectory: root, RootDirectoryStartOnly: true, PrivateTmp: coresvc.PrivateTmpDisconnected,
		ExecStartPre: []coresvc.CommandValue{cmd(t, "/bin/pre")},
		ExecStart:    []coresvc.CommandValue{cmd(t, "/bin/main")},
		ExecStop:     []coresvc.CommandValue{cmd(t, "/bin/stop")},
	}
	s, err := resolve(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	main := specOf(t, &cfg, s, listStart, cfg.ExecStart[0]).Sandbox
	if main.Root != root || main.Mounts[0].Target != root+"/tmp" {
		t.Errorf("ExecStart= outside its root: %+v", *main)
	}
	for _, l := range []list{listStartPre, listStop} {
		away := specOf(t, &cfg, s, l, s.lists[l][0]).Sandbox
		if away.Root != "" || away.Mounts[0].Target != "/tmp" || away.Namespaces&coreproc.NamespaceMount == 0 {
			t.Errorf("%s: root or prefix kept, or the mounts lost: %+v", listDirectives[l], *away)
		}
	}
	cfg.RootDirectoryStartOnly = false
	s, err = resolve(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if pre := specOf(t, &cfg, s, listStartPre, cfg.ExecStartPre[0]).Sandbox; pre.Root != root {
		t.Errorf("without RootDirectoryStartOnly= ExecStartPre= is outside the root: %+v", *pre)
	}
}

// refusedBy resolves cfg and returns its refusal, failing when there is none.
func refusedBy(t *testing.T, cfg Config) error {
	t.Helper()
	cfg.Name = "unit"
	if cfg.ExecStart == nil {
		cfg.ExecStart = []coresvc.CommandValue{cmd(t, "/bin/main")}
	}
	_, err := resolve(&cfg)
	if err == nil {
		t.Fatal("resolved, want a refusal")
	}
	return err
}

// TestSandboxDirectivesRefused: what S2 cannot express, a malformed value,
// a value the template already decides, and what would hide the unit's
// notification socket are refused at NewUnit, naming the directive.
func TestSandboxDirectivesRefused(t *testing.T) {
	sandboxHere(t)
	bounding := uint64(1)
	root := fakeRoot(t)
	cases := []struct {
		name      string
		cfg       Config
		code      errs.Code
		directive string
	}{
		{"strict", Config{ProtectSystem: coresvc.ProtectSystemStrict}, coresvc.CodeDirectiveUnsupported, "ProtectSystem"},
		{"ProtectSystem past strict", Config{ProtectSystem: 9}, coresvc.CodeConfigInvalid, "ProtectSystem"},
		{"PrivateTmp=yes", Config{PrivateTmp: coresvc.PrivateTmpYes}, coresvc.CodeDirectiveUnsupported, "PrivateTmp"},
		{"PrivateTmp past disconnected", Config{PrivateTmp: 7}, coresvc.CodeConfigInvalid, "PrivateTmp"},
		{"an architecture", Config{SystemCallArchitectures: []string{"x86"}, SystemCallFilter: coresvc.SystemCallFilterValue{Syscalls: []string{"read"}}}, coresvc.CodeDirectiveUnsupported, "SystemCallArchitectures"},
		{"native without a filter", Config{SystemCallArchitectures: []string{"native"}}, coresvc.CodeDirectiveUnsupported, "SystemCallArchitectures"},
		{"a group", Config{SystemCallFilter: coresvc.SystemCallFilterValue{Syscalls: []string{"@system-service"}}}, coresvc.CodeConfigInvalid, "SystemCallFilter"},
		{"an empty name", Config{SystemCallFilter: coresvc.SystemCallFilterValue{Syscalls: []string{""}}}, coresvc.CodeConfigInvalid, "SystemCallFilter"},
		{"an errno too large", Config{SystemCallFilter: coresvc.SystemCallFilterValue{Syscalls: []string{"read"}}, SystemCallErrorNumber: 4096}, coresvc.CodeConfigInvalid, "SystemCallErrorNumber"},
		{"a relative path", Config{ReadOnlyPaths: []string{"srv"}}, coresvc.CodeConfigInvalid, "ReadOnlyPaths"},
		{"an unclean path", Config{BindPaths: []coresvc.BindPathValue{{Source: "/srv/../etc"}}}, coresvc.CodeConfigInvalid, "BindPaths"},
		{"a relative root", Config{RootDirectory: "root"}, coresvc.CodeConfigInvalid, "RootDirectory"},
		{"a relative tmpfs", Config{TemporaryFileSystem: []coresvc.TemporaryFileSystemValue{{Path: "var/"}}}, coresvc.CodeConfigInvalid, "TemporaryFileSystem"},
		{"PrivatePIDs forking", Config{PrivatePIDs: true, Type: coresvc.TypeForking, Attacher: stubAttacher{}, PIDFile: "/run/d.pid"}, coresvc.CodeConfigInvalid, "PrivatePIDs"},
		{"ambient outside bounding", Config{CapabilityBoundingSet: &bounding, AmbientCapabilities: 2}, coresvc.CodeConfigInvalid, "AmbientCapabilities"},
		{"two roots", Config{RootDirectory: root, Spec: coreproc.Spec{Sandbox: &coreproc.SandboxValue{Root: "/other"}}}, coresvc.CodeConfigInvalid, "RootDirectory"},
		{"two capability states", Config{SecureBits: 1, Spec: coreproc.Spec{Sandbox: &coreproc.SandboxValue{Capabilities: &coreproc.CapabilitiesValue{}}}}, coresvc.CodeConfigInvalid, "CapabilityBoundingSet"},
		{"two filters", Config{SystemCallFilter: coresvc.SystemCallFilterValue{Syscalls: []string{"read"}}, Spec: coreproc.Spec{Sandbox: &coreproc.SandboxValue{Seccomp: &coreproc.SeccompValue{}}}}, coresvc.CodeConfigInvalid, "SystemCallFilter"},
		{"inherited descriptors", Config{NoNewPrivileges: true, Spec: coreproc.Spec{FDs: coreproc.FDsInherited}}, coresvc.CodeConfigInvalid, "FDs"},
		{"groups in a user namespace", Config{PrivateUsers: true, Spec: coreproc.Spec{Groups: []string{"adm"}}}, coresvc.CodeConfigInvalid, "SupplementaryGroups"},
		{"a bare command in a root", Config{RootDirectory: root, ExecStart: []coresvc.CommandValue{cmd(t, "daemon")}}, coresvc.CodeConfigInvalid, "ExecStart"},
		{"the socket under PrivateTmp", Config{PrivateTmp: coresvc.PrivateTmpDisconnected, Notify: stubSocket("/tmp/sdnotify-1/notify")}, coresvc.CodeDirectiveUnsupported, "PrivateTmp"},
		{"the socket under a tmpfs", Config{TemporaryFileSystem: []coresvc.TemporaryFileSystemValue{{Path: "/run"}}, Notify: stubSocket("/run/sdnotify-1/notify")}, coresvc.CodeDirectiveUnsupported, "TemporaryFileSystem"},
		{"the socket and a root", Config{RootDirectory: root, Notify: stubSocket("/run/sdnotify-1/notify")}, coresvc.CodeDirectiveUnsupported, "RootDirectory"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := refusedBy(t, c.cfg)
			if !errs.HasCode(err, c.code) || fieldOf(err, "directive") != c.directive {
				t.Fatalf("%v, want %v naming %s", err, c.code, c.directive)
			}
		})
	}
	if _, err := resolve(&Config{Name: "unit", ExecStart: []coresvc.CommandValue{cmd(t, "/bin/main")}, PrivateTmp: coresvc.PrivateTmpDisconnected, Notify: stubSocket("/run/sdnotify-1/notify")}); err != nil {
		t.Errorf("a socket outside /tmp refused: %v", err)
	}
}

// TestSandboxRefusedOffItsPlatform: where proc/exec applies no sandbox — off
// Linux amd64 and arm64 — a confinement directive, or a template's sandbox,
// is DIRECTIVE_UNSUPPORTED at NewUnit, never a start that fails later.
func TestSandboxRefusedOffItsPlatform(t *testing.T) {
	sandboxHere(t)
	sandboxPlatform = func() bool { return false }
	for directive, cfg := range map[string]Config{
		"PrivateNetwork": {PrivateNetwork: true},
		"ProtectSystem":  {ProtectSystem: coresvc.ProtectSystemYes},
		"Sandbox":        {Spec: coreproc.Spec{Sandbox: &coreproc.SandboxValue{NoNewPrivileges: true}}},
	} {
		err := refusedBy(t, cfg)
		if !errs.HasCode(err, coresvc.CodeDirectiveUnsupported) || fieldOf(err, "directive") != directive {
			t.Errorf("%s: %v", directive, err)
		}
	}
	if _, err := resolve(&Config{Name: "unit", ExecStart: []coresvc.CommandValue{cmd(t, "/bin/main")}}); err != nil {
		t.Errorf("a unit with no sandbox refused: %v", err)
	}
}

// stubSocket is a NotifySource whose socket is at a path, never read.
type stubSocket string

func (s stubSocket) Socket() string { return string(s) }

func (stubSocket) Recv(ctx context.Context) (coresvc.NotifyValue, error) {
	return stubNotify{}.Recv(ctx)
}

func (stubSocket) Close() error { return nil }
