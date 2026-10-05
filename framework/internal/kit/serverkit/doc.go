// Package serverkit is what framework/kit/server plugs into kit: the HTTP
// engine an app in the server profile serves on (the SDK's net/server) and the
// frontends' file server (the SDK's net/static). kit imports neither: a
// daemon or a CLI neither links nor initialises them.
//
// Package serverkit — the compile-time proof that the engine is kit's.
package serverkit
