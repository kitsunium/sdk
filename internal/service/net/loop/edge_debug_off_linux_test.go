//go:build linux && !loopdebug

package loop

// hookWritableEdge installs nothing: the writability hook is compiled under
// the loopdebug build tag only, which the suite's Bazel target and its lanes
// set (#857).
func hookWritableEdge(func(fd int)) (restore func(), ok bool) {
	return func() {}, false
}
