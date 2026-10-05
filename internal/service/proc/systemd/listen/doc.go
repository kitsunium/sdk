// Package listen — non-Unix stub. Socket activation relies on file-descriptor
// inheritance, which is a Unix mechanism, so every entry point returns the typed
// UnsupportedPlatform sentinel and an empty result rather than acting.
//
// Package listen — Unix socket activation (sd_listen_fds(3) family). The
// service side recovers listening sockets an activator passed via inherited fds
// 3.. plus the LISTEN_FDS / LISTEN_PID / LISTEN_FDNAMES environment; the
// activator side (Prepare) is the symmetric half, so the protocol is testable
// end-to-end without systemd. Fd inheritance is POSIX, so this builds on every
// Unix; non-Unix gets a stub returning UnsupportedPlatform.
package listen
