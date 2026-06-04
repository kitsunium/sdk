// Package writer — holds the process-wide Factory registry. Service- and
// public-level writer packages register themselves via package-level var
// initialisers when imported (no init()), mirroring core/codec.
package writer

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// registry maps each writer Name to its Factory.
//
// snapshot.Value[map[Name]Factory] is the deliberate choice over sync.Map:
// writer packages register exactly ONCE at import time and every other access
// is a lock-free Load (ADR 0011) — the same read-mostly shape that justifies
// it for the codec registry. Update serialises writers on a mutex so Register's
// read-modify-write publish is race-free; Lookup stays lock-free.
var (
	registry snapshot.Value[map[Name]Factory]

	//: errDuplicateRegistration is the wrappable sentinel for boot-time
	//: duplicate-factory panics. Wrapping via %w keeps the chain inspectable
	//: while the message retains the dotted-quad code for grep-friendly logs.
	//: Lowercase per Go style guide (KTN-FUNC-ERRFMT enforces).
	errDuplicateRegistration = errors.New("duplicate registration")
)

// loadRegistry returns the current registry snapshot, or nil when no writer
// has registered yet.
func loadRegistry() map[Name]Factory {
	//: load the current snapshot pointer; nil before first Register call.
	current := registry.Load()
	//: nil snapshot means no factory registered yet — empty result.
	if current == nil {
		//: hand back nil so callers see a clean miss.
		return nil
	}
	//: dereference the snapshot for caller reads.
	return *current
}

// Register inserts f into the registry under f.Name() and returns it so callers
// can bind the singleton to a typed package-level variable like
// `var Writer = writer.Register(&fileFactory{})`. Panics on a nil factory or
// when a distinct factory already claims the same Name.
//
// IFACE-PLUGIN: the registry hands plug-in factory instances back to callers
// so each writer keeps its concrete type unexported; the stable contract is
// the Factory interface itself.
func Register(f Factory) Factory {
	//: nil registration is always a programming error.
	if f == nil {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("writer.Register [%s WRITER_NIL]: nil Factory", CodeWriterNil))
	}
	//: the canonical Name is the primary key.
	name := f.Name()
	//: Name("") is the reserved invalid zero value (writer.go); reject it at
	//: boot so it can never leak into Lookup / Open / Available.
	if name == "" {
		//: panic so the offending factory is visible at boot.
		panic(fmt.Sprintf("writer.Register [%s WRITER_NAME_EMPTY]: empty Name", CodeWriterNameEmpty))
	}
	//: publish the factory under the writer lock; duplicate Name is a hard conflict.
	if err := publishFactory(name, f); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the factory lets callers bind it to a typed singleton var.
	return f
}

// publishFactory inserts (name → f) into the registry snapshot under the writer
// lock. Returns a non-nil error when name is already registered to a different
// factory; the caller turns it into a boot-time panic.
func publishFactory(name Name, f Factory) error {
	//: dupErr escapes the Update closure to signal a conflicting registration.
	var dupErr error
	//: Update serialises writers on the snapshot mutex, so the duplicate check
	//: and the publish are atomic against any concurrent Register.
	registry.Update(func(current *map[Name]Factory) *map[Name]Factory {
		//: duplicate detection runs on the current snapshot before any allocation.
		if current != nil {
			//: an existing entry under name decides idempotent vs conflict.
			if existing, dup := (*current)[name]; dup {
				//: re-registering the SAME factory is idempotent — a no-op
				//: republish (interface == compares the factory pointers).
				if existing == f {
					//: nothing changes; keep the current snapshot.
					return current
				}
				//: a DISTINCT factory under a taken Name is the hard conflict;
				//: wrap the sentinel so errors.Is finds the chain, then abort.
				dupErr = fmt.Errorf("writer.Register [%s %w]: duplicate Name %q", CodeDuplicateRegistration, errDuplicateRegistration, name)
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone the snapshot + insert the new entry, then publish atomically.
		return new(cloneFactoryMap(current, name, f))
	})
	//: surface any conflict to Register, which panics with the doc code.
	return dupErr
}

// cloneFactoryMap copies the source snapshot and inserts (name → f). Register
// is called once per writer at package import, so this clone is init-time,
// one-shot work — not a per-request hot path.
func cloneFactoryMap(src *map[Name]Factory, name Name, f Factory) map[Name]Factory {
	//: size hint = source size + 1 for the new entry; nil source → 1.
	var size int
	//: nil source is the very-first-Register case; size stays zero.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[Name]Factory, size+1)
	//: maps.Copy handles the nil-source case implicitly (no-op).
	if src != nil {
		//: bulk-copy every existing entry.
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[name] = f
	//: caller publishes the snapshot via Value.Update.
	return next
}

// Lookup returns the factory registered under n.
//
// IFACE-PLUGIN: the registry stores plug-in factory instances behind the
// Factory interface — concrete types are intentionally unexported per writer.
func Lookup(n Name) (f Factory, ok bool) {
	//: snapshot-pointer read + map lookup; no interface assertion needed.
	m := loadRegistry()
	//: absence path.
	if m == nil {
		//: no factory registered under this Name.
		return nil, false
	}
	//: typed map read.
	factory, found := m[n]
	//: hand back the typed factory + lookup outcome.
	return factory, found
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

// Available returns the sorted list of registered Names.
func Available() []Name {
	//: snapshot the registry pointer; nil before any Register.
	m := loadRegistry()
	//: empty result when nothing registered yet.
	if m == nil {
		//: nil slice is the documented zero value.
		return nil
	}
	//: slices.Collect(maps.Keys()) avoids the manual range loop the linter's
	//: COLLECTKEYS rule flags; same allocation cost as the hand-rolled loop.
	names := slices.Collect(maps.Keys(m))
	//: slices.Sort avoids reflection compared to sort.Slice.
	slices.Sort(names)
	//: hand back the freshly ordered slice.
	return names
}
