// Package selfupdate replaces the running binary with a newer signed release.
package selfupdate

import coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"

// UpdateValue is the outcome of a version check or an install. It ALIASES the
// core value rather than redeclaring it: a service-local copy of a value core
// owns compiles fine and drifts silently.
type UpdateValue = coreupd.UpdateValue
