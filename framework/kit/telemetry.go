package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// telemetry is Telemetry's body: decl_gen.go writes Telemetry, from the
// design, as one call of it.
func telemetry(path string, gids ...int) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Telemetry(path, gids...)
}

// designDigest is DesignDigest's body: decl_gen.go writes DesignDigest, from the
// design, as one call of it.
func designDigest(digest string) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.DesignDigest(digest)
}
