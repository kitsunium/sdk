// Package cgroupfs reads where this process sits in the control-group
// filesystem: where each hierarchy is mounted and which part of it a mount
// exposes (/proc/self/mountinfo, octal escapes decoded), which cgroup the
// process belongs to (/proc/self/cgroup), the translation between the two,
// and the directory of its own unified cgroup.
//
// It was memlimit's until #1097 and #938 needed the same reading: cgroup's
// Probe names the caller's own group, and self's identity its container. One
// reading is not worth an edge from one engine to another, so it sits under
// internal/, where Go's rule confines it to the proc family, as rlim does.
// Every function takes the file reader, so a test drives each shape without a
// cgroup filesystem.
package cgroupfs
