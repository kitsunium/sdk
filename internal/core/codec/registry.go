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

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// Package-level indexes + the duplicate-registration sentinel.
//
// snapshot.Value[map[K]V] is the deliberate choice here over sync.Map:
// codec packages register themselves exactly ONCE at package import time
// (their package-level var initializer calls Register), and every other
// access is a read (Lookup / LookupMIME / LookupExt). A frozen-after-init
// map read via Value.Load (one atomic.Pointer load) is ~30% faster than
// sync.Map.Load + the interface-to-Codec type assertion (microbench: 9.1 ns
// vs 12.8 ns) and avoids the dirty-map fallback machinery sync.Map carries
// for the write-mostly workloads we never trigger.
//
// snapshot.Value (ADR 0011) serialises writers on a mutex, so Register's
// read-modify-write publish is race-free WITHOUT the hand-rolled CAS loop this
// package carried before; readers stay lock-free via Value.Load. The
// copy-on-write mechanism lives in internal/kernel/snapshot — only the domain
// clone logic (cloneFormatMap / cloneAliasMap) stays here.
var (
	registry  snapshot.Value[map[Format]Codec]
	mimeIndex snapshot.Value[map[string]Format]
	extIndex  snapshot.Value[map[string]Format]

	//: errDuplicateRegistration is the wrappable sentinel for boot-time
	//: duplicate-codec panics. Wrapping via %w keeps the error chain
	//: inspectable (errors.Is can match the sentinel) while the message
	//: retains the dotted-quad code for grep-friendly logs. Lowercase
	//: per Go style guide (KTN-FUNC-ERRFMT enforces).
	errDuplicateRegistration = errors.New("duplicate registration")
)

// loadRegistry returns the current registry snapshot, or nil when no codec
// has registered yet (rare — service codec init order runs before any
// application code).
func loadRegistry() map[Format]Codec {
	//: load the current snapshot pointer; nil before first Register call.
	current := registry.Load()
	//: nil snapshot means no codec registered yet — empty result.
	if current == nil {
		//: hand back nil so callers see a clean miss.
		return nil
	}
	//: dereference the snapshot for caller reads.
	return *current
}

// loadAliasIndex returns the current alias-index snapshot for mime or
// extension lookup. Same nil-handling as loadRegistry.
func loadAliasIndex(p *snapshot.Value[map[string]Format]) map[string]Format {
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
		//: panic so the offender is visible at boot. A nil Codec is a
		//: nil-argument programmer error, not a duplicate — its own dotted-quad
		//: code (CODEC_NIL) keeps operator triage honest. %s renders the
		//: canonical dotted-quad via Code.String() (matching the log-parser regex).
		panic(fmt.Sprintf("codec.Register [%s CODEC_NIL]: nil Codec", CodeCodecNil))
	}
	//: the canonical Format is the primary key.
	name := Format(c.Name())
	//: publish the codec under the writer lock; duplicate Name is a hard conflict.
	if err := publishCodec(name, c); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: split alias indexing into a helper so Register stays linear; otherwise
	//: the conflict-detection branches push the cyclomatic complexity past budget.
	//: MIME keys are normalised with normalizeMIME — the SAME reduction LookupMIME
	//: applies — so a registered MIME is always reachable (issue #36): a
	//: parameter-only alias (e.g. "application/x;p=1") normalises to its bare
	//: media type and would collide with the bare form rather than hide behind it.
	indexAliases(&mimeIndex, c.MIMETypes(), name, "MIME", normalizeMIME)
	//: extensions share the same shape but only need case-folding.
	indexAliases(&extIndex, c.Extensions(), name, "extension", strings.ToLower)
	//: returning the codec lets callers bind it to a typed singleton var.
	return c
}

// publishCodec inserts (name → c) into the registry snapshot under the writer
// lock. Returns a non-nil error when name is already registered to a different
// codec — the caller turns the error into a panic so the boot-time failure is
// loud and grep-friendly.
func publishCodec(name Format, c Codec) error {
	//: dupErr escapes the Update closure to signal a conflicting registration.
	var dupErr error
	//: Update serialises writers on the snapshot mutex, so the duplicate check
	//: and the publish are atomic against any concurrent Register.
	registry.Update(func(current *map[Format]Codec) *map[Format]Codec {
		//: duplicate detection runs on the current snapshot before any allocation.
		if current != nil {
			//: any prior registration of name is a hard conflict.
			if _, dup := (*current)[name]; dup {
				//: wrap the sentinel so errors.Is finds the chain, then abort.
				dupErr = fmt.Errorf("codec.Register [%s %w]: duplicate Name %q", CodeDuplicateRegistration, errDuplicateRegistration, name)
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone the snapshot + insert the new entry, then publish atomically.
		//: new(expr) is Go 1.26+ form — heap-allocates the cloned map in one shot.
		return new(cloneFormatMap(current, name, c))
	})
	//: surface any conflict to Register, which panics with the doc code.
	return dupErr
}

// cloneFormatMap copies the source snapshot and inserts (name → c). Register
// is called once per codec at package import, so this clone is init-time,
// one-shot work — not a per-request hot path.
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
	//: caller publishes the snapshot via Value.Update.
	return next
}

// indexAliases stores every alias (MIME or extension) in dst, panicking
// when a distinct codec already claims the same key. normalize reduces each
// raw alias to its index key — strings.ToLower for extensions, normalizeMIME
// for MIME types so registration and LookupMIME agree on the key (issue #36).
func indexAliases(dst *snapshot.Value[map[string]Format], aliases []string, name Format, kind string, normalize func(string) string) {
	//: iterate over every alias and publish it atomically.
	for _, alias := range aliases {
		//: reduce to the canonical index key the matching Lookup* will compute.
		key := normalize(alias)
		//: publishAlias handles the conflict + atomic publish semantics.
		if err := publishAlias(dst, key, name, kind, alias); err != nil {
			//: distinct codec conflict — loud failure at boot.
			panic(err.Error())
		}
	}
}

// normalizeMIME reduces a raw MIME string to the canonical index key shared by
// registration and LookupMIME: the media type alone (parameters stripped),
// lowercased and trimmed. Sharing this between the two paths is what guarantees
// a registered MIME is reachable — the asymmetry it removes was issue #36.
func normalizeMIME(raw string) string {
	//: parse parameters away; ParseMediaType lowercases the media type itself.
	mediaType, _, perr := mime.ParseMediaType(raw)
	//: fall back to a manual strip when the header is malformed.
	if perr != nil {
		//: strings.Cut returns the part before the first ';' (or the full string).
		before, _, _ := strings.Cut(raw, ";")
		//: normalise to lowercase trimmed form for index parity.
		mediaType = strings.ToLower(strings.TrimSpace(before))
	}
	//: the canonical key both Register and LookupMIME index on.
	return mediaType
}

// publishAlias inserts (key → name) into an alias index snapshot under the
// writer lock. Returns a non-nil error when key is already claimed by a
// different Format; idempotent re-registration by the same Format is accepted.
func publishAlias(dst *snapshot.Value[map[string]Format], key string, name Format, kind, alias string) error {
	//: conflictErr escapes the Update closure to signal a clashing alias.
	var conflictErr error
	//: Update serialises writers; the conflict check and publish are atomic.
	dst.Update(func(current *map[string]Format) *map[string]Format {
		//: conflict + idempotent-reregistration detection on the current snapshot.
		if current != nil {
			//: any prior alias under key is checked against name.
			if prev, exists := (*current)[key]; exists {
				//: idempotent same-codec re-registration is fine.
				if prev == name {
					//: no-op publish — alias already points at us.
					return current
				}
				//: distinct-codec conflict — wrap the sentinel, then abort.
				conflictErr = fmt.Errorf("codec.Register [%s %w]: %s %q already registered by %q (requested by %q)",
					CodeDuplicateRegistration, errDuplicateRegistration, kind, alias, prev, name)
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone + insert via the hoisted helper, then publish atomically.
		//: Go 1.26+ new(expr) form.
		return new(cloneAliasMap(current, key, name))
	})
	//: surface any conflict to indexAliases, which panics with the doc code.
	return conflictErr
}

// cloneAliasMap mirrors cloneFormatMap for the MIME / extension alias indexes:
// copies the source snapshot and inserts (key → name). Same init-time,
// one-shot call shape (once per codec at package import).
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
	//: caller publishes the snapshot via Value.Update.
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
	//: reduce to the canonical key via the SAME helper registration indexes on,
	//: so any registered MIME (parameters and case notwithstanding) resolves.
	mediaType := normalizeMIME(raw)
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
