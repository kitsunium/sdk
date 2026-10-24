//go:build linux && !loopdebug

package loop

// loopDebug reports whether the loopdebug build tag compiled the thread
// check in.
const loopDebug = false

// threadCheck compiles no check without the loopdebug build tag.
func threadCheck(_ int) func() bool {
	//: no check.
	return nil
}
