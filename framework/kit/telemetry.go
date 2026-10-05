package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Telemetry exports the app's telemetry on the private socket at path —
// absolute, and short enough for a Unix socket (pkg/v1/proc/ipc) — admitting the groups gids besides the
// product's own account. It wins over KIT_TELEMETRY.
func Telemetry(path string, gids ...int) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Telemetry(path, gids...)
}

// DesignDigest records the digest of the design the product's code was
// generated from — what the generated wiring passes —, so the telemetry
// handshake can say it and a tool attached to the product can tell a process
// built from another design.
func DesignDigest(digest string) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.DesignDigest(digest)
}
