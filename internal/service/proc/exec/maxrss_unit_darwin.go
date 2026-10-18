//go:build darwin

package exec

// rssUnitsPerKiB is 1024 because darwin's getrusage(2) and wait4(2) report
// ru_maxrss in bytes, not in the kilobytes Linux and the BSDs use. The darwin
// constraint also holds for GOOS=ios, whose kernel is the same.
const rssUnitsPerKiB int64 = 1024
