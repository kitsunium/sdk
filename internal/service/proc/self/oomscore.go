package self

// SetOOMScoreAdj sets the running process's own OOM-killer bias, writing n to
// /proc/self/oom_score_adj (-1000 to 1000): a supervisor that must outlive its
// children lowers it, which needs CAP_SYS_RESOURCE, below the value it started
// with. It is the one writer of this package. A value the kernel refuses is
// RlimitFailed; off Linux it is UnsupportedPlatform.
func SetOOMScoreAdj(n int) error {
	//: the platform's writer, or its refusal.
	return setOOMScoreAdj(n)
}
