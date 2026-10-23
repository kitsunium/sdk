# bench/cref - the C reference bench

A diagnostic: what the same primitives cost in C, on the same machine, so the
Go side of the net path (`security/token`, the loop engine, the `http1` codec)
can be compared with a ceiling instead of a feeling. It is **never a CI gate**
and has **no `go.mod`**, so it stays out of `scripts/ci/go-modules.sh`, of
`scripts/cross-platform-audit.sh`'s census and of every Go lane. The final
measure of the work is the submission's score on `bench/load.js`; ratios from
here only say where the time goes.

## What is in it

| Program | What it measures | Notes |
|---|---|---|
| `hmac_bench evp` | HMAC-SHA256 of a ~200 B JWT signing input, OpenSSL 3 `EVP_MAC` | the MAC is fetched once, a keyed template `EVP_MAC_CTX` is built once, and each message does `EVP_MAC_CTX_dup` + update + final |
| `hmac_bench reinit` | the same, one keyed ctx reused | per message `EVP_MAC_init(ctx, NULL, 0, NULL)` resets it to the keyed state, then update + final: no allocation |
| `hmac_bench mid` | the same, from precomputed ipad / opad SHA-256 midstates | per message: copy the two `SHA256_CTX`, update, final, twice. Uses the low-level `SHA256_*` API (deprecated in OpenSSL 3, still shipped) because a midstate copy has no allocation-free EVP form. **Keep the fastest of the three** as the C figure |
| `hmac_bench selftest` | RFC 4231 case 2 for all three forms, and their agreement for every length 0..300 | run by `make test` |
| `parse_test` | the parser corpus: accepted shapes and framing, every refusal, smuggling vectors, every strict prefix of a valid request | run by `make test` and `make sanitize` |
| `parse_bench own` | `src/http_parse.c`, a C parser with exactly the S5 scope and refusals | see the header of `http_parse.h` for the list; the size limits are this reference's choices and must be aligned with the Go codec's constants |
| `parse_bench pico` | picohttpparser, informational | it implements none of the S5 refusals, so it does strictly less checking; the bench makes it also find Host, Authorization, Content-Length and compute `consumed`. Pinned to commit `465a7ff0`, files checked by sha256 in the Makefile; compiled `-O3 -march=native`, and `src/pico_guard.c` is a `#error` without `__SSE4_2__` |
| `parse_bench stream` | writes the pipelined byte stream to stdout | feed the same bytes to the Go parser |
| `echo_epoll`, `echo_uring` | kernel baseline: one thread, one recv and one send per request, no HTTP | uring: multishot accept, multishot recv into a provided-buffer ring, `COOP_TASKRUN` (`-d` for `SINGLE_ISSUER`+`DEFER_TASKRUN`) |
| `echo_client` | the load generator for the echo baseline | static binary, epoll, checks every echo byte for byte |
| `pairstat` | ratio of two paired series with a bootstrap 95 % interval | 10000 resamples, fixed seed |

Everything is built `-O3 -march=native` (so the binaries are tied to the host
that built them) with `-Wall -Wextra -Wpedantic -Wshadow -Wconversion -Werror`.
C23 is required (`gcc` 14 or newer, `clang` 19 or newer).

## Run it on the bench VM 602

```sh
ssh -i ~/.ssh/id_rsa -J root@135.125.105.110 kodflow@192.168.100.107   # Debian 13, kernel 6.12, 1 vCPU
sudo apt-get install -y build-essential liburing-dev libssl-dev        # once; clang-tidy for `make tidy`
cd bench/cref && make && make test
```

`make` downloads picohttpparser from GitHub at the pinned commit and refuses a
file whose sha256 differs. `make sanitize` runs both test programs under
ASan + UBSan. `make tidy` runs clang-tidy.

The VM must be otherwise idle: stop the submission under test, and check
`steal` in the run's `env.txt` and `/proc/stat` stays near zero.

### Primitives (no load generator)

```sh
./run.sh micro
```

Six pairs per comparison, the order alternating (A,B then B,A, ...) so drift
does not favour a side:

- HMAC: `evp` (A) against `mid` (B), then `reinit` (A) against `mid` (B), `ns_per_op`, `cpu_us_per_op`, user and system split;
- parser: ours (A) against picohttpparser (B).

### Echo baseline (needs another host)

The load generator must run on a different host, otherwise the client eats the
server's only core. The default is VM 603 `mcp-loadgen` (192.168.100.177,
6 vCPU), on the same bench network. `run.sh` runs on the VM under test and
reaches the generator over ssh, so that VM needs a way in. The simplest is to
forward your agent when you log in (no key is copied):

```sh
ssh-add ~/.ssh/id_rsa
ssh -A -J root@135.125.105.110 kodflow@192.168.100.107
cd bench/cref
ulimit -n 65535
GEN_SSH="ssh -o StrictHostKeyChecking=accept-new kodflow@192.168.100.177" \
SUT_ADDR=192.168.100.107 ./run.sh echo
```

`run.sh` pushes the static `echo_client` to `$GEN_DIR` (`cref-gen`) on the
generator, then per run: starts the server, lets the client run `WARM` seconds,
sends `SIGUSR1` (the server takes its CPU and request baselines), lets the
client finish `SECS` seconds, sends `SIGTERM` (the server prints one line).
`PORT`, `CONNS`, `MSG`, `SECS`, `WARM`, `PAIRS` set the shape (defaults: 256
connections, 64 B, 20 s after 4 s, 6 pairs). The load shape is a request/response
echo, not the challenge's mix: it is the cost of the kernel path only.

`k6` (`~/bin/k6` on VM 603) drives the Go submission with `bench/load.js` and is
not used by this tree; the echo servers speak no HTTP.

### What gets recorded

`results/<UTC stamp>/` (git-ignored):

- `env.txt`: kernel, CPU model and MHz, `sha_ni` / `avx2` / `avx512f` / `sse4_2` / TSC flags,
  clock source, governor, `ulimit -n`, compiler, OpenSSL and liburing versions, and when a
  Go toolchain is on the host its version and `GOAMD64` (the Go side's microarchitecture
  level must be quoted next to any ratio);
- `<comparison>.<metric>.tsv`: the raw rows, one per pair;
- `summary.txt`: for each metric, the mean of A, the mean of B, the geometric mean of B/A and
  its bootstrap 95 % interval, with `excludes_1=yes|no`;
- `echo-lines.txt`: the full server and client lines of each echo run.

CPU per operation is `getrusage(RUSAGE_SELF)` utime and stime over the measured
window divided by the operations in it. It does not see softirq work that the
kernel charges to whichever task is running, so the echo runs also record the
host-wide busy, softirq and steal shares from `/proc/stat`.

With six pairs the percentile bootstrap interval is narrow and under-covers.
Read `excludes_1` as "B clearly differs from A", not as a bound.

`SMOKE=1` shrinks every size and stamps the output: it checks the harness
works, and its numbers are not figures.

## What the Go side compares to

Same inputs, same machine, same load shape:

| C reference | Go side to compare |
|---|---|
| `hmac_bench` faster form, `-l 200`, `ns_per_op` | the HS256 MAC step of the `security/token` verifier (`hmac.Reset`, `Write` of a ~200 B input, `Sum` into a `[32]byte`), 32-byte key, `go test -bench` on this VM |
| `parse_bench own`, `ns_per_op` per request | the `http1` codec parsing the bytes of `parse_bench stream` in a loop, pipelined, same refusals |
| `parse_bench pico` | context only |
| `echo_epoll` / `echo_uring`, `cpu_us_per_req` and `client_rps` | the loop engine's echo handler under `echo_client`, both backends, `cpu_us_per_req` from the cgroup (`systemd-run`) |

An echo server's `errors` counter includes sends that fail while the client
tears its connections down at the end of a run.
