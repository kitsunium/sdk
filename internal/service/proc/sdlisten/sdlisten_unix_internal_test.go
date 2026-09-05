//go:build unix

// Package sdlisten — white-box tests for the sd_listen_fds protocol helpers.
// Each one reads a piece of the activation environment, and each one has a
// "this is not ours" branch that the public entry points collapse into a single
// empty result.
package sdlisten

import (
	"net"
	"os"
	"slices"
	"strconv"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_fdCount pins the three outcomes the public Files collapses into one: a
// usable set, an absent-or-foreign one, and a malformed one.
//
// Keeping "foreign" and "malformed" apart is the point. A binary run from a
// shell inherits nothing and must start normally; an activator that wrote a
// garbled LISTEN_FDS is broken and nobody else will say so.
func Test_fdCount(t *testing.T) {
	//: not parallel — every case mutates the process environment.
	type tc struct {
		name      string
		listenFDs string
		listenPID string
		wantN     int
		wantOK    bool
		wantErr   bool
	}
	tests := []tc{
		{name: "no LISTEN_FDS at all"},
		{name: "an empty LISTEN_FDS"},
		{name: "a zero count", listenFDs: "0"},
		{name: "a usable set with no pid", listenFDs: "2", wantN: 2, wantOK: true},
		{
			name:      "a usable set addressed to us",
			listenFDs: "1",
			listenPID: strconv.Itoa(os.Getpid()),
			wantN:     1,
			wantOK:    true,
		},
		{
			name:      "a set addressed elsewhere",
			listenFDs: "1",
			listenPID: strconv.Itoa(os.Getpid() + 1),
		},
		{name: "a non-numeric count", listenFDs: "many", wantErr: true},
		{name: "a negative count", listenFDs: "-1", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Setenv(envFds, c.listenFDs)
		t.Setenv(envPID, c.listenPID)

		n, ok, err := fdCount()
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeListenFailed) {
				t.Fatalf("fdCount with %s = %v, want LISTEN_FAILED", c.name, err)
			}
			//: a refused count must report nothing usable beside the error.
			if ok || n != 0 {
				t.Errorf("fdCount reported n=%d ok=%v beside the error", n, ok)
			}
			return
		}
		if err != nil {
			t.Fatalf("fdCount with %s = %v, want nil", c.name, err)
		}
		if ok != c.wantOK || n != c.wantN {
			t.Errorf("fdCount with %s = (%d, %v), want (%d, %v)", c.name, n, ok, c.wantN, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// Test_pidMatches pins the ownership check. An absent LISTEN_PID is accepted
// because our own activator omits it — pre-fork it cannot know the child's pid —
// while anything present and not ours is refused, since reading another
// process's descriptors is a fault the kernel cannot catch for us.
func Test_pidMatches(t *testing.T) {
	//: not parallel — every case mutates the process environment.
	type tc struct {
		name      string
		listenPID string
		want      bool
	}
	tests := []tc{
		{"an absent pid is trusted", "", true},
		{"our own pid", strconv.Itoa(os.Getpid()), true},
		{"another process's pid", strconv.Itoa(os.Getpid() + 1), false},
		{"pid 1", "1", os.Getpid() == 1},
		//: a malformed value is a mismatch, never an assumption of ownership.
		{"a non-numeric pid", "not-a-pid", false},
		{"a negative pid", "-1", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Setenv(envPID, c.listenPID)
		if got := pidMatches(); got != c.want {
			t.Errorf("pidMatches with %s = %v, want %v", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// Test_splitNames pins the padding. The result indexes fd 3+i, so it MUST have
// exactly n entries: a short slice would panic on the fd nobody named, and a
// long one would attach a name to a descriptor that was never passed.
func Test_splitNames(t *testing.T) {
	//: not parallel — every case mutates the process environment.
	type tc struct {
		name  string
		names string
		n     int
		want  []string
	}
	tests := []tc{
		{"no names for no sockets", "", 0, []string{}},
		{"no names pads every socket", "", 3, []string{"", "", ""}},
		{"one name for one socket", "http", 1, []string{"http"}},
		{"two names for two sockets", "http:metrics", 2, []string{"http", "metrics"}},
		{"fewer names than sockets pads the tail", "http", 3, []string{"http", "", ""}},
		{"more names than sockets are ignored", "a:b:c", 2, []string{"a", "b"}},
		{"an empty name between two others", "a::c", 3, []string{"a", "", "c"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Setenv(envFdNames, c.names)

		got := splitNames(c.n)

		if !slices.Equal(got, c.want) {
			t.Fatalf("splitNames(%d) with %q = %v, want %v", c.n, c.names, got, c.want)
		}
		//: the length is the load-bearing part: it indexes fd 3+i.
		if len(got) != c.n {
			t.Errorf("splitNames(%d) returned %d names", c.n, len(got))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// Test_envWithout pins the filter Prepare uses to re-set the activation
// variables. Leaving a stale entry behind would hand the child two LISTEN_FDS
// values, and which one it reads is up to the exec implementation.
func Test_envWithout(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		env  []string
		keys []string
		want []string
	}
	tests := []tc{
		{"an empty environment", nil, []string{"LISTEN_FDS"}, []string{}},
		{"nothing to remove", []string{"PATH=/bin"}, []string{"LISTEN_FDS"}, []string{"PATH=/bin"}},
		{
			"one key removed",
			[]string{"PATH=/bin", "LISTEN_FDS=1"},
			[]string{"LISTEN_FDS"},
			[]string{"PATH=/bin"},
		},
		{
			"several keys removed",
			[]string{"LISTEN_PID=1", "PATH=/bin", "LISTEN_FDS=1", "LISTEN_FDNAMES=http"},
			[]string{"LISTEN_FDS", "LISTEN_PID", "LISTEN_FDNAMES"},
			[]string{"PATH=/bin"},
		},
		{
			//: a duplicate entry must go entirely, not merely once.
			"a duplicated key",
			[]string{"LISTEN_FDS=1", "PATH=/bin", "LISTEN_FDS=2"},
			[]string{"LISTEN_FDS"},
			[]string{"PATH=/bin"},
		},
		{
			//: a value containing the key name must not be mistaken for it.
			"an entry whose value mentions the key",
			[]string{"NOTES=LISTEN_FDS is set"},
			[]string{"LISTEN_FDS"},
			[]string{"NOTES=LISTEN_FDS is set"},
		},
		{
			//: an entry with no '=' has an empty value, not no key.
			"a bare entry",
			[]string{"LISTEN_FDS"},
			[]string{"LISTEN_FDS"},
			[]string{},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		env := slices.Clone(c.env)

		got := envWithout(env, c.keys...)

		if !slices.Equal(got, c.want) {
			t.Errorf("envWithout(%v, %v) = %v, want %v", c.env, c.keys, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_wrapListen pins that every fd fault reaches the caller under one code
// with its cause intact. Losing the cause would leave an operator with "could
// not set up the activation socket" and no way to tell a bad fd from a bad type.
func Test_wrapListen(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		cause  error
		fields []errs.FieldValue
	}
	tests := []tc{
		{"a cause with no fields", os.ErrInvalid, nil},
		{"a cause with one field", os.ErrInvalid, []errs.FieldValue{errs.String("name", "http")}},
		{"a cause with several fields", os.ErrClosed, []errs.FieldValue{
			errs.String("name", "http"), errs.String("listener", "unsupported type"),
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := wrapListen(c.cause, c.fields...)
		if !errs.HasCode(err, coreproc.CodeListenFailed) {
			t.Fatalf("wrapListen = %v, want LISTEN_FAILED", err)
		}
		//: the exit status is what a supervisor reports; EX_OSERR says the
		//: fault was the operating system's, not the operator's.
		if got := errs.ExitCodeOf(err); got != exitOSErr {
			t.Errorf("exit code = %d, want %d", got, exitOSErr)
		}
		//: the caller's fields must all survive.
		if len(errs.FieldsOf(err)) < len(c.fields) {
			t.Errorf("fields = %v, want at least the %d supplied", errs.FieldsOf(err), len(c.fields))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_fileOrWrap pins the pass-through. It exists so every File() call site
// reads the same, and the only thing it must not do is swallow the failure.
func Test_fileOrWrap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		file    *os.File
		ferr    error
		wantErr bool
	}
	tests := []tc{
		{name: "a recovered file", file: os.Stdin},
		{name: "a nil file with no error"},
		{name: "a dup failure", ferr: os.ErrInvalid, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := fileOrWrap(c.file, c.ferr)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeListenFailed) {
				t.Fatalf("fileOrWrap = %v, want LISTEN_FAILED", err)
			}
			//: a failure must hand back no file, or a caller checking only the
			//: file would use one that was never dup'd.
			if got != nil {
				t.Errorf("fileOrWrap returned a file beside the error")
			}
			return
		}
		if err != nil {
			t.Fatalf("fileOrWrap = %v, want nil", err)
		}
		if got != c.file {
			t.Errorf("fileOrWrap returned a different file")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_listenerFile pins which listener kinds can be passed on. Only TCP and
// Unix expose a portable socket fd; anything else has to be refused here, or
// Prepare would announce a socket in LISTEN_FDS that the child cannot find.
func Test_listenerFile(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		build   func(t *testing.T) net.Listener
		wantErr bool
	}
	tests := []tc{
		{
			name: "a TCP listener",
			build: func(t *testing.T) net.Listener {
				t.Helper()
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatalf("Listen: %v", err)
				}
				t.Cleanup(func() { dropErr(ln.Close()) })
				return ln
			},
		},
		{
			name: "a Unix listener",
			build: func(t *testing.T) net.Listener {
				t.Helper()
				ln, err := net.Listen("unix", t.TempDir()+"/s.sock")
				if err != nil {
					t.Fatalf("Listen: %v", err)
				}
				t.Cleanup(func() { dropErr(ln.Close()) })
				return ln
			},
		},
		{
			name:    "a listener kind with no portable fd",
			build:   func(*testing.T) net.Listener { return fakeListener{} },
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := listenerFile(c.build(t))
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeListenFailed) {
				t.Fatalf("listenerFile = %v, want LISTEN_FAILED", err)
			}
			if got != nil {
				t.Errorf("listenerFile returned a file beside the error")
			}
			return
		}
		if err != nil {
			t.Fatalf("listenerFile = %v, want nil", err)
		}
		if got == nil {
			t.Fatal("listenerFile returned no file and no error")
		}
		dropErr(got.Close())
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// fakeListener is a net.Listener with no portable socket fd, which is what
// listenerFile must refuse.
type fakeListener struct{}

// Accept never accepts; the listener exists only to be rejected.
func (fakeListener) Accept() (net.Conn, error) { return nil, os.ErrClosed }

// Close reports success.
func (fakeListener) Close() error { return nil }

// Addr reports a placeholder address.
func (fakeListener) Addr() net.Addr { return nil }

// Test_clearEnv pins that every activation variable is removed. Leaving one
// behind would let a grandchild believe it was socket-activated and go looking
// for descriptors nobody passed it.
func Test_clearEnv(t *testing.T) {
	//: not parallel — it mutates the process environment.
	type tc struct {
		name string
		key  string
	}
	tests := []tc{
		{"the descriptor count", envFds},
		{"the owning pid", envPID},
		{"the descriptor names", envFdNames},
	}
	t.Setenv(envFds, "1")
	t.Setenv(envPID, strconv.Itoa(os.Getpid()))
	t.Setenv(envFdNames, "http")

	clearEnv()

	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if _, set := os.LookupEnv(c.key); set {
			t.Errorf("clearEnv left %s set", c.key)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
