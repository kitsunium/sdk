// Package codec: registry.go holds the process-wide Codec registry.
// Service-level codec packages register themselves via package-level var
// initialisers when the package is imported.
package codec

import (
	"fmt"
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

// Register inserts c into the registry. Panics on nil or duplicate Name.
//
// Params:
//   - c: a fully constructed Codec instance.
//
// Returns:
//   - bool: always true; the return value lets callers use Register in a
//     package-level var initialiser (e.g. `var _ = Register(New())`).
func Register(c Codec) (ok bool) {
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
	//: index every declared MIME type against the canonical Format.
	for _, mime := range c.MIMETypes() {
		//: normalise to lowercase so MIME lookups are case-insensitive.
		mimeIndex.Store(strings.ToLower(mime), name)
	}
	//: index every declared extension against the canonical Format.
	for _, ext := range c.Extensions() {
		//: normalise to lowercase so extension lookups are case-insensitive.
		extIndex.Store(strings.ToLower(ext), name)
	}
	//: conventional true return lets Register sit in a var initialiser.
	return true
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

// LookupMIME returns the codec whose MIME list matches.
//
// Params:
//   - mime: the MIME type (case-insensitive).
//
// Returns:
//   - Codec: the codec registered for the MIME, or nil if none.
//   - bool: true iff the MIME resolves to a codec.
func LookupMIME(mime string) (c Codec, ok bool) {
	//: empty MIME always misses.
	if mime == "" {
		//: caller supplied nothing to match.
		return nil, false
	}
	//: normalise and look up the index.
	raw, found := mimeIndex.Load(strings.ToLower(mime))
	//: absence path.
	if !found {
		//: MIME unrecognised.
		return nil, false
	}
	//: resolve the Format to the concrete codec.
	name, _ := raw.(Format)
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
