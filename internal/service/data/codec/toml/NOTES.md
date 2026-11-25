# internal/service/data/codec/toml — notes

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles.

## Mapping

| TOML | untyped target (`map[string]any`, `any`) | typed targets |
|---|---|---|
| table | `map[string]any` | struct; `map[K]V` (`K` a string, integer or float kind, else a TextUnmarshaler); a slice's last element (one appended to an empty slice); an array's first element, for a table a longer header or a dotted key passes through |
| array, array of tables | `[]any` | slice (replaced); array (filled from the start, extra static elements dropped, an array of tables too long refused, unreached elements left alone) |
| string | `string` | a string kind directly; any other kind through its UnmarshalText (`time.Time`, `net.IP`, the local types…) |
| integer | `int64` | signed, unsigned (range-checked), float kinds directly; any other kind through its UnmarshalText |
| float | `float64` | float kinds (`float32` range-checked); any other kind through its UnmarshalText |
| boolean | `bool` | bool; any other kind through its UnmarshalText |
| offset date-time | `time.Time` (UTC for a zero offset, else a fixed zone) | `time.Time` only |
| local date-time / date / time | `LocalDateTime` / `LocalDate` / `LocalTime` | the same type, or `time.Time` in `time.Local`; nothing else |

`time.Duration` (#1126) is written as the string of its `String()` —
`'1h30m0s'` — and a string into a `time.Duration` is read by
`time.ParseDuration`, as `yaml/` writes and reads it: the rule of the formats
people edit by hand. A string without its unit (`'30'`) is refused with the
type-mismatch refusal (problem `a duration is written with its unit…`), no new
code. A bare integer into a `time.Duration` still reads as **nanoseconds** —
the form this encoder wrote before #1126 — so a file written then loads
unchanged; the issue proposed refusing it, the legacy reading was kept on
purpose (`TestDurationReadsItsTextAndTheOldIntegers`). The match is on the
exact type (`plan.go` `typeDuration`), as `yaml/` matches it: a named type over
`int64` stays an integer.

Into a target that already holds a map — untyped or typed — a table defined
by a `[header]` or by dotted keys is **merged**; an inline table, like every
other value, **replaces** it. These precedences and placements are go-toml's,
replicated on purpose and checked by a typed differential fuzz against it
(11.8 M inputs into a struct of every field shape, from a pre-populated
target).

Encoding: struct fields in declaration order, map keys sorted; key-values
first, then `[tables]` and `[[arrays of tables]]`, a blank line before each
header that does not follow another; strings literal unless they hold an
apostrophe or a control character; floats in the shortest `'f'` form with
`.0` when integral. Tag options: `omitempty`, `omitzero`, `inline`,
`multiline`, `commented`, plus the standalone `multiline:"true"`,
`inline:"true"`, `commented:"true"` and `comment:"…"` tags.
