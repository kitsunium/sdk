//go:build unix

// Package exec — the procfs control-file writer.
package exec

import (
	"os"
	"path/filepath"
	"testing"
)

// Test_writeProcFile pins the shim: a single short write, and a failure that is
// returned rather than dropped.
//
// The mode is load-bearing only for the signature — procfs ignores it for
// existing entries — but the ERROR is not: this shim is how an OOM bias reaches
// the kernel, and a swallowed failure means a process that asked to be the OOM
// killer's first choice quietly is not.
func Test_writeProcFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	type tc struct {
		name    string
		path    string
		data    string
		wantErr bool
	}
	tests := []tc{
		{name: "a writable path", path: filepath.Join(dir, "score"), data: "-500"},
		{name: "an empty value", path: filepath.Join(dir, "empty"), data: ""},
		{name: "a path under a directory that does not exist", path: filepath.Join(dir, "absent", "score"), data: "0", wantErr: true},
		{name: "a directory where a file belongs", path: dir, data: "0", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := writeProcFile(c.path, c.data)
		if c.wantErr {
			if err == nil {
				t.Fatalf("writeProcFile(%s) = nil, want an error", c.name)
			}
			return
		}
		if err != nil {
			t.Fatalf("writeProcFile(%s) = %v, want nil", c.name, err)
		}
		got, rerr := os.ReadFile(c.path)
		if rerr != nil {
			t.Fatalf("reading back: %v", rerr)
		}
		//: exactly the value, with nothing appended — procfs rejects a
		//: trailing byte it did not ask for.
		if string(got) != c.data {
			t.Errorf("the file contains %q, want %q", got, c.data)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
