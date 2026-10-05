// Package proc — range 0.2.6.* (ADR 0016 core/proc block). The whole domain
// owns this single PP octet: every sentinel below is declared here and only
// wrapped (never re-Defined) by the service implementations and pkg/v1 facades.
//
// Package proc — declares the sentinels returned across the process-supervision
// domain. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form;
// service implementations and pkg/v1 facades wrap these, never re-Define them.
//
// Package proc — the ExitValue value type: the outcome of a finished process.
//
// Package proc — the Group port: a cgroup v2 control group handle.
//
// Package proc — the LimitValue value type: a soft/hard setrlimit pair.
//
// Package proc — the Listener port: the supervisor side of sd_notify.
//
// Package proc — the MemoryLimitValue value type and the MemorySource enum: what
// a derived Go soft memory limit is, and what decided it.
//
// Package proc — the NotificationValue value type: a parsed sd_notify datagram.
//
// Package proc declares the OS process-supervision contract of the SDK: the
// ports (Process, Reaper, Group, Listener), the immutable value types (Spec,
// ExitValue, LimitValue, NotificationValue, Signal, Resource), and the domain's
// complete error-sentinel set (range 0.2.6.*). It is the sixth internal/core
// sibling (ADR 0016), peer of codec / writer / crypto / logger / transform.
//
// Unlike codec and crypto, proc ships no plug-in registry: each primitive has a
// single canonical OS implementation chosen at build time by platform tag, not
// a runtime-registered scheme. Core declares the ports and value types; concrete
// behaviour lives in internal/service/proc/*; the pkg/v1/proc facades (process,
// signal, reaper, rlimit, cgroup, systemd/notify) re-export this surface.
//
// proc is stdlib-only (os, syscall, time, strconv, strings) plus
// internal/kernel/errs — no golang.org/x/sys — preserving the SDK's dep-light
// invariant. Every error returned by the domain is one of the sentinels in
// errors.go; service code wraps them with errs.Wrap and never defines new codes.
//
// Package proc — the Process port: lifetime control over a spawned process.
//
// Package proc — the Reaper port: collection of terminated child processes.
//
// Package proc — the Signal value type: a typed, platform-portable OS signal
// with Parse / String / OS bridging. The name table is platform-specific and
// lives in signal_unix.go / signal_other.go.
//
// Package proc — non-Unix signal name table (Windows): the portable subset.
//
// Package proc — Unix signal name table (linux/darwin/bsd).
//
// Package proc — the Spec value type: an immutable process spawn specification.
//
// Package proc — the StdioMode value: how a spawned child's standard streams
// (stdin, stdout, stderr) are connected.
package proc
