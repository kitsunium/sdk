// Package slogbridge — converts slog levels and attributes to their SDK peers.
package slogbridge

import (
	"log/slog"
	"math"

	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// groupSeparator joins a group chain to an attribute key ("g1.g2.key"). It is
// the separator the SDK text and JSON encoders already emit for nested
// attributes, so a bridged record is indistinguishable from a native one.
const groupSeparator string = "."

// toLevel converts an slog.Level to the SDK Level.
//
// The two scales share their integers by construction — Debug/Info/Warn/Error
// are -4/0/4/8 on both sides — so this is a value-preserving cast, and a custom
// level such as slog.Level(2) keeps its exact position between the named ones.
// Only the width differs: the SDK Level is an int8, so out-of-range levels
// saturate rather than wrap into a lower severity, which is the safe direction
// (an absurdly high level stays an error, it does not become a debug).
func toLevel(lv slog.Level) logger.Level {
	//: saturate below rather than wrap — a wrap would silently promote a
	//: hyper-verbose custom level into a severe one.
	if lv < math.MinInt8 {
		//: clamp to the most verbose level the SDK scale can express.
		return math.MinInt8
	}
	//: saturate above for the mirror reason.
	if lv > math.MaxInt8 {
		//: clamp to the most severe level the SDK scale can express.
		return math.MaxInt8
	}
	//: in-range levels convert exactly; both scales agree on the integers.
	return logger.Level(lv)
}

// qualifyGroup extends the group chain with a nested group's name.
//
// An empty name inlines the group's members, which slog specifies and which
// this expresses by handing the prefix back untouched.
func qualifyGroup(prefix, name string) string {
	//: an empty name adds no level — the members belong to the parent chain.
	if name == "" {
		//: hand the chain back so the members qualify under it directly.
		return prefix
	}
	//: no active chain — this group becomes the first level.
	if prefix == "" {
		//: return the bare name rather than allocating a joined copy.
		return name
	}
	//: join the chain and the new level with the encoders' separator.
	return prefix + groupSeparator + name
}

// qualifyKey prefixes a leaf attribute's key with the active group chain.
//
// An empty KEY is not an empty group: slog's own handlers emit "g.=v" for
// String("", "v") bound under WithGroup("g"), keeping the separator so the
// record still shows which group the value came from. Collapsing it to "g"
// would make a bridged record differ from a native one on the one detail the
// bridge exists to preserve.
func qualifyKey(prefix, key string) string {
	//: no active group — the key stands alone, empty or not.
	if prefix == "" {
		//: return the bare key rather than allocating a joined copy.
		return key
	}
	//: join the chain and the key; an empty key keeps the trailing separator,
	//: matching what slog's TextHandler prints.
	return prefix + groupSeparator + key
}

// appendAttr converts a and appends the result to dst, flattening groups into
// dotted keys under prefix.
//
// slog's elision rules are honoured here rather than pushed down to the SDK
// handler, which has no notion of them: an empty Attr is dropped, and a group
// is expanded into its members.
func appendAttr(dst []logger.Attr, prefix string, a slog.Attr) []logger.Attr {
	//: resolve LogValuer payloads before inspecting the Kind, exactly as the
	//: stdlib handlers do — an unresolved LogValuer would land in Any and
	//: print as a pointer instead of the value it stands for.
	v := a.Value.Resolve()
	//: a group carries members instead of a value; expand it.
	if v.Kind() == slog.KindGroup {
		//: delegate so the recursion has one documented entry point.
		return appendGroup(dst, prefix, a.Key, v.Group())
	}
	//: slog contract: an Attr whose key AND value are zero is ignored.
	if (slog.Attr{Key: a.Key, Value: v}).Equal(slog.Attr{}) {
		//: drop it silently — emitting "=" would be noise, not information.
		return dst
	}
	//: convert the leaf under its fully qualified key.
	return append(dst, convert(qualifyKey(prefix, a.Key), v))
}

// appendGroup flattens a group's members into dst under the group's name.
func appendGroup(dst []logger.Attr, prefix, name string, members []slog.Attr) []logger.Attr {
	//: slog contract: a group with no members is ignored, even when named.
	if len(members) == 0 {
		//: nothing to flatten — leave dst untouched.
		return dst
	}
	//: slog contract: an empty group name inlines its members, which
	//: qualifyGroup expresses by returning the prefix unchanged.
	inner := qualifyGroup(prefix, name)
	//: walk the members; nested groups recurse through appendAttr.
	for _, m := range members {
		//: each member is converted under the extended chain.
		dst = appendAttr(dst, inner, m)
	}
	//: hand back the (possibly re-allocated) slice.
	return dst
}

// convert maps a resolved, non-group slog.Value onto its SDK Attr peer.
func convert(key string, v slog.Value) logger.Attr {
	//: dispatch on the Kind so each payload keeps its type across the bridge;
	//: routing everything through Any would work but would cost the encoders
	//: their type-aware rendering.
	switch v.Kind() {
	//: textual payload.
	case slog.KindString:
		//: String is the direct peer — no widening, no formatting.
		return logger.String(key, v.String())
	//: signed integer payload (slog widens int/int32 to int64 too).
	case slog.KindInt64:
		//: Int64 is the direct peer; slog already widened int/int32 for us.
		return logger.Int64(key, v.Int64())
	//: unsigned integer payload.
	case slog.KindUint64:
		//: Uint64 keeps the value unsigned so it never renders negative.
		return logger.Uint64(key, v.Uint64())
	//: floating-point payload.
	case slog.KindFloat64:
		//: Float64 preserves NaN/Inf, which a string conversion would flatten.
		return logger.Float64(key, v.Float64())
	//: boolean payload.
	case slog.KindBool:
		//: Bool renders as true/false rather than as 0/1.
		return logger.Bool(key, v.Bool())
	//: elapsed-time payload; kept typed so encoders render "1.5s", not a count.
	case slog.KindDuration:
		//: Duration keeps the unit, so encoders emit "1.5s" not a nanosecond count.
		return logger.Duration(key, v.Duration())
	//: instant payload; kept typed so encoders apply the record's own layout.
	case slog.KindTime:
		//: Time stays an instant so encoders apply their own layout to it.
		return logger.Time(key, v.Time())
	//: KindAny and KindLogValuer (already resolved above) land here.
	default:
		//: NOT logger.Any: both bundled encoders render KindAny as "?", so an
		//: slog.Any("err", err) — the most common slog idiom after strings —
		//: would reach the log with its value erased. slog.Value.String
		//: formats any kind the way fmt.Sprint would, which is exactly what
		//: slog's own handlers print, so the text survives. The payload's Go
		//: type is lost; a "?" loses the type AND the value.
		return logger.String(key, v.String())
	}
}
