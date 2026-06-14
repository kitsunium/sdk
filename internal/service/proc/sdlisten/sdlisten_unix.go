//go:build unix

// Package sdlisten — Unix socket activation (sd_listen_fds(3) family). The
// service side recovers listening sockets an activator passed via inherited fds
// 3.. plus the LISTEN_FDS / LISTEN_PID / LISTEN_FDNAMES environment; the
// activator side (Prepare) is the symmetric half, so the protocol is testable
// end-to-end without systemd. Fd inheritance is POSIX, so this builds on every
// Unix; non-Unix gets a stub returning UnsupportedPlatform.
package sdlisten

import (
	"maps"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// listenFdStart is SD_LISTEN_FDS_START: the first inherited socket is fd 3, after
// stdin/stdout/stderr.
const listenFdStart int = 3

// exitOSErr is sysexits.h EX_OSERR (71), restated to wrap a cause under the
// central LISTEN_FAILED sentinel without re-Defining the code.
const exitOSErr int = 71

// Environment-variable names of the sd_listen_fds protocol.
const (
	envFds     string = "LISTEN_FDS"
	envPID     string = "LISTEN_PID"
	envFdNames string = "LISTEN_FDNAMES"
)

// dropErr intentionally discards a non-actionable cleanup error (a best-effort
// close or env unset), so the error audit treats the discard as deliberate.
func dropErr(err error) {
	//: read the parameter so the unused-error audit treats this as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
	//: the error concerns a best-effort cleanup step — nothing to do.
}

// wrapListen wraps an fd/socket cause onto the central ListenFailed sentinel.
func wrapListen(cause error, fields ...errs.FieldValue) error {
	//: restate the LISTEN_FAILED sentinel fields so the cause inherits them.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeListenFailed,
		Reason:   "LISTEN_FAILED",
		Public:   "Could not set up the activation socket",
		Private:  "service/proc/sdlisten: a passed listen fd could not be recovered or wrapped",
		ExitCode: exitOSErr,
	}, fields...)
}

// fdCount parses LISTEN_FDS and verifies LISTEN_PID. ok is false (no error) when
// the activation set is empty or addressed to a different pid; err is non-nil
// only when LISTEN_FDS is present but malformed.
func fdCount() (n int, ok bool, err error) {
	raw := os.Getenv(envFds)
	//: no LISTEN_FDS means this process was not socket-activated.
	if raw == "" {
		//: nothing inherited — an empty, non-error result.
		return 0, false, nil
	}
	count, perr := strconv.Atoi(raw)
	//: a non-integer or negative LISTEN_FDS is a malformed activation env.
	if perr != nil || count < 0 {
		//: surface the malformed count as a typed LISTEN_FAILED.
		return 0, false, wrapListen(os.ErrInvalid, errs.String(envFds, raw))
	}
	//: a zero count, or fds addressed to a different process, yields nothing.
	if count == 0 || !pidMatches() {
		//: an empty / foreign activation set is not an error.
		return 0, false, nil
	}
	//: a valid activation set addressed to this process.
	return count, true, nil
}

// pidMatches reports whether LISTEN_PID is absent (accepted) or equals this
// process's pid. A set-but-mismatched LISTEN_PID means the fds are not ours.
func pidMatches() bool {
	raw := os.Getenv(envPID)
	//: an absent LISTEN_PID is accepted — our activator omits it (pre-fork it
	//: cannot know the child pid); a real systemd parent always sets it.
	if raw == "" {
		//: trust the inherited fds from our direct parent.
		return true
	}
	want, perr := strconv.Atoi(raw)
	//: a malformed LISTEN_PID is treated as a mismatch — never assume it is ours.
	if perr != nil {
		//: reject rather than risk reading fds meant for another process.
		return false
	}
	//: the fds are ours only when LISTEN_PID names this process.
	return want == os.Getpid()
}

// splitNames returns n stream names from LISTEN_FDNAMES (colon-separated),
// padding with "" when fewer names than fds are supplied.
func splitNames(n int) []string {
	var parts []string
	//: split LISTEN_FDNAMES when present; an absent value leaves every name "".
	if raw := os.Getenv(envFdNames); raw != "" {
		//: colon-separated names parallel the inherited fds.
		parts = strings.Split(raw, ":")
	}
	out := make([]string, 0, n)
	//: emit exactly n names: a supplied name where the env provides one, else "".
	for i := range n {
		//: pad past the supplied names with the empty (unnamed) socket name.
		if i >= len(parts) {
			//: no more supplied names — the socket at this fd is unnamed.
			out = append(out, "")
			continue
		}
		//: name[i] pairs with the socket at fd listenFdStart+i.
		out = append(out, parts[i])
	}
	//: exactly n names, padded with "" as needed.
	return out
}

// Files returns the inherited listening sockets (fd 3..3+LISTEN_FDS) as *os.File,
// gated on LISTEN_PID. When unsetEnv is true the activation env is cleared so a
// grandchild does not re-inherit it. An empty/foreign activation set yields nil.
func Files(unsetEnv bool) (files []*os.File, err error) {
	//: optionally clear the activation env after reading it.
	if unsetEnv {
		//: defer so the env is unset on every return path.
		defer clearEnv()
	}
	n, ok, cerr := fdCount()
	//: a malformed LISTEN_FDS surfaces a typed error.
	if cerr != nil {
		//: propagate the typed LISTEN_FAILED verbatim.
		return nil, cerr
	}
	//: an empty or foreign activation set returns no files (not an error).
	if !ok {
		//: nothing inherited for this process.
		return nil, nil
	}
	names := splitNames(n)
	out := make([]*os.File, 0, n)
	//: wrap each inherited fd as a named *os.File, clearing CLOEXEC so a later
	//: re-exec keeps the socket.
	for i := range n {
		fd := listenFdStart + i
		//: clear CLOEXEC so the fd survives a subsequent exec if the caller re-execs.
		syscall.CloseOnExec(fd)
		//: os.NewFile adopts the inherited descriptor under its protocol name.
		out = append(out, os.NewFile(uintptr(fd), names[i]))
	}
	//: the recovered listening sockets, in fd order.
	return out, nil
}

// Listeners returns the inherited stream sockets wrapped as net.Listener. Each
// underlying *os.File is closed after net.FileListener dups it.
func Listeners(unsetEnv bool) (lns []net.Listener, err error) {
	files, ferr := Files(unsetEnv)
	//: an fd-recovery failure aborts before wrapping listeners.
	if ferr != nil {
		//: propagate the typed LISTEN_FAILED verbatim.
		return nil, ferr
	}
	out := make([]net.Listener, 0, len(files))
	//: wrap each socket fd as a net.Listener, releasing the *os.File dup.
	for _, f := range files {
		ln, lerr := net.FileListener(f)
		//: a non-stream socket (or closed fd) cannot become a net.Listener.
		if lerr != nil {
			//: surface the wrap failure as a typed LISTEN_FAILED.
			return nil, wrapListen(lerr, errs.String("name", f.Name()))
		}
		//: FileListener dups the fd, so the original *os.File is no longer needed.
		dropErr(f.Close())
		out = append(out, ln)
	}
	//: the recovered listeners, in fd order.
	return out, nil
}

// WithNames returns the inherited sockets grouped by their LISTEN_FDNAMES name
// (duplicate names group multiple fds under one key).
func WithNames(unsetEnv bool) (byName map[string][]*os.File, err error) {
	files, ferr := Files(unsetEnv)
	//: an fd-recovery failure aborts before grouping.
	if ferr != nil {
		//: propagate the typed LISTEN_FAILED verbatim.
		return nil, ferr
	}
	out := make(map[string][]*os.File, len(files))
	//: group fds by their protocol name; duplicate names accumulate.
	for _, f := range files {
		//: append to the name's group, creating it on first sight.
		out[f.Name()] = append(out[f.Name()], f)
	}
	//: the name→fds grouping.
	return out, nil
}

// Prepare is the activator side: it appends each listener's socket to child as an
// inherited fd and sets LISTEN_FDS / LISTEN_FDNAMES in child.Env so the spawned
// process recovers them via Files/Listeners. LISTEN_PID is intentionally omitted
// (a pre-fork activator cannot know the child pid; Files accepts an absent
// LISTEN_PID from a trusted parent). Sockets are passed in sorted-name order.
func Prepare(child *coreproc.Spec, named map[string]net.Listener) error {
	//: nothing to pass means the child env is left untouched.
	if len(named) == 0 {
		//: a no-op activation.
		return nil
	}
	//: iterate in sorted-name order so the fd↔name pairing is deterministic.
	names := slices.Sorted(maps.Keys(named))
	//: collect each listener's socket fd in the same order as its name.
	for _, name := range names {
		f, ferr := listenerFile(named[name])
		//: a listener with no recoverable fd aborts the whole preparation.
		if ferr != nil {
			//: propagate the typed LISTEN_FAILED verbatim.
			return ferr
		}
		child.ExtraFiles = append(child.ExtraFiles, f)
	}
	//: publish the count and names so the child's Files() recovers them.
	child.Env = append(envWithout(child.Env, envFds, envPID, envFdNames),
		envFds+"="+strconv.Itoa(len(names)),
		envFdNames+"="+strings.Join(names, ":"))
	//: the child spec now carries the inherited sockets and their protocol env.
	return nil
}

// listenerFile returns the dup'd *os.File backing a TCP or Unix listener; other
// listener kinds have no portable fd and are rejected.
func listenerFile(ln net.Listener) (f *os.File, err error) {
	//: only the two stream-socket listener kinds expose a portable fd.
	switch v := ln.(type) {
	//: a TCP listener exposes its socket via File().
	case *net.TCPListener:
		//: File() dups the fd; the caller (the child) inherits the dup.
		return fileOrWrap(v.File())
	//: a Unix-domain listener likewise exposes its socket.
	case *net.UnixListener:
		//: File() dups the fd for inheritance.
		return fileOrWrap(v.File())
	//: any other listener type has no portable socket fd.
	default:
		//: reject an unsupported listener kind with a typed error.
		return nil, wrapListen(os.ErrInvalid, errs.String("listener", "unsupported type"))
	}
}

// fileOrWrap passes through a recovered *os.File or wraps the File() error.
func fileOrWrap(f *os.File, ferr error) (file *os.File, err error) {
	//: a File() failure means the socket could not be dup'd for inheritance.
	if ferr != nil {
		//: surface the dup failure as a typed LISTEN_FAILED.
		return nil, wrapListen(ferr)
	}
	//: the dup'd socket, ready to inherit.
	return f, nil
}

// envWithout returns env with any KEY= entries for the given keys removed, so
// Prepare can re-set the activation variables without duplicates.
func envWithout(env []string, keys ...string) []string {
	out := make([]string, 0, len(env))
	//: keep every entry whose key is not one we are about to re-set.
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		//: drop an entry only when its key matches one being replaced.
		if !slices.Contains(keys, key) {
			//: retain the unrelated environment entry.
			out = append(out, kv)
		}
	}
	//: the filtered environment, ready for the fresh activation entries.
	return out
}

// clearEnv unsets the activation environment variables so a grandchild does not
// re-inherit them.
func clearEnv() {
	//: unset each activation variable; an unset failure is non-actionable here.
	dropErr(os.Unsetenv(envFds))
	dropErr(os.Unsetenv(envPID))
	dropErr(os.Unsetenv(envFdNames))
}
