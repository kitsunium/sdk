<!-- generated from internal/kernel/errs/*_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./errs/` to refresh -->
# Benchmarks — `internal/kernel/errs`

The SDK-wide typed-error package: dotted-quad `Code` (ADR 0005), the `*Error`
value with its Public/Private split and wrap trail, the `FieldValue` carrier,
the `PrefixMatcher` subnet router, and the read-only accessors (`CodeOf`,
`ReasonOf`, …). Every error in the SDK flows through here, so the surface splits
into two performance tiers these benchmarks lock in:

- **Zero-alloc hot paths** — `Code` bit-twiddling (`Pack`, `Major`/`Layer`/
  `Package`/`Serial`), the `*Error` field accessors (`Code`, `Reason`, `Public`,
  `Private`, `Source`, `Unwrap`, `HTTPStatus`, `ExitCode`), `HasCode`,
  `errors.Is` against a prefix/sentinel/pointer, the typed `Field*` constructors,
  and `PrefixMatcher` `Prefix`/`Mask`. All **0 allocs/op** — the contract a hot
  logging path depends on.
- **Bounded-alloc construction & rendering** — `Define`/`NewError`/`NewRuntime`/
  `Wrap` allocate the heap `*Error` (1 alloc; `Wrap` with a cause/fields adds a
  few more), and the string renderers (`Code.String`, `Code.Padded`,
  `Error.Error`, `PrefixMatcher.String`/`Error`, `ParseCode` on the invalid
  path) allocate their result strings. The numbers document that cost so a
  regression that turns a hot accessor into an allocating call is visible.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 11 GiB |
| OS / kernel        | Linux 6.12.72-linuxkit |
| Architecture       | arm64 |
| Go toolchain       | go1.26.4 linux/arm64 |
| Git branch         | feat/issue-17-kernel-errs-bench |
| Git commit         | f3b1610 |
| Generated (UTC)    | 2026-06-19 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
goos: linux
goarch: arm64
pkg: github.com/kitsunium/sdk/internal/kernel/errs
BenchmarkCodeOf-8                          	174567852	         6.866 ns/op	       0 B/op	       0 allocs/op
BenchmarkCodeOf_Parallel-8                 	23230489	        43.76 ns/op	       0 B/op	       0 allocs/op
BenchmarkReasonOf-8                        	140477662	         8.213 ns/op	       0 B/op	       0 allocs/op
BenchmarkReasonOf_Parallel-8               	21424585	        56.73 ns/op	       0 B/op	       0 allocs/op
BenchmarkPublicOf-8                        	166904718	         7.209 ns/op	       0 B/op	       0 allocs/op
BenchmarkPublicOf_Parallel-8               	65529159	        20.79 ns/op	       0 B/op	       0 allocs/op
BenchmarkPrivateOf-8                       	158231680	         7.523 ns/op	       0 B/op	       0 allocs/op
BenchmarkPrivateOf_Parallel-8              	62388438	        22.50 ns/op	       0 B/op	       0 allocs/op
BenchmarkFieldsOf-8                        	184098621	         6.476 ns/op	       0 B/op	       0 allocs/op
BenchmarkFieldsOf_Parallel-8               	65643416	        18.74 ns/op	       0 B/op	       0 allocs/op
BenchmarkHTTPStatusOf-8                    	159004490	         7.510 ns/op	       0 B/op	       0 allocs/op
BenchmarkHTTPStatusOf_Parallel-8           	59106262	        17.68 ns/op	       0 B/op	       0 allocs/op
BenchmarkExitCodeOf-8                      	158743946	         7.525 ns/op	       0 B/op	       0 allocs/op
BenchmarkExitCodeOf_Parallel-8             	64908135	        19.00 ns/op	       0 B/op	       0 allocs/op
BenchmarkHasReason-8                       	154173368	         7.780 ns/op	       0 B/op	       0 allocs/op
BenchmarkHasReason_Parallel-8              	74313372	        18.60 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrailOf-8                         	50013284	        21.16 ns/op	       8 B/op	       1 allocs/op
BenchmarkTrailOf_Parallel-8                	19326951	        55.13 ns/op	       8 B/op	       1 allocs/op
BenchmarkPack-8                            	518663886	         2.338 ns/op	       0 B/op	       0 allocs/op
BenchmarkPack_Parallel-8                   	1000000000	         1.376 ns/op	       0 B/op	       0 allocs/op
BenchmarkCode_Major-8                      	540277904	         2.213 ns/op	       0 B/op	       0 allocs/op
BenchmarkCode_Layer-8                      	550679638	         2.190 ns/op	       0 B/op	       0 allocs/op
BenchmarkCode_Package-8                    	546111429	         2.212 ns/op	       0 B/op	       0 allocs/op
BenchmarkCode_Serial-8                     	507824284	         2.244 ns/op	       0 B/op	       0 allocs/op
BenchmarkCode_String-8                     	22027071	        53.00 ns/op	       8 B/op	       1 allocs/op
BenchmarkCode_String_Parallel-8            	29577624	        38.12 ns/op	       8 B/op	       1 allocs/op
BenchmarkCode_Padded-8                     	 8201974	       158.8 ns/op	      28 B/op	       5 allocs/op
BenchmarkCode_Padded_Parallel-8            	16390616	        74.89 ns/op	      28 B/op	       5 allocs/op
BenchmarkDefine-8                          	 8857024	       113.2 ns/op	     144 B/op	       1 allocs/op
BenchmarkDefine_WithOptions-8              	11215411	       111.2 ns/op	     144 B/op	       1 allocs/op
BenchmarkNewError-8                        	10392980	       121.4 ns/op	     144 B/op	       1 allocs/op
BenchmarkNewRuntime-8                      	10304482	       114.6 ns/op	     144 B/op	       1 allocs/op
BenchmarkWrap_Stdlib-8                     	 9171638	       138.4 ns/op	     144 B/op	       1 allocs/op
BenchmarkWrap_SDKCause-8                   	 9150237	       111.3 ns/op	     152 B/op	       2 allocs/op
BenchmarkWrap_WithFields-8                 	 6300505	       196.3 ns/op	     280 B/op	       3 allocs/op
BenchmarkError_Code-8                      	545592698	         2.218 ns/op	       0 B/op	       0 allocs/op
BenchmarkError_Code_Parallel-8             	1000000000	         1.219 ns/op	       0 B/op	       0 allocs/op
BenchmarkError_Reason-8                    	540086719	         2.222 ns/op	       0 B/op	       0 allocs/op
BenchmarkError_Public-8                    	540286315	         2.238 ns/op	       0 B/op	       0 allocs/op
BenchmarkError_Private-8                   	543496207	         2.253 ns/op	       0 B/op	       0 allocs/op
BenchmarkError_Fields-8                    	26955766	        47.82 ns/op	      64 B/op	       1 allocs/op
BenchmarkError_HTTPStatus-8                	530666732	         2.246 ns/op	       0 B/op	       0 allocs/op
BenchmarkError_ExitCode-8                  	536808571	         2.220 ns/op	       0 B/op	       0 allocs/op
BenchmarkError_Error_NoTrail-8             	11092494	       106.8 ns/op	      56 B/op	       2 allocs/op
BenchmarkError_Error_NoTrail_Parallel-8    	17929916	        71.87 ns/op	      56 B/op	       2 allocs/op
BenchmarkError_Error_Trail-8               	 7877096	       149.7 ns/op	      96 B/op	       3 allocs/op
BenchmarkError_Source-8                    	542012050	         2.221 ns/op	       0 B/op	       0 allocs/op
BenchmarkError_Unwrap-8                    	536430822	         2.221 ns/op	       0 B/op	       0 allocs/op
BenchmarkHasCode-8                         	168121873	         7.135 ns/op	       0 B/op	       0 allocs/op
BenchmarkHasCode_Parallel-8                	85382919	        14.83 ns/op	       0 B/op	       0 allocs/op
BenchmarkHasCode_Trail-8                   	411581562	         2.704 ns/op	       0 B/op	       0 allocs/op
BenchmarkError_Is_Prefix-8                 	540866095	         2.220 ns/op	       0 B/op	       0 allocs/op
BenchmarkError_Is_Sentinel-8               	307404969	         3.919 ns/op	       0 B/op	       0 allocs/op
BenchmarkError_Is_Pointer-8                	506563603	         2.392 ns/op	       0 B/op	       0 allocs/op
BenchmarkFieldString-8                     	390521659	         2.996 ns/op	       0 B/op	       0 allocs/op
BenchmarkFieldInt-8                        	566064661	         2.132 ns/op	       0 B/op	       0 allocs/op
BenchmarkFieldInt64-8                      	564327782	         2.111 ns/op	       0 B/op	       0 allocs/op
BenchmarkFieldBool-8                       	566224362	         2.308 ns/op	       0 B/op	       0 allocs/op
BenchmarkFieldFloat-8                      	527047993	         2.224 ns/op	       0 B/op	       0 allocs/op
BenchmarkNewFieldValue-8                   	397469444	         3.065 ns/op	       0 B/op	       0 allocs/op
BenchmarkFieldValue_Key-8                  	559232965	         2.165 ns/op	       0 B/op	       0 allocs/op
BenchmarkFieldValue_StringValue_String-8   	495368304	         2.411 ns/op	       0 B/op	       0 allocs/op
BenchmarkFieldValue_StringValue_Int-8      	43326969	        23.96 ns/op	       8 B/op	       1 allocs/op
BenchmarkFieldValue_StringValue_Float-8    	19372426	        62.36 ns/op	       8 B/op	       1 allocs/op
BenchmarkParseCode-8                       	48863665	        24.92 ns/op	       0 B/op	       0 allocs/op
BenchmarkParseCode_Parallel-8              	40028853	        32.07 ns/op	       0 B/op	       0 allocs/op
BenchmarkParseCode_Invalid-8               	 6121486	       174.3 ns/op	     192 B/op	       2 allocs/op
BenchmarkNewPrefixMatcher-8                	100000000	        10.80 ns/op	       8 B/op	       1 allocs/op
BenchmarkPrefixMatcher_Prefix-8            	541062511	         2.220 ns/op	       0 B/op	       0 allocs/op
BenchmarkPrefixMatcher_Prefix_Parallel-8   	1000000000	         1.190 ns/op	       0 B/op	       0 allocs/op
BenchmarkPrefixMatcher_Mask-8              	539237482	         2.216 ns/op	       0 B/op	       0 allocs/op
BenchmarkPrefixMatcher_Mask_Parallel-8     	1000000000	         1.202 ns/op	       0 B/op	       0 allocs/op
BenchmarkPrefixMatcher_String-8            	 6944713	       175.0 ns/op	      72 B/op	       4 allocs/op
BenchmarkPrefixMatcher_String_Parallel-8   	12915000	        93.47 ns/op	      72 B/op	       4 allocs/op
BenchmarkPrefixMatcher_Error-8             	 7394191	       164.1 ns/op	      72 B/op	       4 allocs/op
PASS
ok  	github.com/kitsunium/sdk/internal/kernel/errs	92.161s
```

## How to read this

- **Accessors at ~2 ns / 0 allocs** (`Error_Code`, `Error_Reason`, `Pack`,
  `Field*`, `Error_Is_*`, `PrefixMatcher_Prefix`/`Mask`) — pure field reads / bit
  ops on an already-built value. The 0 allocs/op figure is the contract; any
  regression to > 0 allocs is a bug.
- **Typed-accessor helpers at ~7 ns / 0 allocs** (`CodeOf`, `ReasonOf`,
  `HasCode`, …) — one `errors.As`/type-assertion walk plus the field read; the
  `_Parallel` variants show the small contention overhead of concurrent walks.
- **Construction at ~110–200 ns / 1+ allocs** (`Define`, `NewError`,
  `NewRuntime`, `Wrap_*`) — the heap `*Error` is the unavoidable allocation;
  `Wrap` with an SDK cause or fields adds the trail/field slices. This is the
  floor a call site pays to *create* an error, not to inspect one.
- **String rendering / formatting allocate** (`Code_String`, `Code_Padded`,
  `Error_Error_*`, `PrefixMatcher_String`/`Error`, `ParseCode_Invalid`,
  `FieldValue_StringValue_*`, `TrailOf`) — they build result strings/slices, so
  non-zero B/op and allocs/op are expected and documented here rather than hidden.
