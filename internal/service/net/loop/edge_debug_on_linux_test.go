//go:build linux && loopdebug

package loop

// hookWritableEdge installs f as the epoll backend's writability hook until
// restore runs (#857); the loopdebug build tag compiles the hook in.
func hookWritableEdge(f func(fd int)) (restore func(), ok bool) {
	saved := writableEdge
	writableEdge = f
	return func() { writableEdge = saved }, true
}
