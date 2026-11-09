package service

import "strconv"

// ProtectSystem is ProtectSystem=: which trees of the file system a unit's
// processes see read-only. The zero is ProtectSystemNo.
type ProtectSystem uint8

const (
	// ProtectSystemNo leaves every tree writable — the zero, systemd's default.
	ProtectSystemNo ProtectSystem = iota
	// ProtectSystemYes makes /usr read-only, and /boot and /efi where they exist.
	ProtectSystemYes
	// ProtectSystemFull makes /etc read-only too.
	ProtectSystemFull
	// ProtectSystemStrict makes the whole hierarchy read-only but /dev, /proc and
	// /sys; the engine refuses it: a read-only root whose API trees stay writable
	// is a recursive read-only with exceptions, which the spawn's mounts do not
	// make.
	ProtectSystemStrict
)

// PrivateTmp is PrivateTmp=: whether a unit's processes get a /tmp and a
// /var/tmp of their own. The zero is PrivateTmpNo.
type PrivateTmp uint8

const (
	// PrivateTmpNo shares the host's /tmp and /var/tmp — the zero, systemd's default.
	PrivateTmpNo PrivateTmp = iota
	// PrivateTmpYes is systemd's yes: directories on the host's /tmp and
	// /var/tmp, shared by every command of the unit and removed after its stop.
	// The engine refuses it.
	PrivateTmpYes
	// PrivateTmpDisconnected mounts a new tmpfs, mode 1777, on /tmp and on
	// /var/tmp for each command.
	PrivateTmpDisconnected
)

// BindPathValue is one entry of BindPaths= or BindReadOnlyPaths=: Source, a
// path of the host, bound recursively onto Target, a path of the unit's view
// — beneath its RootDirectory= when it has one.
type BindPathValue struct {
	// Source is the absolute file or directory bound.
	Source string
	// Target is where it appears, absolute; empty is Source. It must exist.
	Target string
}

// TemporaryFileSystemValue is one entry of TemporaryFileSystem=: an empty
// tmpfs, mode 0755, nosuid and nodev, mounted on Path in the unit's view.
type TemporaryFileSystemValue struct {
	// Path is the absolute directory the tmpfs is mounted on; it must exist.
	Path string
	// ReadOnly is the ":ro" option.
	ReadOnly bool
}

// SystemCallFilterValue is SystemCallFilter=: the calls a unit's processes
// may make, or with Deny — systemd's "~" — the ones they may not. The zero,
// no call named, is no filter.
type SystemCallFilterValue struct {
	// Syscalls are the calls by their Linux names ("mkdirat"); the @group names
	// are the caller's to expand.
	Syscalls []string
	// Deny inverts the list: the calls named are refused, every other passes.
	Deny bool
}

var (
	// protectSystemNames are the ProtectSystem= spellings.
	protectSystemNames = [...]string{"no", "yes", "full", "strict"}
	// privateTmpNames are the PrivateTmp= spellings.
	privateTmpNames = [...]string{"no", "yes", "disconnected"}
)

// String returns the ProtectSystem= spelling ("full"), or "protectsystem(N)".
func (p ProtectSystem) String() string {
	//: a defined value has its systemd spelling.
	if int(p) < len(protectSystemNames) {
		//: the table's entry.
		return protectSystemNames[p]
	}
	//: an undefined value stays legible.
	return "protectsystem(" + strconv.Itoa(int(p)) + ")"
}

// String returns the PrivateTmp= spelling ("disconnected"), or "privatetmp(N)".
func (p PrivateTmp) String() string {
	//: a defined value has its systemd spelling.
	if int(p) < len(privateTmpNames) {
		//: the table's entry.
		return privateTmpNames[p]
	}
	//: an undefined value stays legible.
	return "privatetmp(" + strconv.Itoa(int(p)) + ")"
}
