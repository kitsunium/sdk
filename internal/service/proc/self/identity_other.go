//go:build !linux && !windows

package self

import "time"

// kernelStart is not read here: darwin and the BSDs keep it in a kinfo_proc
// whose layout differs per OS and per release, which the SDK does not bind;
// the package's initialisation stands in, and StartedAtSource says so.
func kernelStart() (time.Time, bool) {
	//: the init time stands.
	return time.Time{}, false
}

// pidNamespace: pid namespaces are Linux's.
func pidNamespace() (inode uint64, nsPid1 bool) {
	//: none.
	return 0, false
}
