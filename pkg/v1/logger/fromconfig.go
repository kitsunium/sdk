// Package logger — exposes FromConfig, the capstone of the config-driven writer
// subsystem (ADR 0014 §D5): it builds a fully wired Logger from a config blob
// with zero Go glue. The blob is decoded by a codec the CONSUMER already
// registered (FromConfig imports only the core/codec dispatch surface, never
// pkg/v1/codec or any service codec, so a pkg/v1/logger consumer inherits no
// vendor modules). Each decoded WriterEntry is resolved against the writer
// registry; a Factory that implements ConfigDecoder translates its own option
// map, otherwise a default mapping passes the raw map straight to the factory.
package logger

import (
	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	corewriter "github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Format is the typed wire-format identifier accepted by FromConfig. It is a
// stable alias onto the core codec dispatch surface, so a consumer names a
// format with the same string values the codec facade exposes.
type Format = corecodec.Format

// FromConfig builds a Logger from raw, a config blob in the wire format named by
// format, decoded by a codec the consumer has already registered (blank-import
// github.com/kitsunium/sdk/pkg/v1/codec or a single service codec to activate
// one). It unmarshals raw into a Topology, resolves each WriterEntry against the
// writer registry — calling the Factory's ConfigDecoder when it implements one,
// else a default mapping — composes the sinks via Multi, and returns a Logger
// filtered at the topology's Level.
//
// FromConfig returns TopologyInvalid (1.1.0.4) when format is unregistered, the
// blob is undecodable, the topology has no writers, a writer Name is unknown, or
// a writer rejects its options. The error is redacted: it names only the writer
// and the failure kind, never a decoded credential or option value.
func FromConfig(format Format, raw []byte) (lg Logger, err error) {
	//: decode the blob into a TopologyConfig via the consumer-registered codec.
	topo, dErr := decodeTopology(format, raw)
	//: a malformed blob or missing codec aborts before any sink opens.
	if dErr != nil {
		//: surface the already-redacted topology sentinel.
		return nil, dErr
	}
	//: a topology with no writers has no destination to fan out to.
	if len(topo.Writers) == 0 {
		//: redacted: names the failure kind, never option values.
		return nil, errs.Wrap(TopologyInvalid, errs.WrapParams{}, errs.String("reason", "no writers in topology"))
	}
	//: resolve every entry to a Sink against the writer registry.
	sink, rErr := resolveSinks(topo.Writers)
	//: an unknown name or rejected options aborts construction (redacted).
	if rErr != nil {
		//: forward the already-redacted topology sentinel.
		return nil, rErr
	}
	//: route through NewWithSink so the same encoder + version stamping applies.
	return NewWithSink(SinkConfig{
		Sink:     sink,
		Encoder:  TextEncoder(),
		MinLevel: parseLevel(topo.Level),
	})
}

// decodeTopology unmarshals raw into a Topology using the codec registered under
// format. It stays on the core/codec surface (Lookup + Codec.Unmarshal) so the
// public logger package inherits no vendor codec dependency. Every failure maps
// to the redacted TopologyInvalid sentinel — the raw bytes are never echoed.
func decodeTopology(format Format, raw []byte) (topo TopologyConfig, err error) {
	//: resolve the codec the consumer registered for this format.
	codec, ok := corecodec.Lookup(format)
	//: a missing codec means the consumer never blank-imported it.
	if !ok {
		//: redacted: names only the absent format, never the blob.
		return TopologyConfig{}, errs.Wrap(TopologyInvalid, errs.WrapParams{},
			errs.String("format", format.String()), errs.String("reason", "codec not registered"))
	}
	//: decode into the typed TopologyConfig via the core dispatch surface.
	if uErr := codec.Unmarshal(raw, &topo); uErr != nil {
		//: redacted: report decode failure without echoing the blob contents.
		return TopologyConfig{}, errs.Wrap(TopologyInvalid, errs.WrapParams{}, errs.String("reason", "blob did not decode"))
	}
	//: hand back the decoded topology for resolution.
	return topo, nil
}

// resolveSinks resolves every WriterEntryConfig to a Sink and composes them via
// Multi. A single entry needs no fan-out wrapper; any failure is redacted to
// TopologyInvalid and aborts the whole topology.
func resolveSinks(entries []WriterEntryConfig) (sink Sink, err error) {
	//: pre-size the branch slate with exact cardinality.
	branches := make([]Sink, 0, len(entries))
	//: resolve each named writer to a Sink in order.
	for _, entry := range entries {
		//: build one sink; a failure aborts the whole topology (redacted).
		built, bErr := openEntry(entry)
		//: an unknown name or rejected options stops construction.
		if bErr != nil {
			//: forward the already-redacted sentinel (no option values).
			return nil, bErr
		}
		branches = append(branches, built)
	}
	//: a single writer needs no fan-out wrapper.
	if len(branches) == 1 {
		//: hand back the lone sink directly.
		return branches[0], nil
	}
	//: compose the branches behind the fan-out sink.
	return Multi(branches...), nil
}

// openEntry resolves one WriterEntryConfig to a Sink. It looks the Factory up by
// Name, type-asserts it to Decoder for the typed translation (default mapping
// otherwise), then calls Open. Every failure is redacted to TopologyInvalid and
// names only the writer plus the failure kind — never an option value.
func openEntry(entry WriterEntryConfig) (sink Sink, err error) {
	//: resolve the factory; a missing import surfaces a redacted sentinel.
	factory, ok := corewriter.Lookup(corewriter.Name(entry.Name))
	//: absence path — the writer package was never blank-imported.
	if !ok {
		//: redacted: names only the writer, never its options.
		return nil, redactWriterFailure(entry.Name, "unknown writer name")
	}
	//: translate the option map into a typed Config (decoder or default).
	//: a Factory that implements Decoder owns the translation of its own option
	//: keys; one that does not gets the raw map[string]any straight through as
	//: the opaque Config (= any) — its own Open type-assertion accepts the map
	//: or returns WriterConfigInvalid, which the redaction below scrubs. No
	//: string coercion of secret option values happens on the default path.
	cfg, cErr := corewriter.Config(entry.Options), error(nil)
	//: prefer the factory's own decoder when it implements the extension.
	if decoder, ok := factory.(corewriter.Decoder); ok {
		//: the decoder owns its option keys and returns a typed Config.
		cfg, cErr = decoder.Decode(entry.Options)
	}
	//: a decoder rejection aborts (redacted, never echoes an option value).
	if cErr != nil {
		//: forward the redacted sentinel naming only the writer.
		return nil, redactWriterFailure(entry.Name, "decode config rejected")
	}
	//: open the sink from the resolved config (origin cause redacted below).
	built, oErr := factory.Open(cfg)
	//: a factory Open failure names only the writer (no option values).
	if oErr != nil {
		//: redacted: never echo Open's cause, which may carry option detail.
		return nil, redactWriterFailure(entry.Name, "writer rejected its config")
	}
	//: hand back the opened sink to the caller.
	return built, nil
}

// redactWriterFailure builds the redacted TopologyInvalid sentinel for a
// per-writer failure. It carries only the writer name and a fixed reason
// string — never a decoded credential or option value (the SECRET GATE).
func redactWriterFailure(writer, reason string) error {
	//: attach only the writer name + a fixed reason; no option values.
	return errs.Wrap(TopologyInvalid, errs.WrapParams{},
		errs.String("writer", writer), errs.String("reason", reason))
}

// parseLevel maps a level name (case-insensitive) to a Level. An empty or
// unrecognised name defaults to LevelInfo, matching the Config zero-value rule.
func parseLevel(name string) Level {
	//: match the lowercase canonical names the level package prints.
	switch toLowerASCII(name) {
	//: explicit debug selection.
	case "debug":
		//: most-verbose level.
		return LevelDebug
	//: explicit warn selection.
	case "warn":
		//: abnormal-but-recoverable level.
		return LevelWarn
	//: explicit error selection.
	case "error":
		//: failure level.
		return LevelError
	//: empty / "info" / anything unrecognised falls back to info.
	default:
		//: the documented default severity.
		return LevelInfo
	}
}

// toLowerASCII lowercases an ASCII level name without importing strings, keeping
// the parse allocation-light. Non-ASCII bytes pass through unchanged — level
// names are ASCII by contract.
func toLowerASCII(s string) string {
	//: build into a byte slice sized to the input.
	b := make([]byte, len(s))
	//: fold each ASCII uppercase byte to lowercase.
	for i := range len(s) {
		//: only A–Z need folding; everything else is copied verbatim.
		if s[i] >= 'A' && s[i] <= 'Z' {
			//: shift into the lowercase range.
			b[i] = s[i] + ('a' - 'A')
			//: next byte.
			continue
		}
		//: copy the byte unchanged.
		b[i] = s[i]
	}
	//: hand back the folded name.
	return string(b)
}
