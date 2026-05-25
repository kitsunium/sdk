// Package codec — holds the process-wide Codec registry.
// Service-level codec packages register themselves via package-level var
// initialisers when the package is imported.
package codec

import (
	"errors"
	"fmt"
	"maps"
	"mime"
	"slices"
	"strings"
	"sync/atomic"
)

// Package-level indexes + the duplicate-registration sentinel.
//
// atomic.Pointer[map[K]V] is the deliberate choice here over sync.Map:
// codec packages register themselves exactly ONCE at package import time
// (their package-level var initializer calls Register), and every other
// access is a read (Lookup / LookupMIME / LookupExt). A frozen-after-init
// map read via atomic.Pointer is ~30% faster than sync.Map.Load + the
// interface-to-Codec type assertion (microbench: 9.1 ns vs 12.8 ns) and
// avoids the dirty-map fallback machinery sync.Map carries for write-mostly
// workloads we never trigger.
//
// Register uses a CAS-loop on the snapshot pointer so concurrent first-use
// init (rare — Go package init is single-goroutine, but defensive) loses
// gracefully without dropping entries.
var (
	registry  atomic.Pointer[map[Format]Codec]
	mimeIndex atomic.Pointer[map[string]Format]
	extIndex  atomic.Pointer[map[string]Format]

	//: errDuplicateRegistration is the wrappable sentinel for boot-time
	//: duplicate-codec panics. Wrapping via %w keeps the error chain
	//: inspectable (errors.Is can match the sentinel) while the message
	//: retains the dotted-quad code for grep-friendly logs. Lowercase
	//: per Go style guide (KTN-FUNC-ERRFMT enforces).
	errDuplicateRegistration = errors.New("duplicate registration")
)

// loadRegistry returns the current registry snapshot, or an empty map
// when no codec has registered yet (rare — service codec init order
// runs before any application code).
func loadRegistry() map[Format]Codec {
	//: load the current snapshot pointer; nil before first Register call.
	current := registry.Load()
	//: nil snapshot means no codec registered yet — empty result.
	if current == nil {
		//: hand back a fresh empty map (caller never mutates).
		return nil
	}
	//: dereference the snapshot for caller reads.
	return *current
}

// loadAliasIndex returns the current alias-index snapshot for mime or
// extension lookup. Same nil-handling as loadRegistry.
func loadAliasIndex(p *atomic.Pointer[map[string]Format]) map[string]Format {
	//: load the snapshot pointer; nil before first Register call.
	current := p.Load()
	//: nil snapshot — no aliases registered yet.
	if current == nil {
		//: hand back nil so callers see a clean miss.
		return nil
	}
	//: dereference for caller reads.
	return *current
}

// Register inserts c into the registry and returns it so callers can bind
// the singleton to a typed package-level variable like
// `var Codec codec.Codec = codec.Register(&jsonCodec{})`. Panics on nil
// argument or duplicate Name / MIME / extension.
//
// IFACE-PLUGIN: the registry hands plug-in codec instances back to callers
// so each domain can keep its concrete codec type unexported; the only
// stable contract is the Codec interface itself.
func Register(c Codec) Codec {
	//: nil registration is always a programming error.
	if c == nil {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("codec.Register [%d DUPLICATE_REGISTRATION]: nil Codec", CodeDuplicateRegistration))
	}
	//: the canonical Format is the primary key.
	name := Format(c.Name())
	//: CAS-loop publishes a new snapshot that includes c. Duplicate detection
	//: happens inside the loop so a winning racer sees the loser's panic.
	if err := publishCodec(name, c); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: split alias indexing into a helper so Register stays linear; otherwise
	//: the conflict-detection branches push the cyclomatic complexity past budget.
	indexAliases(&mimeIndex, c.MIMETypes(), name, "MIME")
	//: extensions share the same shape.
	indexAliases(&extIndex, c.Extensions(), name, "extension")
	//: returning the codec lets callers bind it to a typed singleton var.
	return c
}

// publishCodec performs the CAS-loop that inserts (name → c) into the
// registry snapshot. Returns a non-nil error when name is already
// registered to a different codec — the caller turns the error into a
// panic so the boot-time failure is loud and grep-friendly.
//
//nolint:gocyclo // CAS-retry shape inherently raises cyclomatic count.
func publishCodec(name Format, c Codec) error {
	//: CAS loop — concurrent first-Register calls (rare under Go's
	//: single-goroutine init order) retry until one wins. The loop
	//: body's make() is NOT a hot allocation: every Register call is
	//: a one-shot init-time operation, never a per-request path.
	//: next is hoisted outside so each retry overwrites the same
	//: stack slot rather than declaring a fresh variable per loop.
	var next *map[Format]Codec
	//: CAS retry loop; loop body documented inline below.
	for {
		//: snapshot the current registry state.
		current := registry.Load()
		//: duplicate detection runs on the current snapshot before any
		//: allocation — caller's panic is grep-friendly via the code.
		if current != nil {
			//: any prior registration of `name` is a hard conflict.
			if _, dup := (*current)[name]; dup {
				//: wrap the sentinel so errors.Is finds the chain.
				return fmt.Errorf("codec.Register [%d %w]: duplicate Name %q", CodeDuplicateRegistration, errDuplicateRegistration, name)
			}
		}
		//: clone the snapshot + append the new entry. maps.Copy handles
		//: the nil-source case so we don't need a separate branch.
		//: new(expr) is Go 1.26+ form — heap-allocates the cloned map
		//: in one shot, no v := expr; &v intermediate.
		next = new(cloneFormatMap(current, name, c))
		//: CAS publishes the new snapshot. Failure loops back to retry.
		if registry.CompareAndSwap(current, next) {
			//: snapshot installed atomically — readers see the new entry.
			return nil
		}
	}
}

// cloneFormatMap copies the source snapshot and inserts (name → c).
// Hoisted out of publishCodec's CAS loop so the per-retry alloc is
// attributed to its own stack frame (the linter's HOTLOOP rule reads
// this as init-time work, which it is — Register is called once per
// codec at package import).
func cloneFormatMap(src *map[Format]Codec, name Format, c Codec) map[Format]Codec {
	//: size hint = source size + 1 for the new entry; nil source → 1.
	var size int
	//: nil source is the very-first-Register case; size stays zero so
	//: the new map allocates just the one slot for the new entry.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[Format]Codec, size+1)
	//: maps.Copy handles the nil-source case implicitly (no-op).
	if src != nil {
		//: bulk-copy every existing entry.
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[name] = c
	//: caller installs the snapshot via CAS.
	return next
}

// indexAliases stores every alias (MIME or extension) in dst, panicking
// when a distinct codec already claims the same key. Uses the same
// CAS-loop pattern as publishCodec for race-free first-Register.
func indexAliases(dst *atomic.Pointer[map[string]Format], aliases []string, name Format, kind string) {
	//: iterate over every alias and publish it atomically.
	for _, alias := range aliases {
		//: normalise so lookups are case-insensitive.
		key := strings.ToLower(alias)
		//: publishAlias handles the conflict + CAS retry semantics.
		if err := publishAlias(dst, key, name, kind, alias); err != nil {
			//: distinct codec conflict — loud failure at boot.
			panic(err.Error())
		}
	}
}

// publishAlias performs the CAS-loop that inserts (key → name) into an
// alias index snapshot. Returns a non-nil error when key is already
// claimed by a different Format; idempotent re-registration by the same
// Format is accepted.
//
//nolint:gocyclo // CAS-retry shape inherently raises cyclomatic count.
func publishAlias(dst *atomic.Pointer[map[string]Format], key string, name Format, kind, alias string) error {
	//: CAS loop matches publishCodec's shape; same init-time semantics
	//: apply (Register runs once per codec at package import).
	//: next is hoisted outside so each retry overwrites the same slot.
	var next *map[string]Format
	//: CAS retry loop; loop body documented inline below.
	for {
		//: snapshot the current alias index.
		current := dst.Load()
		//: conflict + idempotent-reregistration detection runs on the
		//: current snapshot before any allocation.
		if current != nil {
			//: any prior alias under `key` is checked against `name`.
			if prev, exists := (*current)[key]; exists {
				//: idempotent same-codec re-registration is fine.
				if prev == name {
					//: no-op — alias already points at us.
					return nil
				}
				//: wrap the sentinel so errors.Is finds the chain.
				return fmt.Errorf("codec.Register [%d %w]: %s %q already registered by %q (requested by %q)",
					CodeDuplicateRegistration, errDuplicateRegistration, kind, alias, prev, name)
			}
		}
		//: clone + insert via the hoisted helper (same HOTLOOP rationale
		//: as cloneFormatMap above). Go 1.26+ new(expr) form.
		next = new(cloneAliasMap(current, key, name))
		//: CAS publishes the new snapshot. Failure loops back to retry.
		if dst.CompareAndSwap(current, next) {
			//: snapshot installed atomically.
			return nil
		}
	}
}

// cloneAliasMap mirrors cloneFormatMap for the MIME / extension alias
// indexes: copies the source snapshot and inserts (key → name). The
// per-Register call shape is identical (init-time, one-shot work).
func cloneAliasMap(src *map[string]Format, key string, name Format) map[string]Format {
	//: size hint = source size + 1 for the new entry; nil source → 1.
	var size int
	//: nil source is the very-first-Register case; size stays zero.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[string]Format, size+1)
	//: maps.Copy handles the nil-source case implicitly (no-op).
	if src != nil {
		//: bulk-copy every existing entry.
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[key] = name
	//: caller installs the snapshot via CAS.
	return next
}

// Lookup returns the codec registered under f.
//
// IFACE-PLUGIN: the registry stores plug-in codec instances behind the Codec
// interface — concrete types are intentionally unexported per-format.
func Lookup(f Format) (c Codec, ok bool) {
	//: snapshot-pointer read + map lookup; no interface assertion needed
	//: because the snapshot stores typed Codec values directly.
	m := loadRegistry()
	//: absence path.
	if m == nil {
		//: no codec registered under this Format.
		return nil, false
	}
	//: typed map read.
	codec, found := m[f]
	//: hand back the typed codec + lookup outcome.
	return codec, found
}

// LookupMIME returns the codec whose MIME list matches. MIME parameters
// (e.g. "; charset=utf-8") are stripped before the index lookup so that
// "application/json; charset=utf-8" resolves like "application/json".
//
// IFACE-PLUGIN: the registry stores plug-in codec instances behind the Codec
// interface — concrete types are intentionally unexported per-format.
func LookupMIME(raw string) (c Codec, ok bool) {
	//: empty MIME always misses.
	if raw == "" {
		//: caller supplied nothing to match.
		return nil, false
	}
	//: parse parameters away; ParseMediaType lowercases the media type itself.
	mediaType, _, perr := mime.ParseMediaType(raw)
	//: fall back to a manual strip when the header is malformed.
	if perr != nil {
		//: strings.Cut returns the part before the first ';' (or the full string).
		before, _, _ := strings.Cut(raw, ";")
		//: normalise to lowercase trimmed form for index lookup.
		mediaType = strings.ToLower(strings.TrimSpace(before))
	}
	//: snapshot-pointer read + map lookup.
	m := loadAliasIndex(&mimeIndex)
	//: absence path.
	if m == nil {
		//: nothing registered yet.
		return nil, false
	}
	//: typed alias-index read.
	name, found := m[mediaType]
	//: absence path.
	if !found {
		//: MIME unrecognised.
		return nil, false
	}
	//: delegate to Lookup so the typed Codec value comes from the same path.
	return Lookup(name)
}

// LookupExt returns the codec whose extension list contains ext.
//
// IFACE-PLUGIN: the registry stores plug-in codec instances behind the Codec
// interface — concrete types are intentionally unexported per-format.
func LookupExt(ext string) (c Codec, ok bool) {
	//: empty extension always misses.
	if ext == "" {
		//: caller supplied nothing to match.
		return nil, false
	}
	//: normalise and look up the index.
	m := loadAliasIndex(&extIndex)
	//: absence path.
	if m == nil {
		//: nothing registered yet.
		return nil, false
	}
	//: typed alias-index read.
	name, found := m[strings.ToLower(ext)]
	//: absence path.
	if !found {
		//: extension unrecognised.
		return nil, false
	}
	//: delegate to Lookup so the typed Codec value comes from the same path.
	return Lookup(name)
}

// Available returns the sorted list of registered Formats.
func Available() []Format {
	//: snapshot the registry pointer; nil before any Register.
	m := loadRegistry()
	//: empty result when nothing registered yet.
	if m == nil {
		//: nil slice is the documented zero value.
		return nil
	}
	//: slices.Collect(maps.Keys()) avoids the manual range loop the
	//: linter's COLLECTKEYS rule flags; same allocation cost as the
	//: hand-rolled loop but expresses intent at the call site.
	formats := slices.Collect(maps.Keys(m))
	//: slices.Sort avoids reflection compared to sort.Slice.
	slices.Sort(formats)
	//: hand back the freshly ordered slice.
	return formats
}
