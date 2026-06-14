//go:build unix

// Package exec — the re-exec trampoline that honours Spec.Rlimits and Spec.Umask
// pre-exec. The Go runtime exposes no SysProcAttr hook to run setrlimit(2)/
// umask(2) in the child between fork and exec, so when a Spec requests either,
// Start does NOT exec the target directly: it execs this very binary
// (os.Executable()) as a tiny trampoline, passing the real target + an encoded
// limits payload in a sentinel env var. The init() below detects that var,
// applies the limits in the fresh process (before main), then execve()s the real
// target — so the target starts already under its rlimits/umask, with no cgo and
// no dependency. The limits persist across the execve.
//
// Footgun: any binary that imports this package and is run with the sentinel env
// var set will re-exec. Start sets it only on the trampoline child and strips it
// before execve, so a normal run never has it; do not set it by hand.
package exec

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// trampolineEnv is the sentinel environment variable carrying the encoded
// rlimit/umask payload to the trampoline child. Deliberately ugly + namespaced so
// it never collides with a real variable.
const trampolineEnv string = "__KITSUNIUM_SDK_PROC_TRAMPOLINE"

// Trampoline child exit codes (distinct from any the target could use before it
// execs, so a supervisor can tell a trampoline failure from a target failure).
const (
	trampolineApplyExit int = 126 // a setrlimit/umask application failed.
	trampolineExecExit  int = 127 // the execve of the real target failed.
)

// trampolineMinArgs is the minimum argv the trampoline child needs: [self, target].
const trampolineMinArgs int = 2

// decimalRadix is the base used when encoding/decoding the payload integers.
const decimalRadix int = 10

// uintBits is the bit size passed to strconv.ParseUint for the soft/hard fields,
// matching coreproc.LimitValue's uint64 members.
const uintBits int = 64

// The blank-var initialiser fires the pre-main trampoline check via package-level
// variable initialisation — the no-init idiom (KTN-FUNC-NOINIT). In any process
// that links this package the initialiser runs before main, exactly as an init()
// would but without an init() function: installTrampoline is a no-op unless the
// sentinel env var is set (only the trampoline child sees it), in which case the
// process applies the limits and execve()s the real target — never returning.
var _ = installTrampoline()

// installTrampoline acts as the trampoline when this process was spawned as one
// (the sentinel env var is present), else returns a zero marker so the
// package-level var initialiser above has a value to bind.
func installTrampoline() struct{} {
	payload := os.Getenv(trampolineEnv)
	//: a normal process has no payload — return immediately, zero overhead.
	if payload == "" {
		//: not a trampoline invocation; continue to the host's main().
		return struct{}{}
	}
	//: act as the trampoline: apply limits, then execve the real target.
	runTrampoline(payload)
	//: unreachable on success (runTrampoline execs or exits); binds the var.
	return struct{}{}
}

// runTrampoline applies the encoded limits then execve()s the real target
// (os.Args[1] with argv os.Args[2:]). It never returns on success; on any failure
// it reports a status byte to the parent (handshakeFD), writes a diagnostic to
// stderr, and exits with a distinct status.
func runTrampoline(payload string) {
	//: apply every limit token; a failure must not run the target unconfined.
	if msg := applyTrampoline(payload); msg != "" {
		//: signal the apply failure to the parent, then fail the spawn.
		reportHandshake(handshakeApplyFail)
		stderrLine("sdk trampoline: " + msg)
		os.Exit(trampolineApplyExit)
	}
	//: the trampoline argv is [self, target, targetArgv...]; need at least 2.
	if len(os.Args) < trampolineMinArgs {
		//: a malformed invocation cannot locate the target — signal exec failure.
		reportHandshake(handshakeExecFail)
		stderrLine("sdk trampoline: missing target in argv")
		os.Exit(trampolineExecExit)
	}
	//: arm the handshake fd to close on a successful execve so the parent reads
	//: EOF; if it cannot be armed, fail rather than risk the parent blocking on a
	//: descriptor the target would inherit.
	if !armHandshakeClose() {
		//: report the exec failure and exit instead of execing un-armed.
		reportHandshake(handshakeExecFail)
		stderrLine("sdk trampoline: could not arm handshake fd")
		os.Exit(trampolineExecExit)
	}
	target := os.Args[1]
	//: strip the sentinel var so the target never sees it (no re-trampoline).
	env := environWithout(os.Environ(), trampolineEnv)
	//: replace this process image with the real target, now under its limits.
	if err := syscall.Exec(target, os.Args[trampolineMinArgs:], env); err != nil {
		//: the execve failed (e.g. target not found) — signal and report.
		reportHandshake(handshakeExecFail)
		stderrLine("sdk trampoline exec " + target + ": " + err.Error())
		os.Exit(trampolineExecExit)
	}
}

// reportHandshake writes a single status byte to the handshake fd so the parent's
// Start surfaces a typed error. Best-effort: a write fault changes nothing before
// the imminent exit (the parent then falls back to the child's exit code).
func reportHandshake(code byte) {
	//: best-effort one-byte status; the parent maps it to a typed sentinel.
	if _, err := syscall.Write(handshakeFD, []byte{code}); err != nil {
		//: nothing to do — the process exits next regardless.
		return
	}
}

// armHandshakeClose marks the handshake fd close-on-exec so a successful execve
// closes it (the parent reads EOF = success) while a failed execve leaves it open
// for the failure byte. It reports whether the fd was armed.
func armHandshakeClose() bool {
	//: set FD_CLOEXEC on the handshake fd via fcntl; errno 0 means it is armed.
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(handshakeFD),
		syscall.F_SETFD, syscall.FD_CLOEXEC)
	//: a non-zero errno (only on an invalid fd) leaves the handshake un-armed.
	return errno == 0
}

// stderrLine writes a single diagnostic line to standard error, ignoring any
// write error (the trampoline is about to exit regardless).
func stderrLine(msg string) {
	//: best-effort diagnostic; a write failure changes nothing before the exit.
	if _, err := os.Stderr.WriteString(msg + "\n"); err != nil {
		//: nothing to do — the process exits next.
		return
	}
}

// applyTrampoline applies every token in the payload, returning the first failure
// message or "" on success.
func applyTrampoline(payload string) string {
	//: each ';'-separated token sets one limit (umask or one rlimit).
	for tok := range strings.SplitSeq(payload, ";") {
		//: skip the empty trailing token from the separator.
		if tok == "" {
			//: nothing between separators — move on.
			continue
		}
		//: apply the token; the first failure aborts the whole trampoline.
		if msg := applyToken(tok); msg != "" {
			//: surface the failing token's message to the caller.
			return msg
		}
	}
	//: every limit applied cleanly.
	return ""
}

// applyToken applies one payload token: "u<mask>" sets the umask, "r<num>,<soft>,
// <hard>" sets one rlimit. It returns "" on success or a diagnostic message.
func applyToken(tok string) string {
	//: the first byte tags the token kind.
	switch tok[0] {
	//: a umask token carries the decimal mask after the tag.
	case 'u':
		//: apply the file-creation mask for the child's first syscalls.
		return applyUmaskToken(tok[1:])
	//: an rlimit token carries "num,soft,hard" after the tag.
	case 'r':
		//: apply one resource limit pre-exec.
		return applyRlimitToken(tok[1:])
	//: an unknown tag is a malformed payload.
	default:
		//: never run the target on a payload we cannot fully understand.
		return "unknown trampoline token"
	}
}

// applyUmaskToken parses a decimal umask and applies it via umask(2).
func applyUmaskToken(s string) string {
	mask, err := strconv.Atoi(s)
	//: a non-integer umask is a malformed payload.
	if err != nil {
		//: report the malformed umask token.
		return "malformed umask token"
	}
	//: umask(2) returns the previous mask, which the trampoline discards.
	syscall.Umask(mask)
	//: the file-creation mask is in effect for the child.
	return ""
}

// applyRlimitToken parses "num,soft,hard" and applies it via setrlimit(2).
func applyRlimitToken(s string) string {
	numStr, rest, ok1 := strings.Cut(s, ",")
	softStr, hardStr, ok2 := strings.Cut(rest, ",")
	//: an rlimit token must carry the resource number and a soft/hard pair.
	if !ok1 || !ok2 {
		//: report the malformed rlimit token.
		return "malformed rlimit token"
	}
	num, e1 := strconv.Atoi(numStr)
	soft, e2 := strconv.ParseUint(softStr, decimalRadix, uintBits)
	hard, e3 := strconv.ParseUint(hardStr, decimalRadix, uintBits)
	//: any unparseable field makes the whole token malformed.
	if e1 != nil || e2 != nil || e3 != nil {
		//: report the malformed rlimit values.
		return "malformed rlimit values"
	}
	//: setrlimit applies the soft/hard pair to the resource pre-exec; new() takes
	//: the address of the platform-typed Rlimit without a named temporary.
	if err := syscall.Setrlimit(num, new(makeRlimit(soft, hard))); err != nil {
		//: a refused limit (e.g. raising the hard cap unprivileged) fails the spawn.
		return "setrlimit(" + numStr + "): " + err.Error()
	}
	//: the resource limit is in effect for the child.
	return ""
}

// environWithout returns env with any entry whose key is key removed.
func environWithout(env []string, key string) []string {
	out := make([]string, 0, len(env))
	//: keep every entry except the sentinel trampoline variable.
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		//: drop only the sentinel; pass every other variable to the target.
		if k != key {
			//: retain an unrelated environment entry.
			out = append(out, kv)
		}
	}
	//: the environment the real target inherits, sentinel stripped.
	return out
}

// needsTrampoline reports whether spec requests a pre-exec limit (any Rlimit or a
// Umask) that the stdlib spawn cannot apply directly, so Start must route through
// the re-exec trampoline.
func needsTrampoline(spec coreproc.Spec) bool {
	//: a mapped Rlimit or a non-nil Umask is honourable only via the trampoline.
	return len(spec.Rlimits) > 0 || spec.Umask != nil
}

// encodeTrampoline builds the payload env value for spec: a ';'-separated list of
// a "u<mask>" umask token (when set) and one "r<num>,<soft>,<hard>" token per
// rlimit (the resource already validated against resourceLimits by checkLimits).
func encodeTrampoline(spec coreproc.Spec) string {
	var b strings.Builder
	//: a non-nil Umask becomes a single umask token.
	if spec.Umask != nil {
		//: encode the decimal mask under the 'u' tag.
		b.WriteString("u" + strconv.Itoa(*spec.Umask) + ";")
	}
	//: each requested rlimit becomes an "r<num>,<soft>,<hard>" token.
	for res, lim := range spec.Rlimits {
		//: map the abstract Resource to its platform RLIMIT_* number for the child.
		b.WriteString("r" + strconv.Itoa(resourceLimits[res]) + "," +
			strconv.FormatUint(lim.Soft, decimalRadix) + "," +
			strconv.FormatUint(lim.Hard, decimalRadix) + ";")
	}
	//: the encoded payload the trampoline child decodes and applies.
	return b.String()
}

// trampolineSpawn returns the (path, argv, envAddon) that make os.StartProcess run
// the trampoline: exec this binary (os.Executable()) with argv [self, target,
// targetArgv...] and an extra env entry carrying the encoded limits.
func trampolineSpawn(spec coreproc.Spec) (path string, argv, envAddon []string, err error) {
	self, eErr := os.Executable()
	//: without a path to re-exec, the limits cannot be honoured pre-exec.
	if eErr != nil {
		//: surface the failure so Start wraps it as RlimitFailed.
		return "", nil, nil, eErr
	}
	//: argv[0]=self, argv[1]=target exec path, argv[2:]=the target's own argv.
	argv = append([]string{self, spec.Path}, buildArgv(spec)...)
	//: the sentinel env entry carries the encoded rlimit/umask payload.
	envAddon = []string{trampolineEnv + "=" + encodeTrampoline(spec)}
	//: re-exec this binary as the trampoline.
	return self, argv, envAddon, nil
}
