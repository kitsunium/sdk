package writer

import (
	"fmt"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// registry maps each writer Name to its Factory — the kernel's read-mostly,
// copy-on-write table (kernel/plugin.Registry, ADR 0159): writer packages
// register exactly ONCE at import time and every other access is a lock-free
// Lookup (ADR 0011) — the same read-mostly shape the codec registry has. What
// stays here is the domain's refusal: the empty Name and the conflict's code.
var registry plugin.Registry[Name, Factory]

// Register inserts f into the registry under f.Name() and returns it so callers
// can bind the singleton to a typed package-level variable like
// `var Writer = writer.Register(&fileFactory{})`. Panics on a nil factory or
// when a distinct factory already claims the same Name.
//
// IFACE-PLUGIN: the registry hands plug-in factory instances back to callers
// so each writer keeps its concrete type unexported; the stable contract is
// the Factory interface itself.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func Register(f Factory) Factory {
	//: a typed nil and a non-comparable plug-in both satisfy the port and
	//: neither can serve — refuse at import, where the offender is named.
	if why := plugin.Unusable(f); why != "" {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("writer.Register [%s WRITER_NIL]: %s", CodeWriterNil, why))
	}
	//: the canonical Name is the primary key.
	name := f.Name()
	//: Name("") is the reserved invalid zero value (writer.go); reject it at
	//: boot so it can never leak into Lookup / Open / Available.
	if name == "" {
		//: panic so the offending factory is visible at boot.
		panic(fmt.Sprintf("writer.Register [%s WRITER_NAME_EMPTY]: empty Name", CodeWriterNameEmpty))
	}
	//: publish the factory; a distinct factory under a taken Name is a hard conflict.
	if err := publishFactory(name, f); err != nil {
		//: the typed conflict's dotted-quad header, then the Name that collided.
		panic(conflictText(err))
	}
	//: returning the factory lets callers bind it to a typed singleton var.
	return f
}

// publishFactory inserts (name → f) into the registry. Returns a non-nil error
// when name is already registered to a different factory; the caller turns it
// into a boot-time panic. Re-registering the SAME factory is idempotent
// (interface == compares the factory pointers). The check and the insert are
// one atomic step of the kernel table.
func publishFactory(name Name, f Factory) error {
	//: a free name or the same factory under its own name: nothing to refuse.
	if !registry.Publish(name, f) {
		//: published, or already there.
		return nil
	}
	//: a DISTINCT factory under a taken Name is the hard conflict: the typed
	//: sentinel, the fields naming the registrar and Name.
	return errs.Wrap(DuplicateRegistration, errs.WrapParams{},
		errs.String("registrar", "writer.Register"), errs.String("name", string(name)))
}

// Lookup returns the factory registered under n.
//
// IFACE-PLUGIN: the registry stores plug-in factory instances behind the
// Factory interface — concrete types are intentionally unexported per writer.
func Lookup(n Name) (f Factory, ok bool) {
	//: a lock-free snapshot read; a miss hands back nil AND false.
	return registry.Lookup(n)
}

// Open resolves n to its factory and builds a Sink from cfg. It is the
// one-call convenience over Lookup + Factory.Open; a missing Name returns the
// WriterUnknownName sentinel so callers can HasCode it.
func Open(n Name, cfg Config) (sink corelogger.Sink, err error) {
	//: resolve the factory first so a missing import surfaces a clear sentinel.
	factory, ok := Lookup(n)
	//: absence path — the writer package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing writer.
		return nil, WriterUnknownName
	}
	//: delegate construction to the resolved factory (origin wins on error).
	return factory.Open(cfg)
}

// available is Available's body: decl_gen.go writes Available, from the
// design, as one call of it.
func available() []Name {
	//: sorted ascending, the caller's own slice.
	return registry.Names()
}
