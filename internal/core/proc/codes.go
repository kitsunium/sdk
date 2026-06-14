// Package proc — range 0.2.6.* (ADR 0016 core/proc block). The whole domain
// owns this single PP octet: every sentinel below is declared here and only
// wrapped (never re-Defined) by the service implementations and pkg/v1 facades.
package proc

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.6.0 - 0.2.6.255

// CodeUnsupportedPlatform identifies a primitive invoked on an operating system
// that cannot provide it (e.g. cgroup or subreaper off Linux). The no-op stub
// returns it instead of panicking.
const CodeUnsupportedPlatform errs.Code = 0x00_02_06_01 // 0.2.6.1

// CodeInvalidSpec identifies a Start call whose Spec is malformed — an empty
// Path or a contradictory attribute combination.
const CodeInvalidSpec errs.Code = 0x00_02_06_02 // 0.2.6.2

// CodeUnknownUser identifies a Spec naming a User that does not resolve to a uid
// on the host.
const CodeUnknownUser errs.Code = 0x00_02_06_03 // 0.2.6.3

// CodeUnknownGroup identifies a Spec naming a Group or supplementary group that
// does not resolve to a gid on the host.
const CodeUnknownGroup errs.Code = 0x00_02_06_04 // 0.2.6.4

// CodeSpawnFailed identifies a fork/exec failure when starting the process — a
// missing binary, a permission denial, or a kernel resource fault.
const CodeSpawnFailed errs.Code = 0x00_02_06_05 // 0.2.6.5

// CodeWaitFailed identifies a wait4 failure while reaping the leader — a host
// fault, not a normal non-zero exit (which is reported via ExitValue).
const CodeWaitFailed errs.Code = 0x00_02_06_06 // 0.2.6.6

// CodeSignalFailed identifies a kill(2) failure delivering a signal to a process
// or process group.
const CodeSignalFailed errs.Code = 0x00_02_06_07 // 0.2.6.7

// CodeStopFailed identifies a graceful-stop that could not terminate the process
// group even after escalating to SIGKILL.
const CodeStopFailed errs.Code = 0x00_02_06_08 // 0.2.6.8

// CodeUnknownSignal identifies a Parse call whose name or number is not in the
// platform signal table.
const CodeUnknownSignal errs.Code = 0x00_02_06_09 // 0.2.6.9

// CodeRelayFailed identifies a Relay that could not forward a received signal to
// its target process or group.
const CodeRelayFailed errs.Code = 0x00_02_06_0A // 0.2.6.10

// CodeSubreaperFailed identifies a prctl(PR_SET_CHILD_SUBREAPER) failure when
// arming subreaper semantics for a non-PID1 supervisor.
const CodeSubreaperFailed errs.Code = 0x00_02_06_0B // 0.2.6.11

// CodeReapFailed identifies a wait4 failure during a reap sweep other than the
// benign "no children" condition.
const CodeReapFailed errs.Code = 0x00_02_06_0C // 0.2.6.12

// CodeUnknownResource identifies an Apply call naming a Resource the platform
// does not map to an RLIMIT_* constant.
const CodeUnknownResource errs.Code = 0x00_02_06_0D // 0.2.6.13

// CodeRlimitFailed identifies a setrlimit(2) failure applying a per-process
// resource limit.
const CodeRlimitFailed errs.Code = 0x00_02_06_0E // 0.2.6.14

// CodeCgroupUnavailable identifies a cgroup operation attempted where cgroup v2
// is not mounted or not delegated to the caller.
const CodeCgroupUnavailable errs.Code = 0x00_02_06_0F // 0.2.6.15

// CodeCgroupCreateFailed identifies a failure creating a control-group directory
// under the cgroup v2 hierarchy.
const CodeCgroupCreateFailed errs.Code = 0x00_02_06_10 // 0.2.6.16

// CodeCgroupWriteFailed identifies a failure writing a controller file
// (memory.max, cpu.max, pids.max, io.max, cgroup.procs).
const CodeCgroupWriteFailed errs.Code = 0x00_02_06_11 // 0.2.6.17

// CodeCgroupDeleteFailed identifies a failure removing a control-group directory.
const CodeCgroupDeleteFailed errs.Code = 0x00_02_06_12 // 0.2.6.18

// CodeNotifyFailed identifies a failure sending an sd_notify datagram to
// $NOTIFY_SOCKET (distinct from the no-op when the variable is unset).
const CodeNotifyFailed errs.Code = 0x00_02_06_13 // 0.2.6.19

// CodeListenFailed identifies a failure creating or binding the supervisor-side
// sd_notify datagram socket.
const CodeListenFailed errs.Code = 0x00_02_06_14 // 0.2.6.20

// CodeInvalidNotification identifies a received sd_notify datagram that could not
// be parsed into NAME=value fields.
const CodeInvalidNotification errs.Code = 0x00_02_06_15 // 0.2.6.21

// CodeCredentialMismatch identifies a received datagram whose kernel-verified
// sender credentials did not match the expected supervised process.
const CodeCredentialMismatch errs.Code = 0x00_02_06_16 // 0.2.6.22

// CodeStdioCaptureFailed identifies a StdioCapture spawn whose output copier
// could not deliver the child's stdout/stderr to the caller's writer (the writer
// itself returned an error). Surfaced from Wait when the process otherwise
// exited cleanly.
const CodeStdioCaptureFailed errs.Code = 0x00_02_06_17 // 0.2.6.23
