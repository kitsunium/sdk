# internal/service/cli/

## Purpose

The **command-line engine**: it validates a whole command tree once, resolves
an argument vector against it, renders the help the declarations imply, and
returns a typed error carrying an exit status. It also holds the one seam to
`config`. Admitted by **ADR 0065**; the ports and declaration sentinels live
in `internal/core/cli`.

Code range: `0.3.62.*` (ADR 0065).

## Contents

| File | Surface |
|---|---|
| `cli.go` | `New(Config, corecli.CommandValue) (corecli.Executor, error)` + the whole-tree validation + `newFlagSet` |
| `config.go` | `Config` — `Output` / `ErrOutput`, both zero-valued to `os.Stderr` |
| `execute.go` | `Execute` — the resolution loop, the parse verdict, the panic guard |
| `help.go` | the generated help: usage line, sub-command table, `PrintDefaults` |
| `flagsource.go` | `FlagSource(InvocationValue) *FlagSourceValue` — a `config.Source` over `flag.Visit` |
| `codes.go` | `Code*` constants — range 0.3.62.* |
| `errors.go` | `UnknownCommand` / `MissingCommand` / `InvalidFlags` / `CommandPanicked` (`errs.Define`) |
| `BENCH.md` | the numbers, and the conclusion that nothing here is worth optimising |

## Conventions

- **`flag.ContinueOnError`, always.** `newFlagSet` is the ONE place a
  `*flag.FlagSet` is constructed, and it is the only reason the "nothing here
  ends the process" claim is checkable in one place.
- **`flag` is kept silent.** The set's output is `io.Discard` for the whole of
  `Parse` and its `Usage` is a no-op, because on a failure `flag` writes its
  own message AND calls the usage function before returning the error. Left at
  its default the failure would be reported twice, in two vocabularies, one of
  which the caller cannot intercept. `writeFlags` points the output at the help
  buffer for exactly the duration of `PrintDefaults` and puts it back.
- **The engine writes the help; the caller writes the error.** One voice per
  artefact. The help is generated and only the SDK can render it; how an error
  is presented is the caller's (a line, a log record, JSON).
- **The WHOLE tree is validated at construction**, not the branch an invocation
  takes. Each `Binder` is CALLED once on a throwaway set — that is what proves
  it binds no reserved name — so a `Binder` must be safe to call more than
  once. A `Binder` that panics is deliberately **not** recovered: it runs
  inside the caller's own `main`, and a recovered panic there would replace a
  stack pointing at the bug with a sentence about a tree.
- **The only recovered panic is an `Action`'s.** A panicking Go program exits
  with status 2, which sysexits gives no meaning; recovering keeps the value
  AND the originating stack as fields and lets `main` decide. The recovered
  value is a FIELD and never the wrap origin, so a `panic(*errs.Error)` cannot
  hijack `COMMAND_PANICKED` — pinned by
  `TestAPanicCarryingAnErrsErrorCannotHijackTheCode`.
- **The engine is concurrency-safe; a `Binder`'s destination is not.** No
  per-invocation state is held here, but `fs.IntVar(&shared, …)` targets the
  caller's memory. The safe pattern — allocate per call, read back through
  `InvocationValue.Flags` — is what `TestTheEngineHoldsNothingPerInvocation`
  demonstrates.
- **`FlagSource` uses `Visit`, never `VisitAll`.** That one word is the whole
  adapter: `VisitAll` would contribute every DECLARED flag's Go default, so a
  zero-defaulted flag would override the file and the environment on every run
  and the configuration file would look ignored. The flag name is the config
  key verbatim — no case or separator rewriting, because a rename rule is a
  second grammar whose failure mode is a key that quietly matches nothing.

## Do NOT

- **Construct a `flag.FlagSet` outside `newFlagSet`.** `ExitOnError` and
  `PanicOnError` are refused by name and audited.
- **Call `os.Exit`, `log.Fatal*` or `runtime.Goexit`** — in production code or
  in the suite. `TestPackageNeverEndsTheProcess` fails the build on all of
  them, across all three `cli` packages.
- **Add an edit-distance suggester.** The help that has just been written
  already lists every name that would have worked: a complete answer that
  cannot be wrong and costs no table to maintain.
- **Wrap an `Action`'s error.** Origin-wins already preserves an `*errs.Error`;
  a plain stdlib error wrapped with empty `WrapParams` comes back as
  `INVALID_WRAP_PARAMS`, relabelling a command failure as an SDK defect.
- **Read files, environment variables or defaults here.** That is `config`.
  This package contributes one layer and stops.
- **Reimplement `PrintDefaults`.** It already knows how to render a
  `flag.Value`'s zero, how to pull a type name out of a backquoted usage
  string, and how to fold a multi-line usage.

## Verification

```
bazel test --config=race //internal/service/cli:cli_test
# OR
cd internal/service && GOWORK=off go test -race ./cli
# benchmarks (regenerates the numbers in BENCH.md)
cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./cli/
```
