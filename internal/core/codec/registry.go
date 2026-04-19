// Package codec: registry.go holds the process-wide Codec registry.
// Service-level codec packages register themselves via package-level var
// initialisers when the package is imported.
package codec

import (
	"fmt"
	"mime"
	"slices"
	"strings"
	"sync"
)

// Package-level indexes — grouped so KTN-VAR-GROUP stays quiet.
var (
	registry  sync.Map
	mimeIndex sync.Map
	extIndex  sync.Map
)

// Register inserts c into the registry and returns it so callers can bind
// the singleton to a typed package-level variable like
// `var Codec codec.Codec = codec.Register(&jsonCodec{})`. Panics on nil
// argument or duplicate Name / MIME / extension.
//
// Params:
//   - c: a fully constructed Codec instance.
//
// Returns:
//   - registered: the same Codec instance, ready to be bound to a var.
func Register(c Codec) (registered Codec) {
	//: nil registration is always a programming error.
	if c == nil {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("codec.Register [%d DUPLICATE_REGISTRATION]: nil Codec", CodeDuplicateRegistration))
	}
	//: the canonical Format is the primary key.
	name := Format(c.Name())
	//: duplicate detection keeps the first-registered codec deterministic.
	if _, loaded := registry.LoadOrStore(name, c); loaded {
		//: surface the doc code for grep-friendly panic messages.
		panic(fmt.Sprintf("codec.Register [%d DUPLICATE_REGISTRATION]: duplicate Name %q", CodeDuplicateRegistration, name))
	}
	//: delegate MIME + extension indexing to helpers to keep Register below
	//: the KTN-FUNC-CYCLO ceiling (8) — the conflict-detection branches would
	//: otherwise push Register's cyclomatic complexity to 9.
	indexAliases(&mimeIndex, c.MIMETypes(), name, "MIME")
	//: extensions share the same shape.
	indexAliases(&extIndex, c.Extensions(), name, "extension")
	//: returning the codec lets callers bind it to a typed singleton var.
	return c
}

// indexAliases stores every alias (MIME or extension) in dst, panicking
// when a distinct codec already claims the same key.
//
// Params:
//   - dst: the sync.Map to index into (mimeIndex or extIndex).
//   - aliases: the alias list returned by the codec.
//   - name: canonical Format of the codec being registered.
//   - kind: human-readable category for the panic message ("MIME" / "extension").
//
// Returns: nothing; panics on conflict.
func indexAliases(dst *sync.Map, aliases []string, name Format, kind string) {
	//: iterate over every alias and publish it atomically.
	for _, alias := range aliases {
		//: normalise so lookups are case-insensitive.
		key := strings.ToLower(alias)
		//: LoadOrStore is atomic — returns the previous value on a hit.
		prev, loaded := dst.LoadOrStore(key, name)
		//: absence path — brand new alias.
		if !loaded {
			//: move on to the next alias.
			continue
		}
		//: idempotent re-registration by the same codec is accepted.
		if prev == name {
			//: nothing to do.
			continue
		}
		//: distinct codec conflict — loud failure at boot.
		panic(fmt.Sprintf("codec.Register [%d DUPLICATE_REGISTRATION]: %s %q already registered by %q (requested by %q)",
			CodeDuplicateRegistration, kind, alias, prev, name))
	}
}

// Lookup returns the codec registered under f.
//
// Params:
//   - f: the Format identifier.
//
// Returns:
//   - Codec: the registered codec, or nil if none.
//   - bool: true iff f is registered.
func Lookup(f Format) (c Codec, ok bool) {
	//: direct map read.
	raw, found := registry.Load(f)
	//: absence path.
	if !found {
		//: no codec under this Format.
		return nil, false
	}
	//: map values are Codec by construction.
	codec, _ := raw.(Codec)
	//: hand back the typed codec.
	return codec, true
}

// LookupMIME returns the codec whose MIME list matches. MIME parameters
// (e.g. "; charset=utf-8") are stripped before the index lookup so that
// "application/json; charset=utf-8" resolves like "application/json".
//
// Params:
//   - raw: the MIME header value (case-insensitive).
//
// Returns:
//   - c: the codec registered for the MIME, or nil if none.
//   - ok: true iff the MIME resolves to a codec.
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
	//: index keys are already lowercased.
	entry, found := mimeIndex.Load(mediaType)
	//: absence path.
	if !found {
		//: MIME unrecognised.
		return nil, false
	}
	//: resolve the Format to the concrete codec.
	name, _ := entry.(Format)
	//: delegate to Lookup.
	return Lookup(name)
}

// LookupExt returns the codec whose extension list contains ext.
//
// Params:
//   - ext: the file extension with its leading dot.
//
// Returns:
//   - Codec: the codec registered for the extension, or nil if none.
//   - bool: true iff the extension resolves to a codec.
func LookupExt(ext string) (c Codec, ok bool) {
	//: empty extension always misses.
	if ext == "" {
		//: caller supplied nothing to match.
		return nil, false
	}
	//: normalise and look up the index.
	raw, found := extIndex.Load(strings.ToLower(ext))
	//: absence path.
	if !found {
		//: extension unrecognised.
		return nil, false
	}
	//: resolve the Format to the concrete codec.
	name, _ := raw.(Format)
	//: delegate to Lookup.
	return Lookup(name)
}

// Available returns the sorted list of registered Formats.
//
// Returns:
//   - []Format: sorted ascending; empty when no codec is registered.
func Available() (formats []Format) {
	//: collect every key via sync.Map.Range.
	registry.Range(func(key, _ any) (keepGoing bool) {
		//: keys are always Format by construction.
		name, _ := key.(Format)
		formats = append(formats, name)
		//: continue iterating.
		return true
	})
	//: slices.Sort avoids reflection compared to sort.Slice.
	slices.Sort(formats)
	//: hand back the freshly ordered slice.
	return formats
}
