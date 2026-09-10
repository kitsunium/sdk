<!-- generated from internal/service/codec/multipart/multipart_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=500ms -count=3 ./codec/multipart/` and take medians -->
# Benchmarks — `internal/service/codec/multipart`

`multipart/form-data` (ADR 0037), which is the upload path. Two numbers govern
how much of a request a server can afford to accept, and one of them is worse
than the other by a factor of three.

## Decoding costs 3× encoding, and allocates twice the body

| 1 MiB upload | ns/op | throughput | B/op | allocs |
|---|---:|---:|---:|---:|
| `Marshal` | 611 802 | 1 714 MB/s | 1 061 865 | 66 |
| `Unmarshal` | **1 799 328** | **558 MB/s** | **2 240 839** | 84 |

Encoding allocates one copy of the body, which is the output. **Decoding
allocates two**, and a profile is unambiguous about why: `io.ReadAll` inside
`readBounded` is **99.17 % of all bytes allocated**.

`io.ReadAll` grows by doubling, so materialising a 1 MiB part walks
512 B → 1 KiB → … → 1 MiB → 2 MiB and leaves roughly 2× the final size behind
for the collector. A server accepting a 100 MB upload allocates about 200 MB.

## That allocation is measured and deliberately NOT fixed

The obvious repair is to pre-size the buffer, and it is a trap.

`Unmarshal` receives the whole body as a `[]byte`, so the length that is known
is the **body's**, never the **part's** — `multipart/form-data` gives a part no
declared length, which is the entire reason `io.ReadAll` is there. Pre-sizing
each part to the body's length would allocate 1 MiB per part, so a form with
fifty small fields would go from a few kilobytes to fifty megabytes. The common
case would be destroyed to improve the single-large-part case.

Pre-sizing to `MaxPartBytes` is worse still: a caller who sets a 10 MiB ceiling
would allocate 10 MiB for a two-byte checkbox, and an attacker sending many tiny
parts would get that multiplied — turning a limit into an amplifier.

So the doubling stays. It is the correct behaviour for a stream whose length is
genuinely unknown, and the 2× is what that costs. **What would change the
answer** is a per-part length, which the format does not carry.

## The ceiling refuses 27× cheaper than accepting

| | ns/op | B/op |
|---|---:|---:|
| `Unmarshal`, 1 MiB body, permissive limits | 1 799 328 | 2 240 839 |
| `Unmarshal`, same body, 16 KiB `MaxTotalBytes` | **66 167** | 20 550 |

That ratio is the one that matters on a public endpoint. A limit that cost as
much to enforce as the work it prevents is an amplifier, not a defence; this one
refuses for **3.7 %** of the price of accepting, and allocates 1 % of the
memory. The `max+1` LimitReader — the extra byte that separates a legitimately
cap-sized payload from one that wanted to exceed the cap — is what makes the
refusal decidable that early.

## Framing costs 69 allocations regardless of payload

`Marshal_TextForm` — four small text fields, no upload — is 10 295 ns and **69
allocations** for about 150 bytes of data. That is the delimiters, the
`Content-Disposition` headers and the per-part writer, and it does not shrink
with the payload: the 1 MiB upload costs 66.

So multipart is a poor envelope for small structured data, and that is a
property of the format rather than of this codec. A JSON body of the same four
fields costs a fraction of it. Use multipart when something is genuinely a
FILE.

## The two extension calls

`Boundary` is 819.9 ns and one allocation; `ContentType` is 1 827 ns and eight.
Both are called once per response, and they exist because ADR 0037 refused to
widen `Marshal(v any) ([]byte, error)` for the one format whose delimiter lives
in a header the byte slice cannot carry. That decision costs 1.8 µs per
response.

## Reproducibility envelope

> **Numbers vary across machines**, and the MB/s rows most of all — they are
> memory bandwidth. This run shared the box with one other job; figures are
> medians of three. The allocation column is exact and identical across runs,
> which is where the two claims above rest.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | 42d5f48 |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=500ms -count=3`, medians |

## Results (medians of three)

```
BenchmarkMarshal_TextForm-8                 10295.0 ns/op                      4157 B/op   69 allocs/op
BenchmarkMarshal_Upload_64KiB-8             51914.0 ns/op    1280.08 MB/s     77178 B/op   60 allocs/op
BenchmarkMarshal_Upload_1MiB-8             611802.0 ns/op    1713.92 MB/s   1061865 B/op   66 allocs/op
BenchmarkUnmarshal_TextForm-8               22145.0 ns/op                     12027 B/op   72 allocs/op
BenchmarkUnmarshal_Upload_1MiB-8          1799328.0 ns/op     557.59 MB/s   2240839 B/op   84 allocs/op
BenchmarkBoundary-8                           819.9 ns/op                        32 B/op    1 allocs/op
BenchmarkContentType-8                       1827.0 ns/op                       224 B/op    8 allocs/op
BenchmarkUnmarshal_OverLimit-8              66167.0 ns/op                     20550 B/op   62 allocs/op
```
