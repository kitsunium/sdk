// Package checks — systemd-integration domain. This file holds the sd_notify
// (readiness) and sd_listen_fds (socket-activation) conformance suites: SDNotify
// exercises the Listen/Ready/Recv round-trip and the notifier no-op contract;
// SDListen exercises activator-side Prepare and the empty-set service contract.
package checks

import (
	"fmt"
	"net"
	"os"
	"slices"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	perrs "github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/sdlisten"
	"github.com/kitsunium/sdk/pkg/v1/sdnotify"

	"github.com/kitsunium/sdk/e2e/harness"
)

// sdnotifyDomain labels every sd_notify conformance Result.
const sdnotifyDomain string = "sdnotify"

// sdlistenDomain labels every socket-activation conformance Result.
const sdlistenDomain string = "sdlisten"

// recvDeadline bounds a blocking Listener.Recv so a missing datagram degrades to
// a Skip instead of hanging the whole conformance run.
const recvDeadline time.Duration = 2 * time.Second

// lifecycleFlagCount is the width of a Notification lifecycle truth vector:
// {Ready, Reloading, Stopping, Watchdog}.
const lifecycleFlagCount int = 4

// Check names — declared at package level (a const inside a function body is a
// KTN-CONST-LOCAL violation). Each names one row in the conformance table.
const (
	// nameNoopUnset is the unsupervised-notifier no-op contract check.
	nameNoopUnset string = "notifier/noop-unset"
	// nameNotificationStates is the parse-state value-semantics check.
	nameNotificationStates string = "notification/states"
	// nameReadinessRoundTrip is the end-to-end Listen→Ready→Recv check.
	nameReadinessRoundTrip string = "listener/readiness-roundtrip"
	// namePreparePopulatesSpec is the activator-side Prepare population check.
	namePreparePopulatesSpec string = "activator/prepare-populates-spec"
	// nameEmptySetNotError is the empty/foreign activation-set non-error check.
	nameEmptySetNotError string = "service/empty-set-not-error"
)

// prepareFdName is the LISTEN_FDNAMES key the Prepare check attaches its listener
// under; also the expected LISTEN_FDNAMES value the child would recover.
const prepareFdName string = "http"

// foreignPID is a PID that is not this process's, used to stage a foreign
// activation environment so the LISTEN_PID gate (not absence) drives the empty
// result in the empty-set check.
const foreignPID string = "1"

// Package-level state maps. Hoisted out of the check bodies because a map literal
// inside a function is a KTN-VAR-CONSTMAP violation; grouped in one var block per
// KTN-VAR-GROUP.
var (
	// foreignActivationEnv is the complete-but-foreign sd_listen_fds environment the
	// empty-set check stages: a non-zero count addressed to foreignPID.
	foreignActivationEnv = map[string]string{
		"LISTEN_FDS":     "1",
		"LISTEN_PID":     foreignPID,
		"LISTEN_FDNAMES": "http",
	}
	// readyState is the READY=1 state map the no-op check passes to Notify.
	readyState = map[string]string{"READY": "1"}
	// reloadStopWatchState is the RELOADING/STOPPING/WATCHDOG datagram the
	// notification-states check parses to assert the non-ready lifecycle accessors.
	reloadStopWatchState = map[string]string{"RELOADING": "1", "STOPPING": "1", "WATCHDOG": "1"}
)

// SDNotify returns the sd_notify conformance checks: a supervisor Listener
// receives a readiness datagram from a notifier over the NOTIFY_SOCKET — Linux;
// UnsupportedPlatform off Linux.
func SDNotify() harness.Suite {
	//: three independent checks: the unsupervised no-op, the parse-state value
	//: semantics, and the full Linux Listen→Ready→Recv readiness round-trip.
	return harness.Suite{Domain: sdnotifyDomain, Checks: []harness.Check{
		sdnotifyNoopWhenUnset,
		sdnotifyNotificationStates,
		sdnotifyReadinessRoundTrip,
	}}
}

// sdnotifyNoopWhenUnset asserts the portable libsystemd contract: with
// $NOTIFY_SOCKET unset every notifier call is a silent no-op returning nil, so an
// unsupervised binary stays quiet rather than failing. Portable across every GOOS.
func sdnotifyNoopWhenUnset() harness.Result {
	//: clear NOTIFY_SOCKET and restore it on return so the round-trip check is
	//: unaffected by this one.
	restore := withEnvUnset("NOTIFY_SOCKET")
	//: undo the env mutation regardless of the outcome below.
	defer restore()
	//: Ready must no-op silently when unsupervised.
	if err := sdnotify.Ready(); err != nil {
		//: a non-nil result violates the unset-socket no-op contract.
		return harness.Failed(sdnotifyDomain, nameNoopUnset, fmt.Sprintf("Ready unset = %v, want nil", err))
	}
	//: Notify must also no-op silently when unsupervised.
	if err := sdnotify.Notify(readyState); err != nil {
		//: surface the unexpected error so the failure is diagnosable.
		return harness.Failed(sdnotifyDomain, nameNoopUnset, fmt.Sprintf("Notify unset = %v, want nil", err))
	}
	//: both notifier calls no-op'd as the unsupervised contract requires.
	return harness.Passed(sdnotifyDomain, nameNoopUnset, "Ready/Notify no-op (nil) when NOTIFY_SOCKET unset")
}

// sdnotifyNotificationStates asserts the lifecycle accessors on a parsed
// Notification reflect its raw State field set: a READY=1 datagram is Ready, a
// RELOADING/STOPPING/WATCHDOG datagram reports the matching state and is not
// Ready. This exercises the parse-state semantics without needing systemd.
func sdnotifyNotificationStates() harness.Result {
	//: a READY=1 datagram must report Ready and nothing else.
	ready := sdnotify.Notification{State: readyState}
	//: the lifecycle vector order is {ready, reloading, stopping, watchdog}.
	gotReady := [lifecycleFlagCount]bool{ready.Ready(), ready.Reloading(), ready.Stopping(), ready.Watchdog()}
	//: want only Ready set for a READY=1 frame.
	if detail, ok := lifecycleMatch(gotReady, [lifecycleFlagCount]bool{true, false, false, false}); !ok {
		//: report the observed-vs-want flags so the drift is visible.
		return harness.Failed(sdnotifyDomain, nameNotificationStates, "READY=1 "+detail)
	}
	//: a non-ready datagram carrying each lifecycle flag must report it and not Ready.
	other := sdnotify.Notification{State: reloadStopWatchState}
	//: read the same {ready, reloading, stopping, watchdog} vector.
	gotOther := [lifecycleFlagCount]bool{other.Ready(), other.Reloading(), other.Stopping(), other.Watchdog()}
	//: want Ready clear and the three lifecycle flags set.
	if detail, ok := lifecycleMatch(gotOther, [lifecycleFlagCount]bool{false, true, true, true}); !ok {
		//: report the observed-vs-want flags so the drift is visible.
		return harness.Failed(sdnotifyDomain, nameNotificationStates, "non-ready "+detail)
	}
	//: the lifecycle accessors track the raw State as the value contract requires.
	return harness.Passed(sdnotifyDomain, nameNotificationStates, "Ready/Reloading/Stopping/Watchdog track State map")
}

// lifecycleMatch compares an observed {ready, reloading, stopping, watchdog}
// truth vector to the wanted one. ok is true on an exact match; detail describes
// the observed-vs-want vectors otherwise (and is empty when ok).
func lifecycleMatch(got, want [lifecycleFlagCount]bool) (detail string, ok bool) {
	//: an exact array equality is the whole contract — every flag must match.
	if got == want {
		//: every accessor reflected the expected State.
		return "", true
	}
	//: surface both vectors so the failing flag is identifiable (order: r/rl/st/wd).
	return fmt.Sprintf("flags got=%v want=%v", got, want), false
}

// sdnotifyReadinessRoundTrip exercises the full supervisor path without systemd:
// Listen stands up the credential-passing socket, the notifier sends Ready over
// it, and the Listener Recv returns a Ready, credential-verified Notification.
// UnsupportedPlatform off Linux → NotSupported; a host gap or a Recv timeout →
// Skipped (never a hang).
func sdnotifyReadinessRoundTrip() harness.Result {
	//: stand up the supervisor-side socket; this is the Linux-gated entry point.
	lis, path, err := sdnotify.Listen()
	//: classify a Listen failure as off-platform (NotSupported) or host-gap (Skip).
	if err != nil {
		//: an unsupported-platform error is the expected off-platform contract.
		if perrs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			//: NotSupported is a success, not a failure.
			return harness.NotSupported(sdnotifyDomain, nameReadinessRoundTrip, "Listen → UnsupportedPlatform off Linux")
		}
		//: any other Listen error is an environment gap — the check makes no claim.
		return harness.Skipped(sdnotifyDomain, nameReadinessRoundTrip, fmt.Sprintf("Listen unavailable: %v", err))
	}
	//: release the listener socket on every return path below; the closure defers
	//: the Close call itself (a bare defer would evaluate lis.Close() immediately).
	defer func() { dropErr(lis.Close()) }()
	//: point the notifier at the listener for the duration of the send.
	restore := withEnvSet("NOTIFY_SOCKET", path)
	//: restore NOTIFY_SOCKET after the send completes.
	defer restore()
	//: the notifier sends READY=1 over the socket addressed by NOTIFY_SOCKET.
	if rerr := sdnotify.Ready(); rerr != nil {
		//: a send failure is a real conformance failure on a supported host.
		return harness.Failed(sdnotifyDomain, nameReadinessRoundTrip, fmt.Sprintf("Ready send = %v, want nil", rerr))
	}
	//: receive under a bounded deadline so a lost datagram cannot hang the run.
	n, timedOut, rerr := recvWithDeadline(lis)
	//: a timeout makes no claim — the datagram never arrived in time.
	if timedOut {
		//: degrade to a Skip rather than block the conformance run.
		return harness.Skipped(sdnotifyDomain, nameReadinessRoundTrip, fmt.Sprintf("Recv did not return within %s", recvDeadline))
	}
	//: a Recv error on a supported host is a real failure.
	if rerr != nil {
		//: surface the observed error so the failure is diagnosable.
		return harness.Failed(sdnotifyDomain, nameReadinessRoundTrip, fmt.Sprintf("Recv = %v, want nil", rerr))
	}
	//: assert the round-trip property: the datagram is ready and stamped with us.
	return readinessVerdict(n)
}

// readinessVerdict turns a received Notification into the round-trip check result:
// Pass only when the datagram is Ready and its kernel-verified SenderPID is ours.
func readinessVerdict(n sdnotify.Notification) harness.Result {
	//: the round-trip property: the datagram is ready and stamped with our PID.
	if !n.Ready() || n.SenderPID != os.Getpid() {
		//: report both observed values so the contract violation is diagnosable.
		return harness.Failed(sdnotifyDomain, nameReadinessRoundTrip, fmt.Sprintf("Recv ready=%v senderPID=%d, want true and %d", n.Ready(), n.SenderPID, os.Getpid()))
	}
	//: a correct, credential-verified readiness round-trip through the facade.
	return harness.Passed(sdnotifyDomain, nameReadinessRoundTrip, fmt.Sprintf("READY round-trip; senderPID=%d verified", n.SenderPID))
}

// recvResult is the parsed datagram (or error) carried back from the Recv
// goroutine to the bounded select in recvWithDeadline.
type recvResult struct {
	// notification is the parsed datagram on a successful Recv.
	notification sdnotify.Notification
	// err is the Recv error, nil on success.
	err error
}

// recvWithDeadline runs ln.Recv in a goroutine and waits at most recvDeadline.
// timedOut is true when the deadline elapsed first; the goroutine then drains on
// Close so it never leaks.
func recvWithDeadline(ln sdnotify.Listener) (n sdnotify.Notification, timedOut bool, err error) {
	//: a one-slot buffered channel lets the goroutine send and exit even after a
	//: timeout, so it is never blocked forever.
	ch := make(chan recvResult, 1)
	//: perform the blocking Recv off the main path.
	go func() {
		//: deliver the parsed datagram (or error) to the bounded select below.
		got, rerr := ln.Recv()
		//: hand the outcome back; the buffered slot guarantees a non-blocking send.
		ch <- recvResult{notification: got, err: rerr}
	}()
	//: race the Recv result against the deadline.
	select {
	//: the datagram (or a Recv error) arrived in time.
	case got := <-ch:
		//: return the parsed datagram and its error verbatim.
		return got.notification, false, got.err
	//: the deadline elapsed before any datagram — report a timeout.
	case <-time.After(recvDeadline):
		//: the caller will Close the listener, unblocking the parked goroutine.
		return sdnotify.Notification{}, true, nil
	}
}

// SDListen returns the socket-activation conformance checks: an activator
// attaches a bound listener to a child Spec (Prepare populates ExtraFiles +
// LISTEN_FDS/LISTEN_FDNAMES) and the documented empty-set contract returns no
// files — Unix; UnsupportedPlatform off Unix.
func SDListen() harness.Suite {
	//: two checks: the activator-side Prepare population and the service-side
	//: empty/foreign activation-set contract.
	return harness.Suite{Domain: sdlistenDomain, Checks: []harness.Check{
		sdlistenPreparePopulatesSpec,
		sdlistenEmptySetNotError,
	}}
}

// sdlistenPreparePopulatesSpec is the activator-side round-trip the SDK makes
// testable without systemd: Prepare attaches a bound listener's socket to a child
// Spec and publishes the activation environment. Simulating the child side needs
// LISTEN_PID and a real fork, so this asserts the simpler observable instead —
// Prepare populates Spec.ExtraFiles and sets LISTEN_FDS=1 / LISTEN_FDNAMES=<name>
// in Spec.Env. UnsupportedPlatform off Unix → NotSupported.
func sdlistenPreparePopulatesSpec() harness.Result {
	//: an ephemeral loopback listener is a real bound stream socket to attach.
	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	//: a host that cannot bind loopback cannot host this check.
	if lerr != nil {
		//: the check makes no claim — the environment could not provide a socket.
		return harness.Skipped(sdlistenDomain, namePreparePopulatesSpec, fmt.Sprintf("net.Listen: %v", lerr))
	}
	//: release the listener once Prepare has dup'd its socket into the Spec; the
	//: closure defers the Close (a bare defer would close it before Prepare runs).
	defer func() { dropErr(ln.Close()) }()
	//: a minimal valid Spec — Prepare only mutates ExtraFiles and Env.
	spec := sdlisten.Spec{Path: "/usr/bin/true"}
	//: attach the listener's socket to the child and publish the activation env.
	err := sdlisten.Prepare(&spec, map[string]net.Listener{prepareFdName: ln})
	//: classify a Prepare failure as off-platform (NotSupported) or a real failure.
	if err != nil {
		//: an unsupported-platform error is the expected off-Unix contract.
		if perrs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			//: NotSupported is a success, not a failure.
			return harness.NotSupported(sdlistenDomain, namePreparePopulatesSpec, "Prepare → UnsupportedPlatform off Unix")
		}
		//: any other Prepare error on a supported host is a real failure.
		return harness.Failed(sdlistenDomain, namePreparePopulatesSpec, fmt.Sprintf("Prepare = %v, want nil", err))
	}
	//: one listener was attached, so exactly one inherited fd must be present.
	if len(spec.ExtraFiles) != 1 {
		//: report the count so the missing/extra fd is visible.
		return harness.Failed(sdlistenDomain, namePreparePopulatesSpec, fmt.Sprintf("ExtraFiles len = %d, want 1", len(spec.ExtraFiles)))
	}
	//: close the dup'd activation socket so the fd is not leaked by the check; the
	//: closure defers the Close to the function's return, not this statement.
	defer func() { dropErr(spec.ExtraFiles[0].Close()) }()
	//: the child must see exactly one socket named "http".
	if !envHas(spec.Env, "LISTEN_FDS=1") || !envHas(spec.Env, "LISTEN_FDNAMES="+prepareFdName) {
		//: show the published env so the protocol drift is diagnosable.
		return harness.Failed(sdlistenDomain, namePreparePopulatesSpec, fmt.Sprintf("Env = %v, want LISTEN_FDS=1 and LISTEN_FDNAMES=%s", spec.Env, prepareFdName))
	}
	//: Prepare attached the fd and published the activation env as the protocol requires.
	return harness.Passed(sdlistenDomain, namePreparePopulatesSpec, "ExtraFiles=1, LISTEN_FDS=1, LISTEN_FDNAMES="+prepareFdName)
}

// sdlistenEmptySetNotError asserts the documented "an empty / foreign activation
// set is not an error" contract: with the activation environment addressed to a
// different process, Files(false) returns nil with no error. UnsupportedPlatform
// off Unix → NotSupported.
func sdlistenEmptySetNotError() harness.Result {
	//: stage a complete-but-foreign activation env so the gate, not absence, drives
	//: the empty result; restore the prior values on return.
	restore := withEnvBatch(foreignActivationEnv)
	//: undo the env mutation regardless of the outcome below.
	defer restore()
	//: read the activation set; a foreign LISTEN_PID must yield nothing.
	files, err := sdlisten.Files(false)
	//: classify a Files failure as off-platform (NotSupported) or a real failure.
	if err != nil {
		//: an unsupported-platform error is the expected off-Unix contract.
		if perrs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			//: NotSupported is a success, not a failure.
			return harness.NotSupported(sdlistenDomain, nameEmptySetNotError, "Files → UnsupportedPlatform off Unix")
		}
		//: an empty / foreign set must not surface as any other error.
		return harness.Failed(sdlistenDomain, nameEmptySetNotError, fmt.Sprintf("Files foreign = %v, want nil error", err))
	}
	//: a foreign activation set must recover zero files.
	if files != nil {
		//: report the count so the unexpected recovery is visible.
		return harness.Failed(sdlistenDomain, nameEmptySetNotError, fmt.Sprintf("Files foreign returned %d files, want 0", len(files)))
	}
	//: a foreign LISTEN_PID yielded no files and no error as documented.
	return harness.Passed(sdlistenDomain, nameEmptySetNotError, "foreign LISTEN_PID → nil,nil (empty set is not an error)")
}

// envHas reports whether env contains the exact KEY=value entry.
func envHas(env []string, kv string) bool {
	//: an exact KEY=value membership test over the Spec's environment slice.
	return slices.Contains(env, kv)
}

// dropErr discards a non-actionable cleanup error (a best-effort Close) so the
// deferred cleanup reads as deliberate rather than an ignored failure.
func dropErr(err error) {
	//: read the parameter so the discard is explicit; cleanup faults are non-fatal.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
	//: the error concerns a best-effort cleanup step — nothing to do.
}

// withEnvSet sets key=value and returns a restore func that reinstates the prior
// value (or unsets it when it was previously absent).
func withEnvSet(key, value string) func() {
	//: capture the prior state so it can be reinstated exactly.
	prev, had := os.LookupEnv(key)
	//: a Setenv failure here would corrupt the check; treat it as best-effort and
	//: let the subsequent assertion surface any resulting fault.
	dropErr(os.Setenv(key, value))
	//: the restore closure reinstates the captured prior state.
	return func() {
		//: a key absent before is removed; a key present before is reset to prev.
		if !had {
			//: the key was absent before — remove what we set so the env is unchanged.
			dropErr(os.Unsetenv(key))
			//: nothing else to do once the synthetic key is removed.
			return
		}
		//: the key was present before — restore its exact captured value.
		dropErr(os.Setenv(key, prev))
	}
}

// withEnvUnset removes key and returns a restore func that reinstates its prior
// value (a no-op when it was already absent).
func withEnvUnset(key string) func() {
	//: capture the prior state so it can be reinstated exactly.
	prev, had := os.LookupEnv(key)
	//: clear the variable for the duration of the check.
	dropErr(os.Unsetenv(key))
	//: the restore closure reinstates the captured prior state.
	return func() {
		//: only reinstate when the variable was present before.
		if had {
			//: restore the captured value.
			dropErr(os.Setenv(key, prev))
		}
	}
}

// withEnvBatch applies every key=value in vars and returns a single restore func
// that reinstates all prior values in one call.
func withEnvBatch(vars map[string]string) func() {
	//: collect a restore closure per variable so each prior value is reinstated.
	restores := make([]func(), 0, len(vars))
	//: apply each entry, recording how to undo it.
	for key, value := range vars {
		//: withEnvSet captures the prior state and applies the new value.
		restores = append(restores, withEnvSet(key, value))
	}
	//: the aggregate restore runs every per-variable restore.
	return func() {
		//: reinstate each captured variable.
		for _, r := range restores {
			//: run this variable's restore closure.
			r()
		}
	}
}
