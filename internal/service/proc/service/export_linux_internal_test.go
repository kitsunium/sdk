//go:build linux && (amd64 || arm64)

package service

// ForceHandoverVersion makes Reexec write version v: the external tests of
// an image that reads another version than the one handed (S0h).
func ForceHandoverVersion(v uint16) {
	versionSeam = func() uint16 { return v }
}

// ForceBeforeExec makes Reexec call f once the payload is written and before
// the exec: the external tests of an image handed a payload it must refuse,
// or a descriptor that is no longer the one recorded (S0h).
func ForceBeforeExec(f func(payload []byte)) {
	tamperPayload = f
}
