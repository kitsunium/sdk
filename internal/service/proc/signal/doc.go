// Package signal — Unix Relay: forwards each received signal to a pid or process
// group via kill(2), where a negative Target addresses the group whose id is its
// absolute value.
//
// Package signal — Windows Relay. There is no kill(2) on Windows, so signal
// forwarding maps onto the two native delivery primitives, bound directly from
// kernel32 (syscall.NewLazyDLL, ABI cited, no golang.org/x/sys):
//
//   - a process-group target (Target < -1) → GenerateConsoleCtrlEvent, the
//     console control event that reaches every process in the group (CTRL_C for
//     SIGINT, CTRL_BREAK otherwise — the only event that can target one group);
//   - a single-pid target (Target > 0) → OpenProcess + TerminateProcess, the
//     reliable per-process delivery (Windows has no per-pid interrupt).
//
// The reserved targets 0 and -1 are refused before any delivery, exactly as the
// Unix build does, so a zero-value Target never fans out.
//
// Package signal — typed signal toolbox over os/signal: Notify subscribes to a
// set of signals on a leak-free channel, and Relay forwards received signals to
// a process or process group. The kill(2) path is platform-split into
// relay_unix.go / relay_other.go; this file holds the cross-platform Notify and
// the Target value, since os/signal itself is portable.
package signal
