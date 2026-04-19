// Package logger: version.go exposes the SDK version to the rest of the
// logger package. The ldflags pipeline injects the real value at build
// time; local development runs fall back to the "dev" sentinel.
package logger

// devVersion is the string returned by FrameworkVersion when Version was
// not set at link time (typical for `go run` / local `go test`).
const devVersion string = "dev"

// Version is overridden at build time via
//
//	go build -ldflags "-X github.com/kitsunium/sdk/pkg/v1/logger.Version=v0.1.0"
//
// Leave it as the zero value in local dev; FrameworkVersion returns "dev".
var Version string

// FrameworkVersion returns the linked-in SDK version, or "dev" if unset.
//
// Returns:
//   - string: the ldflags-injected Version, or "dev" fallback.
func FrameworkVersion() (v string) {
	//: empty means no -ldflags override — return the dev sentinel.
	if Version == "" {
		//: keep the sentinel stable so tests can pin expectations.
		return devVersion
	}
	//: hand back the linked-in version unchanged.
	return Version
}
