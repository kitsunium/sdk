//go:build linux

package server

import "syscall"

// kernelRelease is uname(2)'s release, "6.8.0-45-generic" say; empty when the
// call fails.
func kernelRelease() string {
	var u syscall.Utsname
	//: unreadable counts as unknown.
	if err := syscall.Uname(&u); err != nil {
		//: unknown.
		return ""
	}
	buf := make([]byte, 0, len(u.Release))
	//: a NUL-terminated array of int8 or uint8, by architecture.
	for _, c := range u.Release {
		//: the terminator.
		if c == 0 {
			break
		}
		buf = append(buf, byte(c))
	}
	//: the release.
	return string(buf)
}
