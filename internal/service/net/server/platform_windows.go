//go:build windows

package server

import (
	"strconv"
	"syscall"
)

// kernelRelease is major.minor.build as GetVersion reports it. Windows answers
// GetVersion with the version the binary's manifest declares support for, so
// an unmanifested binary on Windows 10 or 11 reads 6.2; the build number is
// the real one. Empty when the call fails.
func kernelRelease() string {
	v, err := syscall.GetVersion()
	//: unreadable counts as unknown.
	if err != nil {
		//: unknown.
		return ""
	}
	major, minor, build := v&0xff, (v>>8)&0xff, (v>>16)&0xffff
	//: dotted.
	return strconv.FormatUint(uint64(major), 10) + "." + strconv.FormatUint(uint64(minor), 10) + "." + strconv.FormatUint(uint64(build), 10)
}
