<!-- generated from pkg/v1/agree/agree_bench_test.go — run `cd pkg && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./v1/agree/` to refresh -->
# Benchmarks — `pkg/v1/agree`

X25519 key agreement. Four benchmarks, and they are arranged to answer one
question exactly: **`SharedKey` costs twice the Diffie-Hellman it performs —
where does the second half go?** The family-wide choice table lives in
[`pkg/v1/crypto/BENCH.md`](../crypto/BENCH.md).

## The choice table

| | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `GenerateKey` (one ephemeral keypair) | **79 463** | 448 | 7 |
| `SharedKey` (agree + HKDF + wrap) | **160 431** | 1 712 | 26 |
| bare `ecdh`, both keys pre-parsed | 78 890 | 32 | 1 |
| bare `ecdh`, keys parsed per call | 157 725 | 320 | 7 |

A full ephemeral handshake — generate, exchange, agree — is therefore
**≈ 240 µs of CPU per party**, and that is the number to budget. It is **227×**
a `mac.Verify` (1 055 ns) and **1.9×** an Ed25519 sign-plus-verify (124.5 µs),
which is the right frame: **key agreement is the most expensive primitive in
this SDK that is not deliberately expensive.**

## The finding: half of `SharedKey` is a public key nobody asked for

| | ns/op | ratio |
|---|---:|---:|
| bare ECDH, both keys already parsed | 78 890 | 1.00× |
| bare ECDH, keys parsed from bytes per call | 157 725 | **2.00×** |
| `agree.SharedKey` | 160 431 | **2.03×** |

A CPU profile settles it without a subtraction. **98.34 % of `SharedKey` is
`crypto/ecdh.x25519ScalarMult`**, and its callers split almost exactly in half:

```
                          1.79s 50.42% |   crypto/ecdh.(*x25519Curve).NewPrivateKey
                          1.76s 49.58% |   crypto/ecdh.(*x25519Curve).ecdh
     0.09s  2.49%  2.49%  3.55s 98.34% | crypto/ecdh.x25519ScalarMult
```

`ecdh.X25519().NewPrivateKey(priv)` **derives the corresponding public key
eagerly**, which is a full scalar multiplication — the same operation the
agreement itself performs, and the same one `GenerateKey` performs, which is why
`GenerateKey` (79 463 ns) and the bare pre-parsed ECDH (78 890 ns) land on top
of each other. The SDK's port takes the private key as `[]byte`, so it pays that
derivation on **every** `SharedKey` call, for a public key it then discards.

**So: one X25519 agreement costs 79 µs, and `agree.SharedKey` costs 160 µs.**
The extra 81 µs is not the SDK's code and it is not HKDF (see below) — it is the
stdlib re-deriving a public key from a private one because the private one
arrived as bytes.

This is recorded, not fixed. Removing it means the port carrying an opaque
parsed-key type with a Go lifetime, which is precisely the trade `pkg/v1/crypto`
makes explicitly for `Key` and this domain deliberately does not — the package
doc's own hygiene contract is "hold `priv` like a password, zero it when done",
and an SDK-held parsed key would undercut it. The trade now has a price:
**81 µs per agreement, or 50 % of the call.** ADR 0014 is where it belongs.

## What the SDK costs over the bare primitive: 1.7 %

| | ns/op | allocs |
|---|---:|---:|
| bare `ecdh`, keys parsed per call — the same work | 157 725 | 7 |
| `agree.SharedKey` — plus HKDF, plus the `Key` wrapper | **160 431** | 26 |

**+2 706 ns, or 1.7 %.** That is the whole SDK envelope, and it *includes* the
HKDF-SHA256 pass the facade runs over the raw secret and the redacting `Key` it
returns — so the security work the package doc insists on (the raw DH secret is
biased, is never handed back, and is zeroized as soon as the KDF has read it)
costs **under 2 % of an agreement**.

The 19 extra allocations are that HKDF pass; `pkg/v1/kdf/BENCH.md` measures a
bare `hkdf.Key` at 16 allocations and 2 µs, which matches this delta almost
exactly. **Nobody should be tempted to skip the KDF for speed** — it is 1.7 % of
the call, and skipping it means using biased key material.

## Reproducibility envelope

> **Numbers vary across machines.** This box is shared with fifteen other agent
> jobs. Each benchmark ran **three times**; every figure quoted above is the
> **fastest of the three** — the least-contaminated sample — and all three are
> printed below.
>
> This package's three runs are unusually tight (under 2.4 % spread on every
> row), because 98 % of every call is one arithmetic-bound stdlib function with
> no allocation in its inner loop. The internal coherence check the table rests on
> — `GenerateKey` ≈ bare pre-parsed ECDH, both one scalar multiplication — holds
> to 0.7 %.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core, 8 cores visible |
| CPU crypto ISA     | `aes`, `pclmulqdq`, `sha_ni`, `sse4_2`, `avx2` |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 (`GOAMD64=v1`) |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | c30e2ad |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s -test.count=3`, machine under load |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/pkg/v1/agree
cpu: AMD EPYC 7351P 16-Core Processor
BenchmarkGenerateKey-8         	   14860	     80101 ns/op	     448 B/op	       7 allocs/op
BenchmarkGenerateKey-8         	   15186	     79563 ns/op	     448 B/op	       7 allocs/op
BenchmarkGenerateKey-8         	   15060	     79463 ns/op	     448 B/op	       7 allocs/op
BenchmarkSharedKey-8           	    7435	    160431 ns/op	    1712 B/op	      26 allocs/op
BenchmarkSharedKey-8           	    7398	    162388 ns/op	    1712 B/op	      26 allocs/op
BenchmarkSharedKey-8           	    7285	    163076 ns/op	    1712 B/op	      26 allocs/op
BenchmarkBareECDHRaw-8         	   14844	     80737 ns/op	      32 B/op	       1 allocs/op
BenchmarkBareECDHRaw-8         	   15201	     78890 ns/op	      32 B/op	       1 allocs/op
BenchmarkBareECDHRaw-8         	   14943	     80396 ns/op	      32 B/op	       1 allocs/op
BenchmarkBareECDHWithParse-8   	    7596	    157725 ns/op	     320 B/op	       7 allocs/op
BenchmarkBareECDHWithParse-8   	    7603	    160746 ns/op	     320 B/op	       7 allocs/op
BenchmarkBareECDHWithParse-8   	    7424	    160107 ns/op	     320 B/op	       7 allocs/op
PASS
ok  	github.com/kitsunium/sdk/pkg/v1/agree	14.404s
```
