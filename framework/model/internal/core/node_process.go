// The nodes of processes: binaries, their roles, CLI
// commands, listeners, libraries and presentations, and the profiles a role
// runs in.

package core

// Process profiles: what a role does with its process.
const (
	// ProfileServer serves HTTP until it is stopped: the platform's kit
	// product, and the only profile that opens a TCP listener.
	ProfileServer = "server"
	// ProfileCLI parses its arguments, runs once and exits with a typed
	// status. It starts no listener and no loop outlives it.
	ProfileCLI = "cli"
	// ProfileDaemon runs until it is stopped, or until it has been idle for
	// its declared time, and serves no HTTP: it answers on its listeners —
	// a private socket, a pipe — and runs its loops.
	ProfileDaemon = "daemon"
)

// Listener networks.
const (
	// NetworkUnix is a Unix-domain socket, private to its owner.
	NetworkUnix = "unix"
	// NetworkPipe is a Windows named pipe, its DACL granting its owner only.
	NetworkPipe = "pipe"
	// NetworkLocal is NetworkUnix where there is one, NetworkPipe on
	// Windows: what a product declares when it does not care which.
	NetworkLocal = "local"
)

// BinarySpec is one executable the product ships: the [NodeEntity.Binary] of a
// [KindBinary] node, naming its roles.
type BinarySpec struct {
	// Package is the Go package of its main.
	Package string `json:"package,omitempty"`
	// Roles are the IDs of its process roles, in declaration order; the
	// first is the one it runs with no role argument.
	Roles []string `json:"roles"`
}

// RoleSpec is one process role of a binary: the [NodeEntity.Role] of a
// [KindRole] node — its profile, arguments, singleton and idle stop.
type RoleSpec struct {
	// Binary is the ID of the binary that holds it.
	Binary string `json:"binary"`
	// Profile is one of the Profile constants.
	Profile string `json:"profile"`
	// Args are the leading arguments that select it — ["daemon"] for
	// "statusline daemon" —; empty for the binary's default role.
	Args []string `json:"args,omitempty"`
	// Singleton, when set, is the scope of the lock that keeps one process of
	// this role alive at a time: what the lock's name is derived from.
	Singleton string `json:"singleton,omitempty"`
	// IdleMs is how long a daemon waits without a client before it stops
	// itself; zero means it runs until stopped.
	IdleMs int64 `json:"idleMs,omitempty"`
}

// CLISpec is a short command-line command: the [NodeEntity.CLI] of a [KindCLI]
// node, the words that run it.
type CLISpec struct {
	// Path is the command's words after the binary's name: ["status"],
	// ["daemon", "stop"].
	Path []string `json:"path"`
	// Role is the ID of the role that runs it.
	Role string `json:"role,omitempty"`
}

// ListenerSpec is a listener that is not HTTP: the [NodeEntity.Listener] of a
// [KindListener] node — its network, its contract, who may connect.
type ListenerSpec struct {
	// Network is one of the Network constants.
	Network string `json:"network"`
	// Contract is the versioned contract spoken on it: "render/v1".
	Contract string `json:"contract"`
	// Peer says who may connect: "owner" (the process's own user), or the
	// declared list, never "anyone".
	Peer string `json:"peer"`
	// Role is the ID of the role that runs it.
	Role string `json:"role,omitempty"`
}

// LibrarySpec is a package shared between components (D19): the
// [NodeEntity.Library] of a [KindLibrary] node.
type LibrarySpec struct {
	// Package is its Go import path.
	Package string `json:"package"`
}

// PresentationSpec is what turns results into what a reader sees: the
// [NodeEntity.Presentation] of a [KindPresentation] node.
type PresentationSpec struct {
	// Media is what it renders: "text", "ansi", "html", "json".
	Media string `json:"media"`
}
