//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly)

package vfs

import (
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// platformNative reports that this GOOS lacks at least one mechanic the disk
// filesystem requires. NewOS reads it and refuses at CONSTRUCTION, so the
// refusal arrives where the program is wired rather than at the first publish.
const platformNative bool = false

// syncDirHandle reports the shared UnsupportedPlatform sentinel. It is
// unreachable in practice — the constructor refuses first — and exists so the
// package compiles on every GOOS, which is ADR 0018's build bar.
func syncDirHandle(_ *os.File) error {
	//: the same typed answer every unimplemented primitive gives, everywhere.
	return coreproc.UnsupportedPlatform
}
