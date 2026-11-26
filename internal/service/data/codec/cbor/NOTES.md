# internal/service/data/codec/cbor/ — reference

Moved unchanged from `internal/service/data/codec/cbor/CLAUDE.md` (ADR 0253); each section keeps its title.

## What is written

- Integers, lengths and tag numbers in their **shortest head** (RFC 8949 §4.1).
- A `float64` as a double and a `float32` as a single — **no shortest-float**
  conversion, as before; NaN as `0xf97e00`, ±Inf as `0xf97c00` / `0xf9fc00`.
- A `time.Time` as its **integer Unix seconds, untagged** — the fraction of a
  second is dropped, as fxamacker's default `TimeUnix` dropped it — and the
  zero time as null. This is kept on purpose: changing it changes the bytes.
  A field tagged `cbor:",time=tag1"` is written as **tag 1**: integer seconds
  when it has no fraction, the shortest float when a double carries the
  instant to the nanosecond as `floatTime` reads it back, and otherwise **tag
  0** with its RFC 3339 text to the nanosecond — RFC 8949 §3.4.2 gives tag 1
  nothing finer than a double, and the instant is never rounded.
  `cbor:",time=tag0"` always writes tag 0. The option on a field that is not a
  `time.Time` refuses the struct.
- A `time.Duration` as an integer of **int64 nanoseconds** (#1126) — the rule
  of every binary format: no standard tag carries a duration, and the integer
  is what the `reflect.Int64` path writes. The text formats (yaml, toml) write
  its `String()`; `pkg/v1/data/codec`'s `TestDurationAgreesAcrossFormats`
  pins all three sides.
- A `Tag` as its head and its content, encoded like any value; a tag 0–3 whose
  content is not its RFC type is refused (`tagAdmits`, the validator's rule).
- A `big.Int` as an integer when it fits one, a bignum (tag 2 / 3) otherwise.
- `encoding.BinaryMarshaler` → byte string; a type with `MarshalCBOR()
  ([]byte, error)` writes its own item, which is checked to be exactly one
  valid item first. `encoding.TextMarshaler` is **not** consulted (as before).
- **Map pairs sorted** by the bytes of their encoded keys (RFC 8949 §4.2.1):
  equal values encode to equal bytes. Text keys sort shorter-first, then
  bytewise — which is the same order.
- Nothing the decoder would refuse: no invalid UTF-8, no two keys encoding
  alike, nothing nested past 32 (`walkDepth.enter`), no unbounded chain of
  pointers and interfaces (`maxIndirections`, a cycle), no pointer type that
  points to itself.

## What is read

- **Validation first** (`validate.go`): exactly one item, well-formed (no
  reserved additional information, no indefinite integer or tag, chunks of the
  string's own type and definite, no stray break, two-byte simple values ≥ 32)
  and valid (UTF-8 text **wherever it sits**, tags 0–3 enclosing their RFC
  types), within the bounds `maxCBORArrayElements`, `maxCBORMapPairs`,
  `maxCBORStringChunks` (1 << 20 each) and `maxCBORNestedLevels` (32 levels of
  arrays, maps **and tags**). A refused document never touches the target, and
  no count is acted on before it passed.
- Untyped (`any`): `uint64`, `int64` (or `big.Int` below `math.MinInt64`),
  `[]byte`, `string`, `[]any`, **`map[string]any` when every key is text,
  `map[any]any` otherwise**, `bool`, `nil`, `float64`, `time.Time` (tags 0/1),
  `big.Int` (tags 2/3); any other tag is transparent. A `Tag` target takes any
  tag, its content decoded as into `any`; an untagged item is a mismatch.
- Typed: struct keys match the field exactly, then case-insensitively; integer
  keys match `keyasint`; an unknown key is skipped; a repeated key keeps its
  first value; a map keeps its entries and the last of equal keys wins. An item
  the target cannot hold is recorded, skipped, and decoding goes on — the first
  failure is returned (encoding/json's and fxamacker's rule).
- `encoding.BinaryUnmarshaler` takes a byte string; a type with
  `UnmarshalCBOR([]byte) error` gets the whole item (aliasing the input: it
  must copy what it keeps).
- The stream decoder buffers one item at a time and validates each byte once
  however the item is split across reads (the validator resumes). For an
  untrusted peer, `NewBoundedDecoder(r, maxBytes)` (#891) bounds each item:
  its buffer never holds more than `maxBytes` bytes (reads are cut to the room
  left, growth capped), and an unfinished item that fills it is refused with
  `SizeExceeded` before another byte is read — so at most `maxBytes` bytes of
  a hostile item are ever read. The refusal is placed at `offset` `maxBytes`,
  the first byte past the ceiling within the item, as the stream's other
  refusals are placed (#1125); `Decode` refuses a nil or non-pointer target
  with `TARGET_INVALID` before reading (#1101), bounded or not. The validator knows where an item ends, so no
  look-ahead byte is needed, unlike json and xml. A ceiling of zero or below is
  the core's `BOUND_INVALID`. The unbounded path pays one `limit > 0` test per
  read: `StreamDecode` unchanged (`BENCH.md`, #891).

## Performance

Plans are resolved once per `reflect.Type` (cached in a `sync.Map`, recursive
types handled by building in a private map and publishing complete graphs);
`map[string]any`, `[]any`, `map[string]string` and the common scalars bypass
reflection. `Marshal` allocates exactly its result; `Append` into a sized
buffer allocates nothing; the decode state and map holders are recycled through
`kernel/concur/recycler`.

Against fxamacker, measured side by side in CPU time (`BENCH.md` says how, and
why not wall time): at parity or faster on every benchmark but two, with
allocations down or equal everywhere — a thousand records encode in one
allocation instead of a thousand. The two exceptions are explained there:
`MarshalMap16` (1.55×) is the price of sorting, which fxamacker charges just
the same when asked to sort, and `UnmarshalSmall` (1.23×) is the validation
pass's fixed cost on a 19-byte document. `codec_bench_test.go` holds the
thirteen benchmarks; `BENCH.md` is regenerated by hand from them.
