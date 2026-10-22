package codec

import (
	"fmt"
	"mime"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// Package-level indexes.
//
// Each is the kernel's read-mostly, copy-on-write table (kernel/plugin.Registry,
// ADR 0159): codec packages register themselves exactly ONCE at package import
// time (their package-level var initializer calls Register), and every other
// access is a read (Lookup / LookupMIME / LookupExt) — one atomic pointer load
// and a map read, no lock. A frozen-after-init map read that way is ~30% faster
// than sync.Map.Load + the interface-to-Codec type assertion (microbench: 9.1 ns
// vs 12.8 ns) and avoids the dirty-map fallback machinery sync.Map carries for
// the write-mostly workloads we never trigger.
//
// The MIME and extension indexes are the codec's own, kept BESIDE the Format
// table rather than inside it (the kernel table has no second index); what is
// shared is the mechanism, and what stays here is the domain: the strict rule on
// a Format name, the alias normalisation, and the conflict's code and fields.
var (
	registry  plugin.Registry[Format, Codec]
	mimeIndex plugin.Registry[string, Format]
	extIndex  plugin.Registry[string, Format]
)

// Register inserts c into the registry and returns it so callers can bind
// the singleton to a typed package-level variable like
// `var Codec codec.Codec = codec.Register(&jsonCodec{})`. Panics on nil
// argument, on an empty Name / MIME / extension (KEY_EMPTY), or on a
// duplicate Name / MIME / extension.
//
// Register reads c.Name, c.MIMETypes and c.Extensions through the Codec
// interface, so the initialiser of the calling package variable does NOT
// depend, in the sense of the Go specification's package initialization
// rules, on any package variable those methods read: a dependency is only
// recorded for a method referenced through a non-interface type. A codec
// whose methods read package variables must therefore make that dependency
// visible in the initialiser itself — or, as the SDK's codecs do, return
// fresh literals from the methods — otherwise those variables may still be
// unset when Register runs (the gc compiler hides this by initialising
// constant composite literals statically; garble -literals does not).
//
// IFACE-PLUGIN: the registry hands plug-in codec instances back to callers
// so each domain can keep its concrete codec type unexported; the only
// stable contract is the Codec interface itself.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func Register(c Codec) Codec {
	//: a typed nil and a non-comparable plug-in both satisfy the port and
	//: neither can serve — refuse at import, where the offender is named.
	if why := plugin.Unusable(c); why != "" {
		//: panic so the offender is visible at boot. A nil Codec is a
		//: nil-argument programmer error, not a duplicate — its own dotted-quad
		//: code (CODEC_NIL) keeps operator triage honest. %s renders the
		//: canonical dotted-quad via Code.String() (matching the log-parser regex).
		panic(fmt.Sprintf("codec.Register [%s CODEC_NIL]: %s", CodeCodecNil, why))
	}
	//: the canonical Format is the primary key.
	name := Format(c.Name())
	//: read each alias table once: what is checked is what gets indexed.
	mimes, exts := c.MIMETypes(), c.Extensions()
	//: an empty key is refused before anything is published, so a refused
	//: codec leaves no half-registration behind (its Name claimed, no alias).
	refuseEmptyKeys(c, name, mimes, exts)
	//: publish the codec; any prior registration of the Name is a hard conflict.
	if err := publishCodec(name, c); err != nil {
		//: the typed conflict's dotted-quad header, then the name that collided.
		panic(conflictText(err))
	}
	//: split alias indexing into a helper so Register stays linear; otherwise
	//: the conflict-detection branches push the cyclomatic complexity past budget.
	//: MIME keys are normalised with normalizeMIME — the SAME reduction LookupMIME
	//: applies — so a registered MIME is always reachable (issue #36): a
	//: parameter-only alias (e.g. "application/x;p=1") normalises to its bare
	//: media type and would collide with the bare form rather than hide behind it.
	indexAliases(&mimeIndex, mimes, name, "MIME", normalizeMIME)
	//: extensions share the same shape but only need case-folding.
	indexAliases(&extIndex, exts, name, "extension", strings.ToLower)
	//: returning the codec lets callers bind it to a typed singleton var.
	return c
}

// publishCodec claims name for c. Returns a non-nil error when name is already
// registered — to ANY codec, the same one included: a Format registers once,
// whereas the alias indexes and the other core registries accept the identical
// value again as a no-op. The caller turns the error into a panic so the
// boot-time failure is loud and grep-friendly. The check and the insert are one
// atomic step.
func publishCodec(name Format, c Codec) error {
	//: Claim reports a taken name whoever holds it — the strict rule needs
	//: nothing more than taken.
	if _, taken := registry.Claim(name, c); !taken {
		//: the Name was free and is now c's.
		return nil
	}
	//: the typed sentinel is the origin; the fields say who refused what,
	//: since Error() renders none of them (rule 4).
	return errs.Wrap(DuplicateRegistration, errs.WrapParams{},
		errs.String("registrar", "codec.Register"), errs.String("name", string(name)))
}

// refuseEmptyKeys panics with KEY_EMPTY when c's Name, or one of its MIME
// types or extensions once normalised the way the matching Lookup* normalises
// it, is empty. An empty key is no key at all — LookupMIME and LookupExt miss
// on "" — and it is exactly what a package variable holds when Register reads
// it before its initialiser ran: two codecs in that state both hold "", and
// the second used to die on a DUPLICATE_REGISTRATION naming the FIRST, a
// collision that blamed the wrong codec and hid the cause (supervizio/agent#305).
func refuseEmptyKeys(c Codec, name Format, mimes, exts []string) {
	//: the Name first: without one, nothing else can be attributed.
	if name == "" {
		//: same shape as CODEC_NIL: the code, the reason, the offender's type.
		panic(fmt.Sprintf("codec.Register [%s KEY_EMPTY]: %T declares an empty Name", CodeKeyEmpty, c))
	}
	//: every MIME type, reduced as LookupMIME reduces it.
	refuseEmptyAliases(mimes, name, "MIME type", normalizeMIME)
	//: every extension, reduced as LookupExt reduces it.
	refuseEmptyAliases(exts, name, "extension", strings.ToLower)
}

// refuseEmptyAliases panics with KEY_EMPTY on the first alias of kind that
// normalize reduces to "", naming the codec, the slot and the raw value.
func refuseEmptyAliases(aliases []string, name Format, kind string, normalize func(string) string) {
	//: walk the table in declaration order so the reported slot is the first bad one.
	for i, alias := range aliases {
		//: the key the matching Lookup* would compute for this alias.
		if normalize(alias) == "" {
			//: the code, the reason, then which codec and which slot.
			panic(fmt.Sprintf("codec.Register [%s KEY_EMPTY]: codec %q declares an empty %s at index %d (%q)", CodeKeyEmpty, name, kind, i, alias))
		}
	}
}

// indexAliases stores every alias (MIME or extension) in dst, panicking
// when a distinct codec already claims the same key. normalize reduces each
// raw alias to its index key — strings.ToLower for extensions, normalizeMIME
// for MIME types so registration and LookupMIME agree on the key (issue #36).
// Empty keys never reach it: refuseEmptyKeys ran first.
func indexAliases(dst *plugin.Registry[string, Format], aliases []string, name Format, kind string, normalize func(string) string) {
	//: iterate over every alias and publish it atomically.
	for _, alias := range aliases {
		//: reduce to the canonical index key the matching Lookup* will compute.
		key := normalize(alias)
		//: publishAlias handles the conflict + atomic publish semantics.
		if err := publishAlias(dst, key, name, kind, alias); err != nil {
			//: distinct codec conflict — loud failure at boot, naming the alias,
			//: the codec holding it and the codec asking.
			panic(conflictText(err))
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

// publishAlias inserts (key → name) into an alias index. Returns a non-nil
// error when key is already claimed by a different Format; idempotent
// re-registration by the same Format is accepted. The check, the insert and
// the owner the error names are one atomic step.
func publishAlias(dst *plugin.Registry[string, Format], key string, name Format, kind, alias string) error {
	//: Claim reports the Format holding a taken key, read by the same step.
	owner, taken := dst.Claim(key, name)
	//: a free key, or one that already points at us: nothing to refuse.
	if !taken || owner == name {
		//: published, or the idempotent same-codec re-registration.
		return nil
	}
	//: distinct-codec conflict — the typed sentinel, carrying the alias, the
	//: codec holding it and the codec asking.
	return errs.Wrap(DuplicateRegistration, errs.WrapParams{},
		errs.String("registrar", "codec.Register"), errs.String("kind", kind),
		errs.String("alias", alias), errs.String("owner", string(owner)),
		errs.String("requester", string(name)))
}

// Lookup returns the codec registered under f.
//
// IFACE-PLUGIN: the registry stores plug-in codec instances behind the Codec
// interface — concrete types are intentionally unexported per-format.
func Lookup(f Format) (c Codec, ok bool) {
	//: a lock-free snapshot read; a miss hands back nil AND false.
	return registry.Lookup(f)
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
	name, found := mimeIndex.Lookup(normalizeMIME(raw))
	//: absence path.
	if !found {
		//: MIME unrecognised, or nothing registered yet.
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
	name, found := extIndex.Lookup(strings.ToLower(ext))
	//: absence path.
	if !found {
		//: extension unrecognised, or nothing registered yet.
		return nil, false
	}
	//: delegate to Lookup so the typed Codec value comes from the same path.
	return Lookup(name)
}

// Available returns the sorted list of registered Formats, or nil before any
// Register.
func Available() []Format {
	//: sorted ascending, the caller's own slice.
	return registry.Names()
}
