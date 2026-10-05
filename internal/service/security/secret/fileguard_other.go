//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly)

package secret

// platformNative reports that this GOOS lacks a mechanic the file store needs,
// so NewFile refuses with core/proc.UnsupportedPlatform before creating the
// directory it could then never use.
const platformNative bool = false
