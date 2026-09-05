//go:build unix

// Package sdlisten_test — Unix facade contract for socket activation. The
// off-Unix half lives in sdlisten_other_test.go, gated on the mirror tag, so
// neither file has to guess the platform at runtime.
package sdlisten_test

import (
	"net"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/sdlisten"
)

// TestRecoveryWithoutActivation pins the contract that lets a service call the
// recovery functions unconditionally at startup: outside an activation
// environment they yield an empty set and NO error. A binary run from a shell
// inherits nothing, and refusing to start over it would turn socket activation
// into an all-or-nothing deployment choice rather than an optional one.
func TestRecoveryWithoutActivation(t *testing.T) {
	type tc struct {
		name string
		call func() (int, error)
	}
	tests := []tc{
		{"Files", func() (int, error) {
			f, err := sdlisten.Files(false)
			return len(f), err
		}},
		{"Listeners", func() (int, error) {
			l, err := sdlisten.Listeners(false)
			return len(l), err
		}},
		{"WithNames", func() (int, error) {
			n, err := sdlisten.WithNames(false)
			return len(n), err
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: an absent LISTEN_PID is what makes an activation set foreign, and it
		//: is the shape every non-activated process sees.
		t.Setenv("LISTEN_PID", "")
		t.Setenv("LISTEN_FDS", "")
		t.Setenv("LISTEN_FDNAMES", "")

		got, err := c.call()
		//: an error here would make the call unusable at unconditional startup.
		if err != nil {
			t.Fatalf("%s outside an activation environment = %v, want nil", c.name, err)
		}
		//: anything recovered would be another process's descriptors.
		if got != 0 {
			t.Errorf("%s recovered %d sockets with no activation", c.name, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestForeignActivationSetIsIgnored pins that a set addressed elsewhere is
// ignored rather than adopted. Adopting another process's descriptors is a
// correctness failure the kernel cannot catch for us, since those numbers are
// very likely open in this process too — just on something else.
//
// A malformed count is deliberately NOT in that bucket: an activator that wrote
// a garbled LISTEN_FDS is broken, and reporting it typed is more useful than
// starting with silently zero listeners.
func TestForeignActivationSetIsIgnored(t *testing.T) {
	type tc struct {
		name      string
		listenPID string
		listenFDS string
		wantErr   bool
	}
	tests := []tc{
		{"addressed to another pid", "1", "2", false},
		{"a non-numeric pid", "not-a-pid", "2", false},
		{"a zero fd count", "1", "0", false},
		{"a non-numeric fd count", "1", "many", true},
		{"a negative fd count", "1", "-1", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Setenv("LISTEN_PID", c.listenPID)
		t.Setenv("LISTEN_FDS", c.listenFDS)

		files, err := sdlisten.Files(false)
		//: close whatever was adopted before asserting, so one bad case does
		//: not leak descriptors into the rest of the run.
		for _, f := range files {
			if cerr := f.Close(); cerr != nil {
				t.Errorf("closing an adopted descriptor: %v", cerr)
			}
		}
		if c.wantErr {
			//: a broken activator must be reported, not absorbed.
			if err == nil {
				t.Fatalf("Files with %s = nil, want an error", c.name)
			}
			return
		}
		//: a foreign set is an empty result, never an error.
		if err != nil {
			t.Fatalf("Files with %s = %v, want nil", c.name, err)
		}
		if len(files) != 0 {
			t.Errorf("adopted %d descriptors from a foreign activation set", len(files))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestPrepareWithNothingToPass pins the activator side: handed nothing to pass
// on, Prepare must leave the spec alone rather than setting an empty LISTEN_FDS
// the child would then dutifully try to honour.
func TestPrepareWithNothingToPass(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		named map[string]net.Listener
	}
	tests := []tc{
		{"a nil map", nil},
		{"an empty map", map[string]net.Listener{}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		spec := &sdlisten.Spec{Path: "/bin/true"}
		before := len(spec.Env)

		if err := sdlisten.Prepare(spec, c.named); err != nil {
			t.Fatalf("Prepare with %s = %v, want nil", c.name, err)
		}
		//: an added LISTEN_FDS would tell the child to look for fds that the
		//: activator never passed.
		if len(spec.Env) != before {
			t.Errorf("Prepare added %d env entries for an empty listener set", len(spec.Env)-before)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
