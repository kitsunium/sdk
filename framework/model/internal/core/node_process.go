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
