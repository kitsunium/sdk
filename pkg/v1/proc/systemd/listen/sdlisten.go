//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/proc/systemd/listen .

// Package listen is the public facade for systemd-style socket activation
// (sd_listen_fds(3)): a service recovers already-bound listening sockets handed
// to it by an activator, and an activator hands sockets to a child it spawns.
//
// It is the symmetric companion to pkg/v1/proc/systemd/notify (readiness) and is generic to
// any networked program: zero-downtime restarts, on-demand start, and inetd-style
// launching all rest on passing a bound socket to a fresh process so no
// connection is lost and no bind races.
//
// # Service side
//
// A socket-activated process recovers its inherited listeners:
//
//	lns, err := listen.Listeners(true) // unsetEnv: true so children don't re-inherit
//	if err != nil {
//		return err // UnsupportedPlatform off Unix; LISTEN_FAILED on a bad fd
//	}
//	for _, ln := range lns {
//		go serve(ln)
//	}
//
// Files returns the raw *os.File sockets, Listeners wraps the stream sockets as
// net.Listener, and WithNames groups them by their LISTEN_FDNAMES name (duplicate
// names allowed). All honour LISTEN_PID: fds addressed to a different process are
// ignored (an empty result, not an error).
//
// # Activator side
//
// Prepare is the symmetric half — it makes the protocol self-contained and
// testable without systemd. It appends each listener's socket to the child Spec's
// inherited files and sets LISTEN_FDS / LISTEN_FDNAMES so the child recovers them:
//
//	ln, _ := net.Listen("tcp", ":8080")
//	spec := process.Spec{Path: "/usr/bin/myservice"}
//	if err := listen.Prepare(&spec, map[string]net.Listener{"http": ln}); err != nil {
//		return err
//	}
//	p, err := process.Start(ctx, spec) // the child finds the socket at fd 3
//
// LISTEN_PID is intentionally omitted by Prepare: a pre-fork activator cannot know
// the child's pid, so the receiving side accepts an absent LISTEN_PID from a
// trusted parent while still validating a present one (the systemd-set case).
//
// # Platform
//
// Socket activation relies on Unix file-descriptor inheritance. Off Unix every
// function returns the typed UnsupportedPlatform sentinel; the package compiles
// on every GOOS.
package listen
