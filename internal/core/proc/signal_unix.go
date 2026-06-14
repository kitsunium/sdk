//go:build unix

// Package proc — Unix signal name table (linux/darwin/bsd).
package proc

import "syscall"

// signalNames maps each known Signal to its canonical SIG* name on Unix
// platforms (linux/darwin/bsd). Only POSIX-common signals are listed so the
// table compiles identically across every Unix target; String falls back to a
// numeric form for anything absent, and Parse scans this table for the reverse
// lookup. Declared as a literal (no init) per KTN-FUNC-NOINIT.
var signalNames = map[Signal]string{
	Signal(syscall.SIGHUP):    "SIGHUP",
	Signal(syscall.SIGINT):    "SIGINT",
	Signal(syscall.SIGQUIT):   "SIGQUIT",
	Signal(syscall.SIGILL):    "SIGILL",
	Signal(syscall.SIGTRAP):   "SIGTRAP",
	Signal(syscall.SIGABRT):   "SIGABRT",
	Signal(syscall.SIGBUS):    "SIGBUS",
	Signal(syscall.SIGFPE):    "SIGFPE",
	Signal(syscall.SIGKILL):   "SIGKILL",
	Signal(syscall.SIGUSR1):   "SIGUSR1",
	Signal(syscall.SIGSEGV):   "SIGSEGV",
	Signal(syscall.SIGUSR2):   "SIGUSR2",
	Signal(syscall.SIGPIPE):   "SIGPIPE",
	Signal(syscall.SIGALRM):   "SIGALRM",
	Signal(syscall.SIGTERM):   "SIGTERM",
	Signal(syscall.SIGCHLD):   "SIGCHLD",
	Signal(syscall.SIGCONT):   "SIGCONT",
	Signal(syscall.SIGSTOP):   "SIGSTOP",
	Signal(syscall.SIGTSTP):   "SIGTSTP",
	Signal(syscall.SIGTTIN):   "SIGTTIN",
	Signal(syscall.SIGTTOU):   "SIGTTOU",
	Signal(syscall.SIGURG):    "SIGURG",
	Signal(syscall.SIGXCPU):   "SIGXCPU",
	Signal(syscall.SIGXFSZ):   "SIGXFSZ",
	Signal(syscall.SIGVTALRM): "SIGVTALRM",
	Signal(syscall.SIGPROF):   "SIGPROF",
	Signal(syscall.SIGWINCH):  "SIGWINCH",
	Signal(syscall.SIGSYS):    "SIGSYS",
}
