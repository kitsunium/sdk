<!-- generated from internal/service/data/codec/json/append_bench_test.go — run `go test -run='^$' -bench='Marshal1KiB|Append1KiB' -benchmem -benchtime=500ms -count=6 ./internal/service/data/codec/json/` to refresh -->
# Benchmarks — `internal/service/data/codec/json`

| | |
|---|---|
| Machine | AMD EPYC 7351P 16-Core Processor, 8 vCPU, 15 GiB |
| OS | Linux 6.12.101+deb13-amd64 |
| Go toolchain | go1.27.3 linux/amd64 |
| Base commit | `740e7c1e` (+ the change measured below) |
| Measured | 2026-11-03, `-benchtime=500ms -count=6`, interleaved old/new, medians (benchstat) |

> **Numbers vary across machines.** Compare deltas measured on one machine,
> never figures from two.

## Marshal and Append of a value of about 1 KiB (#1124)

`Append` writes onto a caller buffer that already has the room, so its
figures are what Append costs beyond the bytes themselves.

The value is a pointer to a struct of about 1 KiB of JSON (`kibValue`).

| benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Marshal1KiB` | 5 940 → 5 847 (~) | 1 028 → 1 028 | 2 → 2 |
| `Append1KiB` | 5 528 → 5 638 (+2 %) | 4 → 4 | 1 → **1** |

`Append` used to encode into a pooled buffer through `json.NewEncoder` and copy
the result onto `dst` after dropping the encoder's newline; it now writes
through a pooled `scratch.AppendWriter` over `dst` with json/v2's `MarshalWrite`
under the v1 options, so the only copy left is the encoder's own flush. At
1 KiB the copy saved is below the noise. The one allocation of 4 bytes left is
`encoding/json`'s own (it is in `Marshal`'s two as well — the other is the
result); `Append` adds none to it.

## encoding/json/v2 under the v1 options (#893)

`codec_bench_test.go`: `benchDoc` (scalars, a string list, a string map,
nested records) small (one record), medium (ten children) and large (two
hundred). Base `740e7c1e` against the change, interleaved,
`-benchtime=200ms -count=8`, medians:

| benchmark | allocs/op (v1 → v2) | B/op (v1 → v2) |
|---|---:|---:|
| `Marshal/small` | 5 → 5 | 160 → 160 |
| `Marshal/medium` | 25 → 25 | 1 536 → 1 536 |
| `Marshal/large` | 405 → 405 | 39 475 → 39 475 |
| `Append/small` | 4 → 4 | 64 → 64 |
| `Append/medium` | 24 → 24 | 384 → 384 |
| `Append/large` | 404 → 404 | 6 737 → 6 480 |
| `Unmarshal/small` | 7 → 7 | 512 → 512 |
| `Unmarshal/medium` | 72 → 72 | 7 874 → 7 874 |
| `Unmarshal/large` | 1 355 → 1 355 | 158 515 → 158 515 |
| `StreamEncode/*` | 4 / 24 / 404 → same | same |
| `StreamDecode/*` | 13 / 82 / 1 369 → same | same |

**No allocation added, none removed but `Append`'s copy buffer on a large
document.** That is what the measure says the migration is in Go 1.27:
`encoding/json` is json/v2 under `DefaultOptionsV1()` already
(`GOEXPERIMENT=jsonv2` is in the toolchain's baseline), so `Marshal` and
`Unmarshal` are the very calls the v1 API made. In one binary,
`stdjson.Marshal(v)` and `jsonv2.Marshal(v, stdjson.DefaultOptionsV1())` time
the same on the 1 KiB value (5 166 against 5 164 ns/op).

Time: within noise on every row but `Marshal1KiB` / `Append1KiB` and the two
`small` rows, which the change's test binary runs 4-15 % slower than the
base's. The slowdown stays when the codec's `Marshal` is put back on
`stdjson.Marshal` (the identical call) and when `v2.go` and the golden tests
are left out of the binary, and an A/A run of the base binary against itself
shows none: it is where the binary's code lands, not the code path. Read it
as a layout artefact of this test binary until a run on another machine says
otherwise.

The bytes are pinned, not measured: `golden_external_test.go` holds `Marshal`,
`Append` and the stream encoder to `testdata/golden`, written by the base's
codec on encoding/json v1, and `Unmarshal` and the stream decoder to what they
read there.
