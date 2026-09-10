package vfs_test

import (
	"io/fs"
	"testing"

	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
)

// The paths the guards are measured against: the shallow name most callers use,
// a deep one that costs whatever fs.ValidPath's element scan costs, and the two
// refusals — one lexical, one the write grammar's own.
const (
	benchShallowPath string = "index.html"
	benchDeepPath    string = "assets/css/vendor/normalize/v8/normalize.min.css"
	benchEscapePath  string = "../../etc/passwd"
	benchRootPath    string = "."
)

// benchSink keeps a compared error from being optimised away. Every guard in
// this package returns an error or nil, and nil is the hot answer, so without a
// sink the compiler is free to delete the call being measured.
var benchSink error

// BenchmarkValidatePath measures the read grammar on the name shape callers
// actually pass. It is on every single call into either filesystem, so its cost
// is a floor under every other number in this domain.
func BenchmarkValidatePath(b *testing.B) {
	cases := []struct {
		name string
		path string
	}{
		{"shallow", benchShallowPath},
		{"deep", benchDeepPath},
		{"escaping", benchEscapePath},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			var err error
			for b.Loop() {
				err = corevfs.ValidatePath(tc.path)
			}
			benchSink = err
		})
	}
}

// BenchmarkValidateWritePath measures the write grammar, which is ValidatePath
// plus the root refusal. The gap between the two is the cost of that one extra
// comparison, and it should be indistinguishable.
func BenchmarkValidateWritePath(b *testing.B) {
	cases := []struct {
		name string
		path string
	}{
		{"shallow", benchShallowPath},
		{"deep", benchDeepPath},
		{"root", benchRootPath},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			var err error
			for b.Loop() {
				err = corevfs.ValidateWritePath(tc.path)
			}
			benchSink = err
		})
	}
}

// BenchmarkValidatePerm measures the mode guard on both answers. The refusing
// path allocates because it builds a typed error carrying the offending mode as
// a field; the accepting path must not, and that asymmetry is the point.
func BenchmarkValidatePerm(b *testing.B) {
	cases := []struct {
		name string
		perm fs.FileMode
	}{
		{"accepted", 0o644},
		{"zero", 0},
		{"setuid", fs.ModeSetuid | 0o755},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			var err error
			for b.Loop() {
				err = corevfs.ValidatePerm(tc.perm)
			}
			benchSink = err
		})
	}
}
