// Package crypto — white-box wiring check for the StreamSealer registry.
package crypto

import (
	"io"
	"slices"
	"strings"
	"testing"
)

// : the stub must satisfy StreamSealer at compile time, exactly as a real scheme
// : does.
var _ StreamSealer = (*stubStreamSealer)(nil)

// stubStreamSealer is a comparable in-package StreamSealer for wiring checks.
type stubStreamSealer struct {
	id  Algorithm
	tag int
}

// Algorithm keys the stub in the registry.
func (s stubStreamSealer) Algorithm() Algorithm { return s.id }

// Writer passes bytes through unchanged; confidentiality is not what this file
// pins, and a stub that pretended otherwise would be worse than an honest one.
func (stubStreamSealer) Writer(_ Key, dst io.Writer, _ []byte) (io.WriteCloser, error) {
	return nopWriteCloser{dst}, nil
}

// Reader passes bytes through unchanged.
func (stubStreamSealer) Reader(_ Key, src io.Reader, _ []byte) (io.Reader, error) {
	return src, nil
}

// nopWriteCloser adds a no-op Close to a plain writer.
type nopWriteCloser struct{ io.Writer }

// Close reports success without touching the underlying writer.
func (nopWriteCloser) Close() error { return nil }

// Test_streamSealers pins that the package-level registry is the one the public
// accessors read, and that it names its own registrar. The verb surfaces only
// in a boot-time panic — the one moment nobody can afford a message pointing at
// the wrong registrar, which is exactly what a copy-pasted registry file gives.
func Test_streamSealers(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		alg  Algorithm
	}
	tests := []tc{
		{"a fresh algorithm", "stub-stream-wiring-a"},
		{"another fresh algorithm", "stub-stream-wiring-b"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: publish straight into the private registry, bypassing the public
		//: registrar, so what is under test is the wiring and nothing else.
		if err := streamSealers.publish(c.alg, stubStreamSealer{id: c.alg}); err != nil {
			t.Fatalf("publish(%q) = %v, want nil", c.alg, err)
		}
		if _, ok := LookupStreamSealer(c.alg); !ok {
			t.Errorf("LookupStreamSealer(%q) missed what streamSealers.publish stored", c.alg)
		}
		if !slices.Contains(AvailableStreamSealers(), c.alg) {
			t.Errorf("AvailableStreamSealers() omits %q", c.alg)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the conflict message is the only place the registrar verb ever
	//: surfaces, and it surfaces at boot — a copy-pasted registry file would
	//: send the reader to the wrong source file at the worst moment.
	const clash Algorithm = "stub-stream-wiring-clash"
	if err := streamSealers.publish(clash, stubStreamSealer{id: clash, tag: 1}); err != nil {
		t.Fatalf("publish(%q) = %v, want nil", clash, err)
	}
	err := streamSealers.publish(clash, stubStreamSealer{id: clash, tag: 2})
	if err == nil {
		t.Fatalf("a distinct value under %q published without conflict", clash)
	}
	if !strings.Contains(err.Error(), "RegisterStreamSealer") {
		t.Errorf("the conflict on %q reads %q, want it to name RegisterStreamSealer", clash, err.Error())
	}
}
