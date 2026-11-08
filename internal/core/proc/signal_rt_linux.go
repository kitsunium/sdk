package proc

// The realtime signal range as a Linux program linked against glibc sees it:
// the kernel numbers 32 to 64, and glibc keeps 32 and 33 for its threads
// (SIGCANCEL, SIGSETXID), so SIGRTMIN is 34. A cgo build linking another libc
// may move SIGRTMIN; a pure Go program sees this range.
const (
	// rtMin is SIGRTMIN.
	rtMin Signal = 34
	// rtMax is SIGRTMAX.
	rtMax Signal = 64
)
