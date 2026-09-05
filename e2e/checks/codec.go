// Package checks holds the per-domain conformance suites for the SDK e2e binary.
// Each exported Suite function exercises one domain's public pkg/v1 API on the
// host and returns the checks main runs.
package checks

import (
	"fmt"
	"reflect"

	// codec is imported by name; importing the package also activates the full
	// codec registry (all 18 Format names) via its own init side-effects.
	codec "github.com/kitsunium/sdk/pkg/v1/codec"

	"github.com/kitsunium/sdk/e2e/harness"
)

// codecDomain labels every codec conformance Result.
const codecDomain string = "codec"

// codecMinFormats is the minimum number of Formats a populated registry exposes.
const codecMinFormats int = 12

// codecStructAge is the int field value carried by the codecStruct sample; named
// so the table literal stays free of a bare magic number.
const codecStructAge int = 36

// codecStruct is the tagged value the shape-constrained formats (xml, asn1-der)
// round-trip — those codecs reject an arbitrary map and need a concrete struct.
type codecStruct struct {
	// Name is a string field carried across the wire.
	Name string `xml:"name" json:"name"`
	// Age is an int field carried across the wire.
	Age int `xml:"age" json:"age"`
}

var (
	// codecStringMap is the structured value the map-shaped formats round-trip.
	// String-only entries avoid the numeric-type drift (int vs float64) that
	// otherwise makes a cross-codec DeepEqual brittle, so the same value
	// survives every text and binary codec identically.
	codecStringMap = map[string]any{
		"name": "Ada",
		"city": "London",
		"role": "engineer",
	}

	// codecScalar is the small value the base-N family round-trips — a
	// single-entry map proves the JSON-mediated encode/decode wrap is reversible.
	codecScalar = map[string]any{"v": "hello-base-n"}

	// codecSamples is the table of one round-trip Check per registered Format.
	// Each row pairs a Format with the representative value to round-trip; the
	// decode target is rebuilt from the value's dynamic type via reflection, so
	// no per-shape constructor is needed.
	codecSamples = []struct {
		// format is the registered codec.Format under test.
		format codec.Format
		// value is the representative Go value to Marshal then Unmarshal back.
		value any
	}{
		//: structured text + binary formats round-trip a string-keyed map.
		{codec.JSON, codecStringMap},
		{codec.YAML, codecStringMap},
		{codec.TOML, codecStringMap},
		{codec.NDJSON, codecStringMap},
		{codec.CBOR, codecStringMap},
		{codec.MsgPack, codecStringMap},
		{codec.TLV, codecStringMap},
		{codec.CSV, codecStringMap},
		{codec.PEM, codecStringMap},
		{codec.FlatBuffers, codecStringMap},
		//: shape-constrained formats need a concrete tagged struct.
		{codec.XML, codecStruct{Name: "Ada", Age: codecStructAge}},
		{codec.ASN1DER, codecStruct{Name: "Ada", Age: codecStructAge}},
		//: base-N text-safe wraps round-trip a single scalar map.
		{codec.Base64, codecScalar},
		{codec.Base64URL, codecScalar},
		{codec.Base32, codecScalar},
		{codec.Base16, codecScalar},
		{codec.Hex, codecScalar},
		{codec.ASCII85, codecScalar},
	}
)

// Codec returns the codec-domain conformance checks (Marshal/Unmarshal round-trip
// across every registered Format).
func Codec() harness.CheckGroup {
	//: one round-trip Check per Format plus a registry-presence Check.
	checks := make([]harness.Check, 0, len(codecSamples)+1)
	//: bind each table row into its own Check closure.
	for _, sample := range codecSamples {
		//: copy the loop vars so each closure captures its own row.
		format, value := sample.format, sample.value
		//: append the round-trip Check for this Format.
		checks = append(checks, func() harness.Result {
			//: exercise Marshal→Unmarshal and assert structural equality.
			return codecRoundTrip(format, value)
		})
	}
	//: assert the registry actually activated via the package import.
	checks = append(checks, codecRegistryPopulated)
	//: bundle every round-trip plus the registry check under the codec domain.
	return harness.CheckGroup{Domain: codecDomain, Checks: checks}
}

// codecRoundTrip marshals value under format, unmarshals it back into a fresh
// target of the same type, and asserts the decoded value deep-equals the original.
func codecRoundTrip(format codec.Format, value any) harness.Result {
	//: the check name carries the Format so the table row is identifiable.
	name := "roundtrip/" + string(format)
	//: Marshal is the encode half of the round-trip.
	encoded, mErr := codec.Marshal(format, value)
	//: a marshal fault means the Format cannot encode this value here.
	if mErr != nil {
		//: surface the observed error so the failure is diagnosable.
		return harness.Failed(codecDomain, name, fmt.Sprintf("Marshal: %v", mErr))
	}
	//: an empty encoding is structurally wrong for any non-empty value.
	if len(encoded) == 0 {
		//: report the byte count so the emptiness is visible.
		return harness.Failed(codecDomain, name, "Marshal produced 0 bytes")
	}
	//: a fresh, correctly-typed decode target built from the value's own type.
	target := reflect.New(reflect.TypeOf(value)).Interface()
	//: Unmarshal is the decode half of the round-trip.
	uErr := codec.Unmarshal(format, encoded, target)
	//: a decode fault means the encoded bytes did not parse back.
	if uErr != nil {
		//: surface the observed error and the byte count.
		return harness.Failed(codecDomain, name, fmt.Sprintf("Unmarshal (%d bytes): %v", len(encoded), uErr))
	}
	//: the target is a pointer — compare the pointee against the original value.
	got := reflect.ValueOf(target).Elem().Interface()
	//: the round-trip property: decoded value equals the original.
	if !reflect.DeepEqual(value, got) {
		//: show both sides so the structural drift is diagnosable.
		return harness.Failed(codecDomain, name, fmt.Sprintf("not equal: want %#v got %#v", value, got))
	}
	//: a correct, byte-producing, structurally-equal round-trip.
	return harness.Passed(codecDomain, name, fmt.Sprintf("%d bytes round-tripped equal", len(encoded)))
}

// codecRegistryPopulated asserts importing the codec package activated the
// registry so Available() lists the expected formats rather than an empty set.
func codecRegistryPopulated() harness.Result {
	//: query the live registry the package import was meant to fill.
	available := codec.Available()
	//: an under-populated registry means the activation side-effect did not run.
	if len(available) < codecMinFormats {
		//: report the observed count so the gap is visible.
		return harness.Failed(codecDomain, "registry/populated", fmt.Sprintf("only %d formats registered, want >= %d", len(available), codecMinFormats))
	}
	//: the registry activated and lists the expected breadth of formats.
	return harness.Passed(codecDomain, "registry/populated", fmt.Sprintf("%d formats registered", len(available)))
}
