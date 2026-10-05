package view

import (
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// registry maps each [Engine] to its [Factory] — the kernel's read-mostly,
// copy-on-write table (kernel/plugin.Registry, ADR 0159), for the same reason
// core/observe/logger/writer gives: an engine package registers exactly ONCE at
// import time and every other access is a lock-free Lookup (ADR 0011). What
// stays here is the domain's refusal: the empty name and the conflict.
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
// engine has a name here, and a named AST audit in internal/service/app/view fails
// the build if text/template is imported anywhere in the domain.
var registry plugin.Registry[Engine, Factory]

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
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func Register(f Factory) Factory {
	//: a nil registration is always a programming error, and it is one the
	//: importing package can fix — so it fails at import, not at first render.
	if why := plugin.Unusable(f); why != "" {
		//: panic so the offending import is visible at boot.
		panic(EngineInvalid.Error() + ": " + why)
	}
	name := f.Engine()
	//: Engine("") is the reserved invalid zero value; refusing it at boot keeps
	//: it out of Lookup, Open and Available forever.
	if name == "" {
		//: panic so the offending factory is visible at boot.
		panic(EngineInvalid.Error())
	}
	//: publish; a duplicate name is a conflict.
	if err := publish(name, f); err != nil {
		//: surface the dotted-quad code in the panic for grep-friendly boot logs.
		panic(err.Error())
	}
	//: returning the factory lets an engine bind it to a typed singleton var.
	return f
}

// publish inserts (name → f) into the registry, returning a non-nil error when
// name is already taken by a different factory. The check and the insert are
// one atomic step of the kernel table.
func publish(name Engine, f Factory) error {
	//: a free name or the same factory under its own name: nothing to refuse.
	if !registry.Publish(name, f) {
		//: published, or already there — re-registering is an idempotent no-op.
		return nil
	}
	//: a DISTINCT factory already claimed the name; the caller panics with it.
	return errs.Wrap(DuplicateEngine, errs.WrapParams{},
		errs.String("engine", string(name)))
}

// Lookup returns the [Factory] registered under name.
//
// IFACE-PLUGIN: the registry hands plug-in Factory instances back to callers so
// each engine keeps its concrete type unexported; the stable contract is the
// Factory interface itself.
func Lookup(name Engine) (factory Factory, found bool) {
	//: a lock-free read of the published snapshot; an absent name is a clean
	//: miss, never an error — Open turns it into one.
	return registry.Lookup(name)
}

// available is Available's body: decl_gen.go writes Available, from the
// design, as one call of it.
func available() []Engine {
	//: sorted output keeps logs and tests deterministic.
	return registry.Names()
}

// Open resolves name and builds a [Renderer] from cfg.
//
// It is the config-driven entry point, and it fails loudly on a name nobody
// claims — [EngineUnknown] carrying the registered names — rather than falling
// back to a default. A fallback here would mean a typo in a configuration file
// silently choosing how every value in the program is escaped.
//
// IFACE-PLUGIN: the engine's concrete Renderer stays unexported in
// internal/service/app/view; the stable contract is the Renderer interface.
func Open(name Engine, cfg Config) (renderer Renderer, err error) {
	factory, found := Lookup(name)
	//: no fallback: a name nobody claims is a refusal, never a default engine.
	if !found {
		//: name the registered engines: the usual cause is an unimported package.
		return nil, errs.Wrap(EngineUnknown, errs.WrapParams{},
			errs.String("engine", string(name)),
			errs.Int("registered", len(registry.Names())))
	}
	//: the factory owns every Config check; the registry adds none of its own.
	return factory.New(cfg)
}
