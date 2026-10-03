// Package ipc — the sentinels internal/service/proc/ipc refuses with. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form. None
// quotes a path in its Public half: the path travels in the "path" field, for
// the log. The Private strings name the service package, where each condition
// is detected.
package ipc

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig is EX_CONFIG: the remedy is the deployment's, not a retry.
const exitConfig int = 78

var (
	// Misconfigured refuses a configuration no endpoint can be built from.
	Misconfigured = errs.Define(CodeMisconfigured, "MISCONFIGURED",
		"The private socket is misconfigured",
		"service/proc/ipc: empty or relative path, path beyond sun_path, or a negative UID/GID; the rule field says which",
		errs.WithExitCode(exitConfig))

	// DirectoryUnsafe refuses a socket directory another account could reach.
	DirectoryUnsafe = errs.Define(CodeDirectoryUnsafe, "DIRECTORY_UNSAFE",
		"The private socket's directory is not private",
		"service/proc/ipc: the directory is a link, not a directory, not ours, or group/world writable; the rule field says which",
		errs.WithExitCode(exitConfig))

	// InUse refuses to listen where a live process already answers.
	InUse = errs.Define(CodeInUse, "IN_USE",
		"Another process already listens on this private socket",
		"service/proc/ipc: a dial to the existing socket succeeded, so it was not removed")

	// ListenFailed reports a listen the kernel refused.
	ListenFailed = errs.Define(CodeListenFailed, "LISTEN_FAILED",
		"The private socket could not be opened",
		"service/proc/ipc: net.Listen or the chmod after it failed; the cause is in the chain")

	// PeerRefused reports a connection from an account the listener does not
	// admit. It is returned to no one but the listener's observer: the
	// connection is closed without a word.
	PeerRefused = errs.Define(CodePeerRefused, "PEER_REFUSED",
		"The peer is not allowed on this private socket",
		"service/proc/ipc: the kernel-reported UID/GID is neither ours nor in the allow-lists; uid and gid fields")

	// DialFailed reports a dial that reached no listener.
	DialFailed = errs.Define(CodeDialFailed, "DIAL_FAILED",
		"The private socket did not answer",
		"service/proc/ipc: the dial failed or timed out; the cause is in the chain")

	// EndpointForeign refuses a socket file another account owns.
	EndpointForeign = errs.Define(CodeEndpointForeign, "ENDPOINT_FOREIGN",
		"The private socket belongs to another account",
		"service/proc/ipc: the socket file's owner is not this process's user nor an allowed one; the uid field names it",
		errs.WithExitCode(exitConfig))

	// Closed reports an Accept on a closed listener.
	Closed = errs.Define(CodeClosed, "CLOSED",
		"The private socket is closed",
		"service/proc/ipc: Accept after Close")

	// PathUnsafe refuses a socket path another account could steer.
	PathUnsafe = errs.Define(CodePathUnsafe, "PATH_UNSAFE",
		"The private socket's path runs through a directory another account controls",
		"service/proc/ipc: a component above the socket's directory is a link planted where anybody can write, a directory another account owns there, or an entry anybody can replace; the path, dir, kind and container fields say which, target and uid where they apply",
		errs.WithExitCode(exitConfig))
)
