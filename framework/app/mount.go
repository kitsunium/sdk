package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// MountOption configures the mount of a module: [Prefix], [Bind], [Only].
type MountOption = iapp.MountConfigurer

// Mount mounts module m with options: [Prefix] serves its routes elsewhere
// than "/<module>/", [Bind] chooses what one of its ports calls.
//
//	a := app.NewApp("vigie", identity.Service).
//		With(app.Mount(moderation.Module, app.Prefix("/"), app.Bind(moderation.Enforcer, posts.EnforceAPI)))
//
// A module is mounted once per app: mounted again, its prefix is the last
// one given, and its bindings join the others, the last of a port winning.
//
//go:noinline
func Mount(m *Module, opts ...MountOption) AppOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Mount(m, opts...)
}

// Prefix serves a module's routes under prefix — "/trust/" — instead of
// "/<module>/"; "/" shares the product's route space, where a route both
// declare is refused as any two are. A prefix starts and ends with '/', and
// never lies under /_app/, nor under /_kit/ while its probes answer there.
//
//go:noinline
func Prefix(prefix string) MountOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Prefix(prefix)
}

// Only makes a mount hold in the environments envs names alone — the name
// <PRODUCT>_ENV or [Env] gives, lower-cased, "production" when neither does:
//
//	a := app.NewApp("vigie", identity.Service).
//		With(app.Mount(debugbar.Module, app.Only("dev", "staging")))
//
// Elsewhere the mount is as if it had not been given, its [Prefix] and its
// [Bind] with it; another mount of the module without Only still mounts it.
// Several Only on one mount add up. The start refuses an Only that names no
// environment, a name that cannot be one — lower-case letters, digits and
// dashes inside, 63 at most —, and a module a mounted module requires
// ([Requires]) that its mount leaves out, rather than mounting it at its
// defaults behind the product's back.
//
//go:noinline
func Only(envs ...string) MountOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Only(envs...)
}
