// Package serverengine is what framework/app/server plugs into kit: the HTTP
// engine an app in the server profile serves on (the SDK's net/server), the
// frontends' file server (the SDK's net/static), and the process sampler of
// the runtime graph a serving app answers (the SDK's proc/process, and through
// it runtime/metrics and the spawn engine). kit imports none of them: a
// daemon or a CLI neither links nor initialises them (ADR 0172).
//
// Package serverengine — the compile-time proof that the engine is kit's.
package serverengine
