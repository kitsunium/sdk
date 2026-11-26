# internal/service/data/codec/bson/ — reference

Moved unchanged from `internal/service/data/codec/bson/CLAUDE.md` (ADR 0253); each section keeps its title.

## Mapping

Encode — exact types first, then a type's own `MarshalBSON`, then its kind:

| Go | BSON |
|---|---|
| `bool` | boolean |
| `int8`, `int16`, `int32`, `uint8`, `uint16` | int32 |
| `int` | int32 when it fits, int64 otherwise |
| `int64` | int64; int32 under `minsize` when it fits |
| `uint`, `uint32`, `uint64` | int64; int32 under `minsize` when ≤ MaxInt32; past MaxInt64 refused |
| `float32`, `float64` | double |
| `string` | string (must be UTF-8) |
| `[]byte`, a `[]byte`-element slice, `[N]byte` | binary subtype 0x00; nil → null |
| other slices, arrays | array; nil slice → null |
| `D`, `[]E`, `[N]E` | document, in order; nil → null |
| maps (`string`, integer, `TextMarshaler` keys) | document, **keys sorted**; nil → null, except at the top level, where it is `{}` |
| structs | document of their fields, declaration order, `",inline"` flattened, `",inline"` map last |
| pointers, interfaces | their target; nil → null |
| `time.Time` | datetime, UTC milliseconds (truncated) |
| `time.Duration` | int64 **nanoseconds** — the binary formats' rule (#1126); the text formats write its `String()` |
| `url.URL` | string; `json.Number` → int64 when it parses as one, else double |
| the value types | their BSON type; `Regex` options written sorted |

Struct tags: `bson:"name,omitempty,omitzero,minsize,truncate,inline"`, `bson:"-"` skips.
`omitzero` (#1103) is encoding/json's since Go 1.24 — the field type's `IsZero()`
when it declares one (through the field's address, or an addressable copy, when
only the pointer does; a nil pointer or interface is zero without being asked),
else `reflect.Value.IsZero` — decided once per field when the plan is built
(`omitzero.go`); with `omitempty` too, either one leaves the field out.
No tag → the field name lower-cased. A tag with no key and no colon (`` `name` ``)
is read as a bson tag. Every comma part, the first included, is matched as an
option. An embedded struct WITHOUT `",inline"` is a sub-document named after
its type. Two fields claiming one name: the shallower wins, equal depth refuses
the type. `omitempty`: `IsZero()` when the type has it, length 0, never a struct,
an interface only when nil.

Decode — numbers convert into any numeric target they fit (a double only when
whole, unless `truncate`), booleans and numbers into each other, null and
undefined into every zero value; a string target also takes a symbol, an
ObjectID (hex) and a generic binary; `[]byte` takes a generic binary, a string
or a symbol; `time.Time` takes a datetime, an int64 (ms), a timestamp (s) or a
`2006-01-02T15:04:05.999Z07:00` string. Fields match exactly, then by the
element name lower-cased. Unknown elements go to the `",inline"` map or are
skipped. A slice is decoded into its existing backing array when it has room; a
map is added to; a pointer is allocated, and a null sets it back to nil.

Interface targets: double → `float64`, int32 → `int32`, int64 → `int64`, string
→ `string`, boolean → `bool`, null → `nil`, array → `A`, datetime → `DateTime`,
binary → `Binary`, and each other BSON type to its value type. A document
becomes the **ancestor** type — the nearest enclosing `map[string]any`, `M` or
slice of `E` — or `D` when there is none: `*any` gives D all the way down,
`map[string]any` gives maps all the way down; a struct field resets it.

Hooks: `MarshalBSON() ([]byte, error)` (the bytes must be one well-formed
document) and `UnmarshalBSON([]byte) error` (a copy of the value's bytes; the
whole document at the top level). A pointer-receiver hook is reached only
through an addressable value, as the driver reached it.

## Differences from the MongoDB driver

Intentional, each refusing what the driver accepted or wrote silently:

- invalid UTF-8 in a string, key, regex or namespace is refused both ways (the
  specification's corpus requires it; the driver accepted it);
- a malformed element the target does not keep is refused (the driver skipped
  it unread);
- nesting past 100 levels is refused (the driver recursed until the stack
  ran out);
- map keys are written sorted (the driver wrote Go's random iteration order);
- a nil pointer whose type has `MarshalBSON` encodes as null (the driver called
  the method on nil); a double past the int64 range, NaN included, is refused
  for an integer target even under `truncate` (the driver's result was
  platform-dependent); NaN decodes into a `float32` (the driver refused it);
- a subtype 0x02 binary must carry a consistent inner length;
- not ported: `bsoncodec` registries and options, `Raw`/`RawValue`,
  `ValueMarshaler` and `Proxy` (their signatures name driver types), extended
  JSON, `KeyMarshaler`, ObjectID generation (an `id` scheme's job).
