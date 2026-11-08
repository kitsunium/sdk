//go:build linux || illumos || solaris

package exec

// fdDir lists the process's open descriptors: procfs's per-process fd
// directory, complete on Linux, illumos and Solaris.
const fdDir string = "/proc/self/fd"
