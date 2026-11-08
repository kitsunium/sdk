package proc

// The realtime signal range of FreeBSD's kernel (sys/signal.h: SIGRTMIN 65,
// SIGRTMAX 126).
const (
	// rtMin is SIGRTMIN.
	rtMin Signal = 65
	// rtMax is SIGRTMAX.
	rtMax Signal = 126
)
