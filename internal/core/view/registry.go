// Package view — holds the process-wide [Factory] registry. Engine packages
// register themselves through package-level var initialisers when imported (no
// init()), mirroring core/codec and core/writer.
package view

import (
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// registry maps each [Engine] to its [Factory].
//
// snapshot.Value is the deliberate choice over sync.Map, for the same reason
// core/writer gives: an engine package registers exactly ONCE at import time
// and every other access is a lock-free Load (ADR 0011). Update serialises
// writers on a mutex so Register's read-modify-write publish is race-free;
// Lookup stays lock-free.
//
// # Why this domain HAS a registry when proc, resilience, net, scheduler,
// # token, session, lock and lifecycle do not
//
// Those domains have no registry because their alternatives are not
// interchangeable: swapping a memory lock for a file lock changes whether a
// lease can be taken from a live holder, and resolving that from a
// configuration string would let a typo change it silently (ADR 0052 §D10).
//
// A template engine is the other case. html/template, pongo2, quicktemplate and
// jet all answer the same question — a name plus a model becomes a document —
// and the SDK has an opinion about none of them. A registry is what lets the
// SDK ship the extension point without shipping the opinion: the engine that
// lives in third-party/ or in a framework registers itself and is reached
// through the same [Open] as the built-in one, with no fork of the port.
//
// It is worth being precise about what it does NOT buy, because the codec and
// writer registries do buy that and this one does not. Swapping engines is not
// a deployment decision: template syntax differs between them, so changing the
// engine invalidates every template file on disk. Nobody flips this in a
// config map between staging and production, and a reader who assumes otherwise
// will design a fallback that cannot work.
//
// # And the hazard a string-keyed registry creates here
//
// A registry keyed by a name from configuration is exactly how "text/template
// must not be reachable by accident" becomes reachable by accident: one typo,
// one merged config map, and the engine that escapes has been replaced by one
// that does not, with every call still succeeding. That is why the registry's
// membership rule is not documentary — the contract in [Factory] requires
// contextual escaping for the content type an engine reports, no non-escaping
// engine has a name here, and a named AST audit in internal/service/view fails
// the build if text/template is imported anywhere in the domain.
var registry snapshot.Value[map[Engine]Factory]

// loadRegistry returns the current snapshot, or nil before any registration.
func loadRegistry() map[Engine]Factory {
	//: nil pointer means no engine has registered yet.
	current := registry.Load()
	if current == nil {
		//: hand back nil so callers see a clean miss.
		return nil
	}
	//: dereference the published snapshot for caller reads.
	return *current
}

// Register inserts f under f.Engine() and returns it, so an engine package can
// bind the singleton to a typed package-level variable:
//
//	var Engine = view.Register(htmlFactory{})
//
// It panics on a nil factory, on the empty [Engine] name, and when a DISTINCT
// factory already claims the name. Re-registering the same factory is a no-op.
//
// IFACE-PLUGIN: the registry hands plug-in Factory instances back to callers so
// each engine keeps its concrete type unexported; the stable contract is the
// Factory interface itself.
func Register(f Factory) Factory {
	//: a nil registration is always a programming error, and it is one the
	//: importing package can fix — so it fails at import, not at first render.
	if f == nil {
		//: panic so the offending import is visible at boot.
		panic(EngineInvalid.Error())
	}
	name := f.Engine()
	//: Engine("") is the reserved invalid zero value; refusing it at boot keeps
	//: it out of Lookup, Open and Available forever.
	if name == "" {
		//: panic so the offending factory is visible at boot.
		panic(EngineInvalid.Error())
	}
	//: publish under the snapshot's writer lock; a duplicate name is a conflict.
	if err := publish(name, f); err != nil {
		//: surface the dotted-quad code in the panic for grep-friendly boot logs.
		panic(err.Error())
	}
	//: returning the factory lets an engine bind it to a typed singleton var.
	return f
}

// publish inserts (name → f) under the snapshot writer lock, returning a
// non-nil error when name is already taken by a different factory.
func publish(name Engine, f Factory) error {
	//: dupErr escapes the Update closure to signal a conflicting registration.
	var dupErr error
	//: Update serialises writers, so the duplicate check and the publish are
	//: atomic against any concurrent Register.
	registry.Update(func(current *map[Engine]Factory) *map[Engine]Factory {
		//: nil snapshot is the very-first-Register case; nothing to collide with.
		if current != nil {
			//: an existing binding is either the same factory or a hard conflict.
			if existing, taken := (*current)[name]; taken {
				//: re-registering the SAME factory is an idempotent no-op.
				if existing == f {
					//: republish unchanged — nothing to add, nothing to report.
					return current
				}
				dupErr = errs.Wrap(DuplicateEngine, errs.WrapParams{},
					errs.String("engine", string(name)))
				//: republish unchanged; the caller turns dupErr into a panic.
				return current
			}
		}
		//: clone + insert, then publish atomically. Registration is import-time,
		//: one-shot work — this clone never lands on a request path.
		next := make(map[Engine]Factory, registrySize(current)+1)
		//: copy the previous bindings forward so no engine is lost on republish.
		if current != nil {
			maps.Copy(next, *current)
		}
		next[name] = f
		//: hand the new map back for the snapshot to publish atomically.
		return &next
	})
	//: non-nil only when a DISTINCT factory already claimed the name.
	return dupErr
}

// registrySize returns len of the snapshot, treating nil as empty.
func registrySize(current *map[Engine]Factory) int {
	//: nil snapshot is the very-first-Register case.
	if current == nil {
		//: an unpublished registry holds nothing.
		return 0
	}
	//: the published map's size is the allocation hint for the clone.
	return len(*current)
}

// Lookup returns the [Factory] registered under name.
//
// IFACE-PLUGIN: the registry hands plug-in Factory instances back to callers so
// each engine keeps its concrete type unexported; the stable contract is the
// Factory interface itself.
func Lookup(name Engine) (factory Factory, found bool) {
	//: a lock-free read of the published snapshot.
	factory, found = loadRegistry()[name]
	//: an absent name is a clean miss, never an error — Open turns it into one.
	return factory, found
}

// Available returns every registered [Engine], sorted, so a caller can print
// what its imports actually wired up.
func Available() []Engine {
	//: sorted output keeps logs and tests deterministic.
	return slices.Sorted(maps.Keys(loadRegistry()))
}

// Open resolves name and builds a [Renderer] from cfg.
//
// It is the config-driven entry point, and it fails loudly on a name nobody
// claims — [EngineUnknown] carrying the registered names — rather than falling
// back to a default. A fallback here would mean a typo in a configuration file
// silently choosing how every value in the program is escaped.
//
// IFACE-PLUGIN: the engine's concrete Renderer stays unexported in
// internal/service/view; the stable contract is the Renderer interface.
func Open(name Engine, cfg Config) (renderer Renderer, err error) {
	factory, found := Lookup(name)
	//: no fallback: a name nobody claims is a refusal, never a default engine.
	if !found {
		//: name the registered engines: the usual cause is an unimported package.
		return nil, errs.Wrap(EngineUnknown, errs.WrapParams{},
			errs.String("engine", string(name)),
			errs.Int("registered", len(loadRegistry())))
	}
	//: the factory owns every Config check; the registry adds none of its own.
	return factory.New(cfg)
}
