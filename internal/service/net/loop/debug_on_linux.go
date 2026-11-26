//go:build linux && loopdebug

package loop

import "syscall"

// loopDebug reports whether the loopdebug build tag compiled the thread
// check in.
const loopDebug = true

// threadCheck returns the loopdebug check that the caller runs on thread tid.
func threadCheck(tid int) func() bool {
	//: Gettid is the thread, whatever goroutine runs on it.
	return func() bool { return syscall.Gettid() == tid }
}

// writableEdge, when a test sets it before its group starts, is called on
// the loop's thread with a connection's descriptor each time epoll reports
// the connection writable, before its output is flushed: the instant the
// kernel judged the socket writable, which a test reads the socket's queues
// at (#857). The loopdebug build tag compiles it in; without it the call is
// an empty function.
var writableEdge func(fd int)

// atWritableEdge calls writableEdge with fd, when set.
func atWritableEdge(fd int32) {
	//: set by a test.
	if edge := writableEdge; edge != nil {
		edge(int(fd))
	}
}
