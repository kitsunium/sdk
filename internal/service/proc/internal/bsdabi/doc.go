// Package bsdabi decodes, from their bytes, the records the BSD kernels and
// darwin hand the proc family: a process's kinfo_proc (kinfo_proc2 on NetBSD),
// read by sysctl(3) for its start time and its users, and the sockcred2
// FreeBSD attaches to a datagram for its sender.
//
// Every structure is laid out as its header declares it, for the
// architecture's ABI, and a record is decoded only once it proves to be the
// one asked for — its size, the size it states where it states one, the pid
// it names —, never guessed at. The package holds no system call and no
// build constraint: the caller passes runtime.GOOS and runtime.GOARCH, and
// the tests read every layout on any machine, from records built member by
// member after the headers. It sits under internal/, where Go's rule confines
// it to the proc family, as rlim and cgroupfs do.
package bsdabi
