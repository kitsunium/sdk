//go:build !linux

package service

// parentOf knows no parent off Linux: no /proc to read it from, so
// GuessMainPID= guesses nothing there and the main process stays unknown.
func parentOf(int) (int, bool) {
	//: unknown.
	return 0, false
}
