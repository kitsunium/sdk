package otlpjson

import (
	"encoding/hex"
	"strconv"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// The OpenTelemetry slog bridge's severity mapping: SeverityNumber = level+9,
// so DEBUG (-4) is 5, INFO (0) 9, WARN (4) 13, ERROR (8) 17, held inside the
// specification's 1 (TRACE) to 24 (FATAL4).
const (
	severityOffset int = 9
	severityMin    int = 1
	severityMax    int = 24
	// decimalBase formats the integers JSON carries as numbers.
	decimalBase int = 10
	// groupSeparator joins a group's name and its attributes' keys.
	groupSeparator byte = '.'
)

// EncodeOTLPJSON writes recs as one OTLP/JSON ExportLogsServiceRequest under
// resource and scope — the document the writer hands its exporter. Every field
// in field-number order; a record outside a span carries no traceId and no
// spanId. It cannot fail: every value has an OTLP spelling.
func EncodeOTLPJSON(resource coreotel.ResourceValue, scope coreotel.ScopeValue, recs []corelogger.RecordEvent) []byte {
	//: a fresh document; the writer reuses its own buffer through appendRequest.
	return appendRequest(nil, otlp.ResourceOf(resource), otlp.ScopeOf(scope), recs)
}

// appendRequest appends the ExportLogsServiceRequest of recs to dst:
// resourceLogs (1) → resource (1), scopeLogs (2) → scope (1), logRecords (2).
func appendRequest(dst []byte, resource otlp.ResourceMessage, scope otlp.ScopeMessage, recs []corelogger.RecordEvent) []byte {
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "resourceLogs")
	dst = append(dst, '[', '{')
	dst = otlp.AppendKey(dst, "resource")
	dst = resource.AppendJSON(dst)
	dst = otlp.AppendKey(dst, "scopeLogs")
	dst = append(dst, '[', '{')
	dst = otlp.AppendKey(dst, "scope")
	dst = scope.AppendJSON(dst)
	dst = otlp.AppendKey(dst, "logRecords")
	dst = append(dst, '[')
	//: one LogRecord per record, in order.
	for i := range recs {
		//: comma-separated.
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendLogRecord(dst, &recs[i])
	}
	//: logRecords, scopeLogs, resourceLogs, the request.
	return append(dst, ']', '}', ']', '}', ']', '}')
}

// appendLogRecord appends one LogRecord in field-number order:
// timeUnixNano (1), severityNumber (2), severityText (3), body (5),
// attributes (6, omitted when none), traceId (9) and spanId (10), both hex
// and both omitted outside a span.
func appendLogRecord(dst []byte, rec *corelogger.RecordEvent) []byte {
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "timeUnixNano")
	dst = otlp.UnixNano(rec.Time).AppendJSON(dst)
	dst = otlp.AppendKey(dst, "severityNumber")
	dst = strconv.AppendInt(dst, int64(severityNumber(rec.Level)), decimalBase)
	dst = otlp.AppendKey(dst, "severityText")
	dst = otlp.AppendString(dst, rec.Level.String())
	dst = otlp.AppendKey(dst, "body")
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "stringValue")
	dst = otlp.AppendString(dst, rec.Message)
	dst = append(dst, '}')
	//: attributes (6), the typed OTel attributes.
	if len(rec.Attrs) > 0 {
		dst = otlp.AppendKey(dst, "attributes")
		dst = append(dst, '[')
		dst, _ = appendAttrs(dst, "", rec.Attrs, false)
		dst = append(dst, ']')
	}
	//: the correlation pair only inside a span (ADR 0062).
	if rec.TraceContext.IsValid() {
		dst = otlp.AppendKey(dst, "traceId")
		dst = appendHex(dst, rec.TraceContext.TraceID[:])
		dst = otlp.AppendKey(dst, "spanId")
		dst = appendHex(dst, rec.TraceContext.SpanID[:])
	}
	//: closed.
	return append(dst, '}')
}

// severityNumber maps a level onto OTLP's SeverityNumber as the
// OpenTelemetry slog bridge does: level+9, held in 1…24.
func severityNumber(l level.Level) int {
	//: the band, clamped to the specification's range.
	return min(max(int(l)+severityOffset, severityMin), severityMax)
}

// appendHex appends id as a quoted lowercase hex string.
func appendHex(dst, id []byte) []byte {
	dst = append(dst, '"')
	dst = hex.AppendEncode(dst, id)
	//: closed.
	return append(dst, '"')
}

// appendAttrs appends attrs as KeyValues, a group flattened under its name
// joined by '.', and reports whether anything was written (wrote carries the
// comma state across a group's recursion).
func appendAttrs(dst []byte, prefix string, attrs []corelogger.AttrValue, wrote bool) ([]byte, bool) {
	//: each attribute, groups recursed into.
	for i := range attrs {
		attr := &attrs[i]
		key := attr.Key
		//: inside a group the key is qualified by it.
		if prefix != "" {
			key = prefix + string(groupSeparator) + key
		}
		//: a group contributes its members, not itself.
		if attr.Value.Kind() == corelogger.KindGroup {
			dst, wrote = appendAttrs(dst, key, attr.Value.Group(), wrote)
			continue
		}
		//: comma-separated across groups too.
		if wrote {
			dst = append(dst, ',')
		}
		dst = appendKeyValue(dst, key, attr.Value)
		wrote = true
	}
	//: what was written, and whether anything was.
	return dst, wrote
}

// appendKeyValue appends {"key":…,"value":{…}} with the value typed as the
// OpenTelemetry slog bridge types it: a bool, an int64, a double and a
// string as themselves; a uint64 an int64 when it fits and a string when it
// does not; a duration its int64 nanoseconds; a time its int64 Unix
// nanoseconds; anything else its rendered string.
func appendKeyValue(dst []byte, key string, v corelogger.Value) []byte {
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "key")
	dst = otlp.AppendString(dst, key)
	dst = otlp.AppendKey(dst, "value")
	dst = append(dst, '{')
	dst = appendAnyValue(dst, v)
	//: value, then the KeyValue.
	return append(dst, '}', '}')
}

// appendAnyValue appends the AnyValue member that types v.
func appendAnyValue(dst []byte, v corelogger.Value) []byte {
	//: the kind decides the member.
	switch v.Kind() {
	//: a bool.
	case corelogger.KindBool:
		dst = otlp.AppendKey(dst, "boolValue")
		return otlp.AppendBool(dst, v.Bool())
	//: an int64, a decimal string on the wire.
	case corelogger.KindInt64:
		return appendInt(dst, v.Int64())
	//: a uint64: an int64 while it fits.
	case corelogger.KindUint64:
		return appendUint(dst, v.Uint64())
	//: a double.
	case corelogger.KindFloat64:
		dst = otlp.AppendKey(dst, "doubleValue")
		return otlp.Double(v.Float64()).AppendJSON(dst)
	//: a duration, in nanoseconds.
	case corelogger.KindDuration:
		return appendInt(dst, v.Duration().Nanoseconds())
	//: a time, in Unix nanoseconds.
	case corelogger.KindTime:
		return appendInt(dst, v.Time().UnixNano())
	//: a string, and whatever renders as one.
	default:
		dst = otlp.AppendKey(dst, "stringValue")
		return otlp.AppendString(dst, v.String())
	}
}

// appendInt appends an intValue member.
func appendInt(dst []byte, n int64) []byte {
	dst = otlp.AppendKey(dst, "intValue")
	//: a decimal string, as OTLP/JSON spells an int64.
	return otlp.Int64(n).AppendJSON(dst)
}

// appendUint appends an intValue member for a uint64 that fits an int64,
// a stringValue for one that does not — OTLP has no unsigned 64-bit value.
func appendUint(dst []byte, n uint64) []byte {
	//: past int64: the decimal digits as a string.
	if n > 1<<63-1 {
		dst = otlp.AppendKey(dst, "stringValue")
		return otlp.AppendString(dst, strconv.FormatUint(n, decimalBase))
	}
	//: an int64.
	return appendInt(dst, int64(n))
}
