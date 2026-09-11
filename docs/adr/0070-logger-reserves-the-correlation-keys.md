# ADR 0070 — the two keys the logger writes itself are reserved, and a caller's are renamed rather than dropped

- **Status**: Accepted
- **Date**: 2026-09-11
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0062](0062-logger-trace-correlation.md) — what happens when a caller's attribute spells one of the two keys it introduced
- **Related**: [ADR 0051](0051-sdk-trace-domain.md) (the span context this correlates to), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (the SDK decides rather than leaving the dangerous outcome to chance)

## Context

ADR 0062 writes `trace_id` and `span_id` as **top-level** fields of every
record emitted inside a span, because that is where the OpenTelemetry log data
model puts them and where every ingestion pipeline looks. It did not say what
happens when the caller's own attributes carry one of those names.

They do. `logger.Info(ctx, "done", logger.String("trace_id", upstreamID))` is
the natural thing to write when a service forwards somebody else's identifier,
and until now it produced this:

```json
{"ts":"…","level":"INFO","msg":"done","trace_id":"4bf92f35…","trace_id":"whatever-the-caller-had"}
```

Two members of one JSON object with the same name. RFC 8259 §4 does not forbid
it and does not say which one a decoder keeps: `encoding/json` keeps the last,
and so do most pipelines. So the field the SDK wrote to make the line joinable
to a trace is the one that gets discarded, and the value that replaces it comes
from wherever the caller got it — a header written by a stranger, in the shape
this exact pattern is used for. The text encoder has the same shape with the
same outcome for anything parsing `key=value` pairs.

Nothing observes it. The line renders, the record is complete, every test
passes, and what is wrong is a correlation that points at another trace.

## Decision

`trace_id` and `span_id` are **reserved at the top level**. An attribute whose
key is one of them is rendered under `attr.` — `attr.trace_id` — by every
writer in the SDK: both encoders (`json`, `text`) and the legacy
single-writer `TextHandler`, which renders the correlation itself and so has
its own two write sites.

- **Renamed, never dropped.** The caller logged a value on purpose; losing it
  silently is the second failure mode of the same shape. Renaming keeps every
  byte the caller wrote and keeps it unable to be mistaken for the span the
  line came from.
- **Top level only.** Under `WithGroup("http")` the key is already
  `http.trace_id`, which collides with nothing and which no pipeline reads as
  the correlation — ADR 0062's own reason for putting the field at the top
  level. A grouped attribute is left exactly as it was.
- **The prefix is `attr.`**, spelled once as `encoder.ReservedPrefix`, and the
  reserved set is `encoder.ReservesKey` — one predicate, so a future key the
  SDK writes itself is reserved in every writer by editing one function.

The set is exactly the two keys ADR 0062 introduced. `ts`, `level`, `msg` and
`framework_version` are deliberately NOT reserved: they are not a security
boundary, they predate every caller in the wild, and reserving them now would
rename attributes in lines nobody has a problem with.

## Consequences

- A caller who logs `trace_id` sees it arrive as `attr.trace_id`. That is a
  visible change, and the one the ADR is for: the alternative is a line whose
  correlation is chosen by whoever supplied the value.
- A record outside a span still emits neither key (ADR 0062), and the
  reservation applies anyway — the rename does not depend on a span being
  present, so a line does not change shape depending on whether it was traced.
- No cost on the common path: the check is two string comparisons per
  top-level attribute, and no attribute in the SDK's own emission is affected.

## Breaking changes

Behavioural, for one shape: a caller logging an attribute named `trace_id` or
`span_id` at the top level. The key it renders under changes; the value does
not. No API changes.

## Why not

- **Drop the caller's attribute.** It reports success while discarding
  something the caller asked for, which is the failure this ADR is about, one
  step further along.
- **Let the collision stand and document it.** It is invisible: the defect is
  in a decoder the SDK does not own, and its symptom is a correlation that
  looks right.
- **Refuse the record.** A logger that fails on an attribute name turns a
  naming choice into a dropped log line, exactly where lines matter most.
- **Reserve every top-level key.** `msg` and `level` carry no identity and
  renaming them buys nothing for the churn.

## Deferred

- **A configurable prefix.** One more knob for a value nothing reads
  semantically. `attr.` is spelled once and can become configurable the day a
  caller shows a conflict with it.

## References

- `internal/service/logger/encoder/encoder.go` (`ReservedPrefix`,
  `ReservesKey`), `encoder/json.go`, `encoder/text.go`,
  `internal/service/logger/text_handler.go`.
- `TestATopLevelAttributeCannotSpellTheSDKsOwnFields` (encoder, JSON and text,
  grouped and not) and
  `TestTextHandler_ATopLevelAttributeCannotSpellTheSDKsOwnFields`.
- RFC 8259 §4 (an object's members are not required to be unique, and no
  behaviour is prescribed when they are not).
