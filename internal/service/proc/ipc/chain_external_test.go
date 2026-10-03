//go:build unix

package ipc_test

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/ipc"
)

// This file carries the SAME build constraint as chain_unix.go, whose rule it
// pins. Windows has no socket directory — its endpoint is a named pipe whose
// name is a hash of the path, so nothing is resolved through it.

// shortBase is a fresh directory under base, short enough for macOS's sun_path
// (ADR 0094), removed at the end of the test whatever modes the test left on
// the directories inside it.
func shortBase(t *testing.T, base string) string {
	t.Helper()
	dir, err := os.MkdirTemp(base, "ipc")
	if err != nil {
		t.Skipf("cannot create a directory under %s here: %v", base, err)
	}
	t.Cleanup(func() {
		//: a 0702 or 0777|sticky directory is the owner's to remove, but the
		//: walk RemoveAll makes is not guaranteed to cope with every mode a
		//: row leaves behind, so the tree is made owner-only first.
		if walkErr := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				return os.Chmod(path, 0o700)
			}
			return nil
		}); walkErr != nil {
			t.Logf("cleanup: %v", walkErr)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	return dir
}

// fieldsOf reads a refusal's fields into a map: they are the operator's whole
// diagnosis, so they are asserted by key rather than matched against a
// rendered sentence a formatting change could reshape.
func fieldsOf(err error) map[string]string {
	fields := map[string]string{}
	for _, field := range errs.FieldsOf(err) {
		fields[field.Key()] = field.StringValue()
	}
	return fields
}

// rawListener binds a Unix socket at path without this package, the way a
// daemon already running there — or an impostor — would have: the Dial side
// of a row must be judged on a socket that exists.
func rawListener(t *testing.T, path string) *net.UnixListener {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("building the socket directory = %v", err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("binding %s = %v", path, err)
	}
	t.Cleanup(func() {
		//: a test that closed it itself, to end an Accept loop, already did.
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Logf("close: %v", err)
		}
	})
	return ln
}

// TestAComponentAboveTheSocketDirectoryIsJudgedByTheDirectoryHoldingIt pins
// the whole rule, both sides, including every accepting row.
//
// The rule (chain_unix.go): a component above the socket's directory is
// refused when ANYBODY can write the directory holding it AND it is a link
// (anybody could have planted it — lock's rule, ADR 0083), or it is not this
// account's or root's (anybody could have created it), or that directory has
// no sticky bit (anybody can replace it). The accepting rows are not
// decoration: on macOS every path here also crosses /tmp -> private/tmp, a
// link the operating system ships in a directory only root writes, and a rule
// that refused links outright would refuse every socket on that kernel.
//
// The world-writable STICKY row with a link is the one that separates this
// rule from the socket directory's own check: sticky governs UNLINKING an
// entry that exists, and planting a component creates one at a name nobody
// had taken — which is what a stranger in /tmp does.
func TestAComponentAboveTheSocketDirectoryIsJudgedByTheDirectoryHoldingIt(t *testing.T) {
	t.Parallel()
	type row struct {
		name      string
		container os.FileMode
		link      bool
		kind      string // "" accepts; otherwise the refusal's kind field
	}
	rows := []row{
		{"a real directory in an owner-only container", 0o700, false, ""},
		{"a real directory in a group-writable container", 0o770, false, ""},
		{"a real directory of ours in a world-writable STICKY container", 0o777 | os.ModeSticky, false, ""},
		{"a real directory in a world-writable container without sticky", 0o777, false, "replaceable"},
		{"a link in an owner-only container", 0o700, true, ""},
		{"a link in a group-writable container", 0o770, true, ""},
		{"a link in a world-writable container", 0o777, true, "indirection"},
		{"a link in a world-writable STICKY container", 0o777 | os.ModeSticky, true, "indirection"},
		{"a link in a write-only-for-others container", 0o702, true, "indirection"},
	}
	run := func(t *testing.T, r row) {
		t.Helper()
		base := shortBase(t, "/tmp")
		//: the tree a link points at, and the directory that would hold the
		//: component — identical in every row but the thing under test.
		target := filepath.Join(base, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatalf("building the target = %v", err)
		}
		container := filepath.Join(base, "pub")
		if err := os.Mkdir(container, 0o700); err != nil {
			t.Fatalf("building the container = %v", err)
		}
		middle := filepath.Join(container, "app")
		if r.link {
			if err := os.Symlink(target, middle); err != nil {
				t.Skipf("cannot create a symbolic link here: %v", err)
			}
		} else if err := os.Mkdir(middle, 0o700); err != nil {
			t.Fatalf("building the component = %v", err)
		}
		//: the mode is set last, because Mkdir's is filtered by the umask and
		//: a runner with umask 022 would turn every world-writable row into
		//: 0755 and assert nothing.
		if err := os.Chmod(container, r.container); err != nil {
			t.Skipf("cannot set the mode this row needs (%v): %v", r.container, err)
		}
		cfg := ipc.Config{Path: filepath.Join(middle, "run", "d.sock")}
		ln, err := ipc.NewListener(&cfg)
		if r.kind == "" {
			//: the accepting half, which is the one a careless rule loses.
			if err != nil {
				t.Fatalf("NewListener under a %v container (link=%v) = %v, want a listener", r.container, r.link, err)
			}
			defer closeOrLog(t, ln)
			c, dialErr := ipc.Dial(t.Context(), &cfg)
			if dialErr != nil {
				t.Fatalf("Dial under a %v container (link=%v) = %v, want a connection", r.container, r.link, dialErr)
			}
			closeOrLog(t, c)
			return
		}
		//: the refusing half: no listener, the right code, the right kind.
		if ln != nil {
			closeOrLog(t, ln)
			t.Fatalf("%s: accepted, so the socket lives wherever it leads", r.name)
		}
		if !errs.HasCode(err, ipc.CodePathUnsafe) {
			t.Fatalf("NewListener under a %v container (link=%v) = %v, want PATH_UNSAFE", r.container, r.link, err)
		}
		if got := fieldsOf(err)["kind"]; got != r.kind {
			t.Fatalf("NewListener's refusal kind = %q, want %q", got, r.kind)
		}
		//: and nothing was created inside the steered tree: the audit runs
		//: before Mkdir, which would have followed the link.
		if _, statErr := os.Lstat(filepath.Join(target, "run")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("the refused Listen still created a directory under the link's target: %v", statErr)
		}
		//: the client's half, on a socket that EXISTS where the path leads —
		//: what a stranger's listener behind the component would be. It is
		//: refused before a byte is sent, under the same kind.
		real := filepath.Join(target, "run", "d.sock")
		if !r.link {
			real = cfg.Path
		}
		rawListener(t, real)
		if c, dialErr := ipc.Dial(t.Context(), &cfg); !errs.HasCode(dialErr, ipc.CodePathUnsafe) || fieldsOf(dialErr)["kind"] != r.kind {
			if c != nil {
				closeOrLog(t, c)
			}
			t.Fatalf("Dial under a %v container (link=%v) = %v, want PATH_UNSAFE kind %q", r.container, r.link, dialErr, r.kind)
		}
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			run(t, r)
		})
	}
}

// TestAStrangerCannotRetargetAClientThroughALinkItPlanted is the attack the
// rule was written against, measured on the code before it: a link planted in
// a 0777|sticky directory belongs to its planter, who may replace it at will,
// so a client that followed it reached whatever socket the planter chose and
// handed it its first line ("impostor read secret-token").
//
// Goroutine lifecycle: one goroutine accepts on the impostor's socket until
// that socket is closed, which the test does before reading the count it
// reports on a buffered channel — Accept returns then, and the goroutine ends.
func TestAStrangerCannotRetargetAClientThroughALinkItPlanted(t *testing.T) {
	t.Parallel()
	base := shortBase(t, "/tmp")
	pub := filepath.Join(base, "pub")
	trap := filepath.Join(base, "trap")
	for _, dir := range []string{pub, trap} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("building %s = %v", dir, err)
		}
	}
	impostor := rawListener(t, filepath.Join(trap, "run", "d.sock"))
	accepted := make(chan int, 1)
	go func() {
		count := 0
		for {
			c, err := impostor.Accept()
			if err != nil {
				accepted <- count
				return
			}
			count++
			closeOrLog(t, c)
		}
	}()
	planted := filepath.Join(pub, "app")
	if err := os.Symlink(trap, planted); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	if err := os.Chmod(pub, 0o777|os.ModeSticky); err != nil {
		t.Skipf("cannot set the mode this test needs: %v", err)
	}
	c, err := ipc.Dial(t.Context(), new(ipc.Config{Path: filepath.Join(planted, "run", "d.sock")}))
	if c != nil {
		closeOrLog(t, c)
	}
	if !errs.HasCode(err, ipc.CodePathUnsafe) {
		t.Errorf("Dial through a link planted in a 0777|sticky directory = %v, want PATH_UNSAFE", err)
	}
	if err := impostor.Close(); err != nil {
		t.Fatalf("closing the impostor = %v", err)
	}
	if n := <-accepted; n != 0 {
		t.Fatalf("the impostor accepted %d connection(s): the client reached it", n)
	}
}

// TestTheRefusalNamesThePlantedComponent pins the fields, because the
// component that was planted is usually neither the first nor the last thing
// an operator would look at.
func TestTheRefusalNamesThePlantedComponent(t *testing.T) {
	t.Parallel()
	base := shortBase(t, "/tmp")
	pub := filepath.Join(base, "pub")
	elsewhere := filepath.Join(base, "elsewhere")
	for _, dir := range []string{pub, elsewhere} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("building %s = %v", dir, err)
		}
	}
	planted := filepath.Join(pub, "app")
	if err := os.Symlink(elsewhere, planted); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	if err := os.Chmod(pub, 0o777|os.ModeSticky); err != nil {
		t.Skipf("cannot set the mode this test needs: %v", err)
	}
	dir := filepath.Join(planted, "run")
	_, err := ipc.NewListener(&ipc.Config{Path: filepath.Join(dir, "d.sock")})
	fields := fieldsOf(err)
	//: the component is reported where it LIVES — through /tmp -> private/tmp
	//: on macOS — so it is compared by identity, with Lstat on both sides so
	//: the link compares as itself rather than as its target.
	reported, reportedErr := os.Lstat(fields["path"])
	if reportedErr != nil {
		t.Fatalf("the path field %q: %v (refusal: %v)", fields["path"], reportedErr, err)
	}
	expected, expectedErr := os.Lstat(planted)
	if expectedErr != nil {
		t.Fatalf("Lstat(%s) = %v", planted, expectedErr)
	}
	if !os.SameFile(reported, expected) {
		t.Fatalf("field path = %q, which is not the planted component %q", fields["path"], planted)
	}
	//: what the operator configured, what was found, where it leads, and the
	//: mode that made it plantable — sticky included, since sticky does not
	//: exempt a creation.
	want := map[string]string{"dir": dir, "kind": "indirection", "target": elsewhere}
	for key, value := range want {
		if fields[key] != value {
			t.Errorf("field %s = %q, want %q", key, fields[key], value)
		}
	}
	if container := fields["container"]; !strings.Contains(container, "t") || !strings.HasSuffix(container, "rwxrwxrwx") {
		t.Errorf("field container = %q, want the world-writable sticky mode", container)
	}
	if fields["uid"] != "" && fields["uid"] != itoa(os.Geteuid()) {
		t.Errorf("field uid = %q, want the link's owner %d", fields["uid"], os.Geteuid())
	}
}

// TestTheSocketDirectoryItselfIsJudgedByTheDirectoryHoldingIt pins the
// holder's rule on the socket's directory: held where anybody can write and
// nothing stops anybody replacing it, it is refused; held where anybody can
// write but only its owner may unlink it — /tmp's shape, RuntimeDir's
// fallback — it is accepted.
func TestTheSocketDirectoryItselfIsJudgedByTheDirectoryHoldingIt(t *testing.T) {
	t.Parallel()
	for _, r := range []struct {
		holder os.FileMode
		refuse bool
	}{
		{0o777, true},
		{0o777 | os.ModeSticky, false},
	} {
		base := shortBase(t, "/tmp")
		holder := filepath.Join(base, "pub")
		if err := os.Mkdir(holder, 0o700); err != nil {
			t.Fatalf("building the holder = %v", err)
		}
		if err := os.Chmod(holder, r.holder); err != nil {
			t.Skipf("cannot set the mode this test needs (%v): %v", r.holder, err)
		}
		cfg := ipc.Config{Path: filepath.Join(holder, "run", "d.sock")}
		ln, err := ipc.NewListener(&cfg)
		if !r.refuse {
			if err != nil {
				t.Fatalf("NewListener in a %v holder = %v, want a listener", r.holder, err)
			}
			closeOrLog(t, ln)
			continue
		}
		if ln != nil {
			closeOrLog(t, ln)
		}
		if !errs.HasCode(err, ipc.CodePathUnsafe) || fieldsOf(err)["kind"] != "replaceable" {
			t.Fatalf("NewListener in a %v holder = %v, want PATH_UNSAFE kind replaceable", r.holder, err)
		}
		//: the client refuses it too, on a directory and a socket that exist.
		rawListener(t, cfg.Path)
		if c, dialErr := ipc.Dial(t.Context(), &cfg); !errs.HasCode(dialErr, ipc.CodePathUnsafe) {
			if c != nil {
				closeOrLog(t, c)
			}
			t.Fatalf("Dial in a %v holder = %v, want PATH_UNSAFE", r.holder, dialErr)
		}
	}
}

// TestTheOperatingSystemsOwnLinksAreNotRefused runs a listener and a client
// under the system's shared directories, which macOS reaches through
// /tmp -> private/tmp and /var -> private/var, and most Linux distributions
// through /var/run -> /run: links the operating system ships, held by a
// directory nobody but root writes. A rule that refused a link for being a
// link would refuse every private socket on those kernels. /var/run is
// writable by root only, so that row runs where the suite does — a container,
// a CI job — and skips, saying so, elsewhere.
func TestTheOperatingSystemsOwnLinksAreNotRefused(t *testing.T) {
	t.Parallel()
	for _, shared := range []string{"/tmp", "/var/tmp", "/var/run"} {
		t.Run(shared, func(t *testing.T) {
			t.Parallel()
			base := shortBase(t, shared)
			crossed := false
			for _, component := range []string{shared, filepath.Dir(shared)} {
				if info, err := os.Lstat(component); err == nil && info.Mode()&os.ModeSymlink != 0 {
					crossed = true
				}
			}
			t.Logf("%s: the path crosses a link the system ships = %v", shared, crossed)
			cfg := ipc.Config{Path: filepath.Join(base, "run", "d.sock")}
			ln, err := ipc.NewListener(&cfg)
			if err != nil {
				t.Fatalf("NewListener under %s = %v, want a listener", shared, err)
			}
			defer closeOrLog(t, ln)
			c, err := ipc.Dial(t.Context(), &cfg)
			if err != nil {
				t.Fatalf("Dial under %s = %v, want a connection", shared, err)
			}
			closeOrLog(t, c)
		})
	}
}

// TestADirectoryAnotherAccountCreatedAboveTheSocketIsRefused is the foreign
// row on a real tree, which only root can build: a directory another account
// owns, in a world-writable sticky directory, holding the socket's directory.
// Whoever created it decides what is below it — on macOS, down to the
// access-control entries a directory made there inherits. Unprivileged, the
// same rule is pinned on synthetic steps by TestSteerable.
func TestADirectoryAnotherAccountCreatedAboveTheSocketIsRefused(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("only root can give a directory to another account; TestSteerable pins the rule unprivileged")
	}
	const nobody int = 65534
	base := shortBase(t, "/tmp")
	pub := filepath.Join(base, "pub")
	foreign := filepath.Join(pub, "app")
	if err := os.MkdirAll(foreign, 0o777); err != nil {
		t.Fatalf("building the tree = %v", err)
	}
	if err := os.Chmod(foreign, 0o777); err != nil {
		t.Fatalf("opening the foreign directory = %v", err)
	}
	if err := os.Chown(foreign, nobody, nobody); err != nil {
		t.Skipf("cannot give the directory to uid %d: %v", nobody, err)
	}
	if err := os.Chmod(pub, 0o777|os.ModeSticky); err != nil {
		t.Fatalf("setting the container's mode = %v", err)
	}
	_, err := ipc.NewListener(&ipc.Config{Path: filepath.Join(foreign, "run", "d.sock")})
	if !errs.HasCode(err, ipc.CodePathUnsafe) || fieldsOf(err)["kind"] != "foreign" {
		t.Fatalf("NewListener under a directory uid %d owns = %v, want PATH_UNSAFE kind foreign", nobody, err)
	}
	if _, statErr := os.Lstat(filepath.Join(foreign, "run")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the refused Listen still created its directory inside the foreign one: %v", statErr)
	}
}

// itoa renders a uid the way a field's StringValue does.
func itoa(n int) string {
	return errs.Int("n", n).StringValue()
}
