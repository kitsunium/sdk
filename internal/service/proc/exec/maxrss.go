package exec

// peakKiB converts a raw ru_maxrss figure into the KiB the
// coreproc.ExitValue.MaxRSS port field documents. unitsPerKiB is how many of
// the kernel's units make one KiB: 1 where getrusage(2) reports kilobytes
// (Linux, the BSDs), so the figure passes through unchanged, and 1024 where it
// reports bytes (darwin and ios), so the figure is divided and rounded up — a
// child that touched any memory at all never reads as 0 KiB.
//
// It is platform-neutral so its arithmetic is tested on every GOOS, not only on
// the one host whose kernel reports bytes.
func peakKiB(raw, unitsPerKiB int64) int64 {
	//: a kernel that already speaks KiB is passed through bit for bit, the
	//: behaviour this function must not change on Linux and the BSDs.
	if unitsPerKiB <= 1 {
		return raw
	}
	//: ru_maxrss is never negative; a value that is reads as no figure at all
	//: rather than as a negative size.
	if raw <= 0 {
		return 0
	}
	kib := raw / unitsPerKiB
	//: rounding up keeps a non-zero peak non-zero, and costs at most one KiB.
	if raw%unitsPerKiB != 0 {
		kib++
	}
	return kib
}
