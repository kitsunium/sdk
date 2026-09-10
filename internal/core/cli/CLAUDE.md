# internal/core/cli/

## Purpose

Declares the **command-line port**: `Action` (what a command does), `Binder`
(what declares its flags, onto the stdlib's own `*flag.FlagSet`), and
`Executor` (resolve an argument vector against a tree and run it), plus the
`CommandValue` / `InvocationValue` domain values and the typed declaration
sentinels. Admitted by **ADR 0065**. The engine, the help renderer and the
`config` adapter are concrete and live in `internal/service/cli`.

Code range: `0.2.32.*` (ADR 0065).

## Contents

| File | Surface |
|---|---|
| `cli.go` | `Action func(ctx, InvocationValue) error`, `Binder func(*flag.FlagSet)`, `Executor interface { Execute(ctx, []string) error }` |
| `cli_command.go` | `CommandValue` — `Name` / `Summary` / `Description` / `Flags` / `Run` / `Commands` + `IsGroup()` |
| `cli_invocation.go` | `InvocationValue` — `Path` / `Args` / `Flags` / `Output` + `Leaf()` |
| `codes.go` | `Code*` constants — range 0.2.32.* |
| `errors.go` | `InvalidCommand` / `AmbiguousCommand` / `DuplicateCommand` / `ReservedFlag` (`errs.Define`) |

## Conventions

- **`flag` is the substrate, not a detail to hide.** A `Binder` receives the
  stdlib's `*flag.FlagSet`, unchanged and unwrapped — the shape
  `internal/core/vfs` takes for `io/fs` (ADR 0056) and for the same reason. The
  flag syntax, `flag.Value`, every `FooVar` helper and `PrintDefaults` stay
  the stdlib's. This domain adds sub-commands, composed help, a typed exit
  status and the `config` seam, and nothing else.
- **`Action` and `Binder` are FUNC types, not interfaces.** ADR 0039 satisfied
  structurally: a published interface must not grow a method, and a func type
  **cannot**. `TestPortsAreFunctionsNotInterfaces` is the executable guard;
  `TestExecutorIsFrozenAtOneMethod` guards the one interface.
- **A command is a LEAF or a GROUP, never both and never neither.** Neither is
  the inert declaration ADR 0031 refuses. Both is worse: `tool db migrate`
  would mean "run db with the argument migrate" until somebody adds a child
  named `migrate`, and then the identical command line means something else —
  the same class of failure as prefix matching.
- **Nothing here can end the process.** No `os.Exit`, no `log.Fatal`, no
  `flag.ExitOnError`, in production code **and** in the suite. Pinned by
  `TestPackageNeverEndsTheProcess` in `internal/service/cli`, which audits all
  three `cli` packages through the AST.
- **Declaration refusals carry `EX_CONFIG` (78)**, deliberately apart from the
  `EX_USAGE` (64) `internal/service/cli` returns for a mistyped command line:
  one says main is wired wrong, the other says the operator mistyped, and a
  supervisor needs to tell them apart.
- **`-h` and `-help` belong to the domain.** `flag` reports an *undefined* `-h`
  as `flag.ErrHelp` rather than as a parse failure, and that is the only signal
  distinguishing a help request (exit 0) from a usage error (exit 64). A
  `Binder` that binds either is refused at construction.

## Do NOT

- **Add a method to `Executor`, or turn `Action` / `Binder` into interfaces.**
  `pkg/v1/cli` aliases all three, so the shape is published (ADR 0039). A new
  capability gets a sibling.
- **Wrap `*flag.FlagSet`.** A wrapper is a second contract to learn, to keep in
  sync with the stdlib, and to get subtly wrong — and it would break every
  `flag.Value` a caller already owns.
- **Add a `Signals` field, a shutdown budget, an env prefix or a config
  source here.** `lifecycle` (ADR 0050) and `config` (ADR 0028/0061) exist. A
  CLI that grew its own copy would be a second, weaker one.
- **Relabel an `Action`'s error.** It is returned verbatim so the caller's own
  `errors.Is` keeps working and origin-wins preserves its exit status; only a
  *panic* becomes a typed sentinel.
- **Add an abbreviation, prefix or case-insensitive match.** All three make
  what an existing command line MEANS depend on which siblings exist today.

## Verification

```
bazel test --config=race //internal/core/cli:cli_test
# OR
cd internal/core && GOWORK=off go test -race ./cli
```
