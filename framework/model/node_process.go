// The nodes of processes: binaries, their roles, CLI
// commands, listeners, libraries and presentations, and the profiles a role
// runs in.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// Process profiles: what a role does with its process.
const (
	// ProfileServer serves HTTP until it is stopped: the platform's kit
	// product, and the only profile that opens a TCP listener.
	ProfileServer string = core.ProfileServer
	// ProfileCLI parses its arguments, runs once and exits with a typed
	// status. It starts no listener and no loop outlives it.
	ProfileCLI string = core.ProfileCLI
	// ProfileDaemon runs until it is stopped, or until it has been idle for
	// its declared time, and serves no HTTP: it answers on its listeners —
	// a private socket, a pipe — and runs its loops.
	ProfileDaemon string = core.ProfileDaemon
)

// Listener networks.
const (
	// NetworkUnix is a Unix-domain socket, private to its owner.
	NetworkUnix string = core.NetworkUnix
	// NetworkPipe is a Windows named pipe, its DACL granting its owner only.
	NetworkPipe string = core.NetworkPipe
	// NetworkLocal is NetworkUnix where there is one, NetworkPipe on
	// Windows: what a product declares when it does not care which.
	NetworkLocal string = core.NetworkLocal
)

type (
	// BinaryInfo is one executable the product ships: the [Node].Binary of a
	// [KindBinary] node, naming its roles.
	BinaryInfo = core.BinarySpec
)

type (
	// RoleInfo is one process role of a binary: the [Node].Role of a
	// [KindRole] node — its profile, arguments, singleton and idle stop.
	RoleInfo = core.RoleSpec
)

type (
	// CLIInfo is a short command-line command: the [Node].CLI of a [KindCLI]
	// node, the words that run it.
	CLIInfo = core.CLISpec
)

type (
	// ListenerInfo is a listener that is not HTTP: the [Node].Listener of a
	// [KindListener] node — its network, its contract, who may connect.
	ListenerInfo = core.ListenerSpec
)

type (
	// LibraryInfo is a package shared between components (D19): the
	// [Node].Library of a [KindLibrary] node.
	LibraryInfo = core.LibrarySpec
)

type (
	// PresentationInfo is what turns results into what a reader sees: the
	// [Node].Presentation of a [KindPresentation] node.
	PresentationInfo = core.PresentationSpec
)
