//go:build unix && !linux && !darwin

package self

// maxRSSUnit is the byte count of one ru_maxrss unit: the BSDs report KiB.
// illumos and Solaris define the field and leave it 0.
const maxRSSUnit uint64 = 1024
