// Package notify — the supervisor (listener) side on Linux: SO_PASSCRED.
//
// Package notify — the supervisor (listener) side off Linux: unsupported.
//
// Package notify — the notifier (child) side: sending sd_notify datagrams.
//
// Package notify — sd_notify(3) readiness/watchdog protocol implementation.
//
// This package implements both sides of the systemd sd_notify protocol on top of
// the ports declared in internal/core/proc:
//
//   - the notifier (Notify and its lifecycle shorthands) writes a single
//     newline-separated NAME=value datagram to $NOTIFY_SOCKET; when the variable
//     is unset every notifier call is a no-op returning nil, exactly as
//     libsystemd specifies, so an unsupervised binary stays silent rather than
//     erroring;
//   - the listener (Listen) is the supervisor side: it owns an AF_UNIX datagram
//     socket, enables SO_PASSCRED so the kernel stamps each datagram with the
//     sender's verified PID, and yields one parsed NotificationValue per Recv.
//
// $NOTIFY_SOCKET is an AF_UNIX address; a leading '@' selects the abstract
// namespace and is replaced by a NUL byte before binding/connecting. The
// credential-passing listener is Linux-only (SO_PASSCRED / SCM_CREDENTIALS); on
// every other platform Listen returns coreproc.UnsupportedPlatform while the
// notifier remains fully portable.
package notify
