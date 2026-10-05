package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// MountOption configures the mount of a module: [Prefix], [Bind].
type MountOption = ikit.MountConfigurer

// Mount mounts module m with options: [Prefix] serves its routes elsewhere
// than "/<module>/", [Bind] chooses what one of its ports calls.
//
//	app := kit.NewApp("vigie", identity.Service).
//		With(kit.Mount(moderation.Module, kit.Prefix("/"), kit.Bind(moderation.Enforcer, posts.EnforceAPI)))
//
// A module is mounted once per app: mounted again, its prefix is the last
// one given, and its bindings join the others, the last of a port winning.
//
//go:noinline
func Mount(m *Module, opts ...MountOption) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Mount(m, opts...)
}

// Prefix serves a module's routes under prefix — "/trust/" — instead of
// "/<module>/"; "/" shares the product's route space, where a route both
// declare is refused as any two are. A prefix starts and ends with '/', and
// never lies under /_kit/.
//
//go:noinline
func Prefix(prefix string) MountOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Prefix(prefix)
}
