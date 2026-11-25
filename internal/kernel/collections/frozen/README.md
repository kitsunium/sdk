# frozen

Read-only maps built once from a known key set and read in place, for the SDK
kernel — stdlib-only, no domain vocabulary. A minimal perfect hash gives every
key its own record; the table is its own binary image, so it is written and
loaded without a copy, and the garbage collector scans none of it.

```go
import "github.com/kitsunium/sdk/internal/kernel/collections/frozen"

symbols, err := frozen.NewStringMap([]string{"cpu", "mem", "disk"}, []uint32{0, 1, 2})
id, ok := symbols.Get("mem") // 1, true

image, _ := symbols.MarshalBinary() // the bytes to store
again, err := frozen.LoadStringMap[uint32](image, frozen.CheckAll) // reads image in place
```

| Type | Keys | Values |
|---|---|---|
| `Map[K, V]` | any integer type | any integer type |
| `StringMap[V]` | `string` | any integer type |
| `Dict` | `string` | `string` |

`Get` allocates nothing, hit or miss. A loaded table reads the caller's bytes
for as long as it lives: keep them alive and unchanged. Load bytes from
anywhere but your own hands under `CheckAll`, which verifies everything and
never lets a `Get` panic; `CheckHeader` is constant time and trusts the body.

Measurements against a native `map` and against `cnfgo`'s `FrozenMap` are in
`BENCH.md`; the design and its reasons in `CLAUDE.md` and ADR 0252.
