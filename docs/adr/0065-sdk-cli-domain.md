# ADR 0065 — command-line domain (`cli`): what `flag` does not have, and the four things this adds instead of a framework

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0030](0030-stdout-is-a-protocol-channel.md) (no SDK default writes to `os.Stdout` — the rule that decides where the help goes), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero value is a safe default or an explicit refusal — the rule the leaf/group trichotomy comes from), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows by siblings; func ports satisfy it structurally), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (a published concrete shape may change while v0, out loud), [ADR 0056](0056-sdk-vfs-domain.md) (the "take the stdlib contract unchanged" precedent this copies), [ADR 0028](0028-sdk-config-domain.md) + [ADR 0061](0061-sdk-config-schema.md) (the configuration domain this feeds and does not duplicate), [ADR 0050](0050-sdk-lifecycle-domain.md) (ordered start/stop and opt-in signals — what this domain refuses to re-own), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) (`WithExitCode` / `ExitCodeOf`, the exit-status convention this uses rather than replaces)

## Context

Twenty-six domains ship and every one of them is reached from a `main` that
someone had to write. That `main` is where the SDK stops and where the same
four hundred lines get rewritten per tool: a `flag.FlagSet`, a `switch` over
`os.Args[1]`, a `usage()` function maintained by hand beside the flags it
describes, and an `os.Exit(1)` for everything that went wrong.

The stdlib's `flag` package is genuinely good at the one thing it does. It
parses flags: `-x=v`, `-x v`, `--x`, a `Value` interface any type can
implement, a defaults table that already knows how to render a backquoted type
name and fold a multi-line usage. Nothing in this ADR reimplements one byte of
it, and the alternatives that would — `cobra`, `urfave/cli`, `kingpin` — are
refused on the doctrine that governs this SDK: an argument parser is a
**mechanism**, not a connector to somebody else's system, and mechanisms are
written here. `cobra` alone brings `pflag`, `viper`'s orbit, and a `Command`
struct with thirty-odd fields, of which this domain needs six.

What `flag` does not have is exactly four things, and the boundary matters
more than the list:

1. **Sub-commands.** `flag` parses one flat set. A tree is the caller's
   problem, and the hand-rolled version is a `switch` that no help text knows
   about.
2. **Composed help.** `flag.PrintDefaults` renders one set's flags. Which
   sub-commands exist, what each does, and what the usage line should say are
   nowhere in the package, so they are written by hand — beside the
   declarations, and out of date one rename later.
3. **A typed exit status.** `flag`'s own answer is `flag.ExitOnError`:
   `os.Exit(2)` from inside a library. Everything else exits 1 because that is
   what people type.
4. **Any connection to the rest of the SDK.** A real CLI reads configuration
   and shuts down in order. Both domains exist here already.

Two mechanics also make this worth a domain rather than a snippet:

- **`flag` stops at the first non-flag argument.** That rule, which exists for
  other reasons, is precisely what makes sub-command resolution unambiguous
  with no lookahead, no two-pass parse and no re-ordering. A domain built on
  it inherits a property the hand-rolled `switch` has to invent.
- **`flag` reports an *undefined* `-h` as `flag.ErrHelp`, not as a parse
  failure.** That is the only signal in the whole package separating "the
  operator asked a question" from "the operator got it wrong" — and it is the
  signal the exit status has to be built on.

## Decision

Add `cli` as a core sibling: `internal/core/cli` (`0.2.32.*`),
`internal/service/cli` (`0.3.62.*`), `pkg/v1/cli`. Built on the stdlib `flag`
package, with **zero** third-party dependencies. **No registry.**

---

### D1 — `flag` is the substrate, taken unchanged, and the boundary is stated

A `Binder` receives the standard library's own `*flag.FlagSet`:

```go
type Binder func(flags *flag.FlagSet)
```

Not a wrapper, not a `cli.FlagSet`, not a `cli.Value`. This is ADR 0056's
decision applied one domain over: `vfs`'s read half is `io/fs` *unchanged*,
because a wrapper around a stdlib contract is a second contract to learn, to
keep in sync, and to get subtly wrong. Here the cost of wrapping would be
paid immediately and by everybody: every `FooVar` helper, every `flag.Value` a
consumer already owns, and every `flag.Getter` would need an adapter that
exists only so the SDK could say it owned the type.

So the domain adds four things and touches nothing else:

| | belongs to | this domain |
|---|---|---|
| flag syntax (`-x=v`, `-x v`, `--x`, `--`) | `flag` | — |
| `flag.Value` / `flag.Getter` | `flag` | — |
| the defaults table | `flag.PrintDefaults` | — |
| "stop at the first non-flag argument" | `flag` | **relied on** (D3) |
| sub-commands | — | **yes** |
| composed help | — | **yes** |
| typed exit status | `errs` | **routed** (D2) |
| flags as a config layer | `config` | **adapted** (D6) |

The one place the domain constructs a `*flag.FlagSet` is `newFlagSet`, and it
is the only place, so D2's central claim is checkable by reading one function.

---

### D2 — `flag.ExitOnError` is refused by name; nothing in this domain can end the process

`flag.ExitOnError` calls `os.Exit` from inside a library. That is three
separate defects wearing one name:

- **it makes the parse untestable** — a test that provokes a bad flag ends the
  test binary, so the failure is reported as "the package crashed" rather than
  as the assertion it was;
- **it skips every deferred function in the program** — no flush, no
  `Close`, no unlock;
- **it takes a decision that belongs to `main`** — whether this process should
  still be alive is not a parser's call, and it is not available to a library
  that may be running inside a REPL, a test harness or a supervisor.

`flag.PanicOnError` is refused with it: a panic crossing a library boundary is
`os.Exit` with a corrupted stack and a `recover` in somebody else's code.

Every set is `flag.ContinueOnError`, every failure is a returned `*errs.Error`,
and `main` is the only place that may act on it.

**Enforcement is mechanical, not documentary.** `TestPackageNeverEndsTheProcess`
parses all three `cli` packages through the AST and fails on `os.Exit`,
`syscall.Exit`, `runtime.Goexit`, the `log.Fatal*` / `log.Panic*` family, and
on `flag.ExitOnError` / `flag.PanicOnError` appearing anywhere. It reads the
sources through the AST rather than by grepping, so the forbidden identifiers
can be written in the audit's own tables and prose. It covers the **suite** as
well as the production code, for a reason specific to test binaries: a test
that ends the process takes every other test with it.

`TestTheExitAuditDetectsAViolation` writes a two-violation fixture and asserts
the audit finds exactly two — because a green audit proves nothing until it has
been shown to fail on the thing it claims to catch (the same reason
`internal/kernel/errs` runs its registry audits against fixtures).

**`flag` is also kept silent.** This is a second, less obvious half of the same
decision. On a parse failure `flag` writes its own message *and* calls the
usage function **before** returning the error:

```go
func (f *FlagSet) failf(format string, a ...any) error {
	err := f.sprintf(format, a...)   // → f.Output()
	f.usage()                        // → f.Output()
	return err
}
```

A set left at its default would therefore report every failure twice — once in
`flag`'s words, to a stream the caller cannot intercept, and once through the
returned error. So `newFlagSet` sets the output to `io.Discard` and installs a
no-op `Usage`. `flag`'s own text is not lost: it travels in a field and in
`Private`, never in `Public`, because it quotes the operator's value verbatim.

This also answers the ADR 0030 question directly. `flag.FlagSet.Output()`
returns `os.Stderr` when unset — which happens to be safe — but
`SetOutput` can point it anywhere, and the package-level `flag.PrintDefaults`
writes to `flag.CommandLine`'s. Rather than depend on a default, the domain
sets the destination explicitly at every point: `io.Discard` while parsing, the
help buffer for exactly the duration of `PrintDefaults`, and back.

---

### D3 — Sub-commands: an arbitrary tree, exact-match only, and no suggester

**Depth is unbounded.** One level would be a bound every real tool breaks on
its second release (`git remote add`, `kubectl config set-context`), and the
resolution loop costs the same either way — the bound would be a restriction
the mechanism does not impose. Measured: three levels cost 2.8× one level, and
all of it is the two extra `flag.FlagSet`s; the linear scan over nine sibling
names does not register (`internal/service/cli/BENCH.md`).

The resolution loop is the whole algorithm, and it borrows `flag`'s own rule
rather than inventing one:

```
parse this command's flags → flag stops at the first non-flag token
  leaf  → that token and the rest are positional arguments
  group → that token IS the sub-command's name; recurse on the rest
```

`tool --verbose db migrate --dry-run` therefore needs no lookahead: `--verbose`
belongs to the group that declared it and `--dry-run` to the leaf that declared
it, because each set stopped exactly where the next command began.

**Names are compared by exact byte equality.** No prefix matching, no
abbreviation, no case folding. All three make what an existing command line
MEANS depend on which siblings exist in today's release: `tool st` resolves to
`status` until somebody adds `start`, and then a script that worked for two
years resolves to nothing — in a release that changed no line of `status`'s own
code.

**There is no "did you mean …?".** This is the decision with two defensible
answers, so here is the argument for the one taken. An unknown command already
writes the group's help, and that help **lists every name that would have
worked**. That listing is a complete answer, it cannot be wrong, and it costs
no edit-distance table to keep correct. A suggestion is a second answer beside
the first: it has to be tuned (what threshold? is `push` a suggestion for
`rm`?), it has to be maintained as the tree grows, and it is wrong in exactly
the situation where the operator is least able to tell — a typo that happens to
be close to a *destructive* command. The alternative was considered and is
recorded under "Why not" rather than left silent.

---

### D4 — The help is generated, and asking for it is not a failure

The help is rendered from the **same declarations the resolver walks**: the
summaries, the sub-command list, and `flag.PrintDefaults` over the set the
`Binder` just filled. It is never written by hand.

This is CLAUDE.md rule 11 applied to the executable. A help text maintained
beside the declarations eventually documents a flag that was renamed or a
sub-command that was removed — and unlike a stale comment, **an operator acts
on it**. Generation makes "the help cannot lie" a property of the mechanism.
`TestHelpNamesEveryDeclaredCommandAndFlag` walks the declarations and asserts
each appears, rather than comparing against a golden string somebody would
update to match a regression.

`PrintDefaults` is used rather than reimplemented, for the D1 reason: it
already knows how to render a `flag.Value`'s zero, how to pull a type name out
of a backquoted usage string, and how to fold a multi-line usage. A
hand-written renderer would be a second one that disagrees with the stdlib the
first time a caller uses an idiom it had not heard of.

**The exit status is what separates a question from a mistake.**

| what happened | help written? | status |
|---|---|---|
| `-h` / `-help` / `--help` | yes | **0** |
| unknown sub-command | yes | 64 |
| no sub-command under a group | yes | 64 |
| a flag `flag` refused | yes | 64 |

All four write the **same** page. The text does not have to distinguish them,
because the status already does — and the status is the thing a script reads.
`-h` returning nil is not a convenience: `flag` hands back `ErrHelp`, which
*is* an error value, and treating it as one would make `tool --help` exit
non-zero, which breaks every `--help` smoke test in every CI system.

**The engine writes the help; the caller writes the error.** One voice per
artefact. The help is generated and only the SDK can render it; how an error is
presented — a line on stderr, a log record, JSON — is the caller's, and two
voices printing one failure is how a CLI ends up saying it twice.

---

### D5 — The zero value: a command is a leaf **or** a group, never both and never neither

ADR 0031's question, asked of `CommandValue`: what does a command with no `Run`
mean?

Both readings are defensible in isolation, which is exactly the condition ADR
0031 says must not be resolved by silence. The decision:

| `Run` | `Commands` | verdict |
|---|---|---|
| set | empty | **leaf** |
| nil | non-empty | **group** |
| nil | empty | **refused** — `INVALID_COMMAND` |
| set | non-empty | **refused** — `AMBIGUOUS_COMMAND` |

**Neither** is the inert declaration ADR 0031 exists for: a name an operator
can type that does nothing, reports success while doing it, and is discovered
by somebody wondering why the tool did not do the thing.

**Both** is the more interesting refusal, and it is not fastidiousness. With
both, `tool db migrate` means "run `db` with the positional argument `migrate`"
— until somebody adds a child named `migrate`, at which point the identical
command line means something else, in a release that changed no line of `db`'s
own code. A declaration whose meaning depends on which siblings exist today is
the same failure class as prefix matching, and it is refused for the same
reason.

A **group invoked with no sub-command** is a failure (`MISSING_COMMAND`, 64)
and not a quiet success: a group declares no action, so `tool db` did nothing,
and a script reading exit status 0 there would treat "I forgot the verb" as
"the migration ran".

Three more clauses, each mechanically derived rather than stylistic:

- a name beginning with `-` is refused, because the parent's flag parse would
  consume it as a flag and stop before the resolver ever saw it — it could
  never be typed;
- a name containing whitespace is refused, because the resolver is handed one
  token;
- a `Binder` that binds `h` or `help` is refused, because `flag` only reports
  `ErrHelp` for an *undefined* `-h`; a command that defines one silently turns
  a help page into a successful parse and takes D4's whole distinction away.

**The WHOLE tree is validated at construction**, not the branch an invocation
happens to take. A declaration is a wiring fact, and a wiring fault found by
the operator who typed the one command nobody had tried is a fault found in
production by somebody who cannot fix it. This requires **calling** each
`Binder` once on a throwaway set — a declaration cannot be inspected without
executing it — so a `Binder` must be safe to call more than once, which is
stated on the port and pinned by a test.

A `Binder` that **panics** is deliberately **not** recovered. It runs inside
the caller's own `main`, on the line that wrote it, and a recovered panic there
would replace a stack pointing at the bug with a sentence about a tree. The one
panic this domain recovers is an `Action`'s — see D7.

Refusals here carry **EX_CONFIG (78)**, deliberately apart from the EX_USAGE
(64) a mistyped command line gets: one says `main` is wired wrong and will be
refused identically forever, the other says the operator mistyped, and a
supervisor that restarts on one and reports on the other needs them to differ.

---

### D6 — What `cli` composes, and what it refuses to reimplement

This is the ticket's stated principal risk, so it is answered as a list rather
than as prose.

**`config` (ADR 0028 / 0061) — composed through exactly one adapter.**

`cli.FlagSource(invocation)` yields the flags an operator **typed**, as a
`config.Source`. That is the entire integration. It reads no file, no
environment variable and no default; it does no layering, no decoding and no
validation. The intended shape is one line:

```go
err := config.LoadSchema(&cfg, schema,
    config.FileSource("toml", "/etc/tool.toml"),
    config.EnvSource("TOOL_"),
    cli.FlagSource(invocation))   // last wins
```

Two decisions inside that adapter carry it:

- **`Visit`, never `VisitAll`.** `VisitAll` yields every *declared* flag,
  including the ones nobody passed, carrying their Go defaults — so in the
  last-layer position that "flags win" requires, every zero-defaulted flag
  would override the file and the environment on every run, and the
  configuration file would appear to be ignored for reasons nothing reports.
  This is the single most common defect in a flags-plus-config integration and
  it is one identifier wide.
- **The flag name IS the config key**, verbatim. No camelCase-to-snake_case, no
  `-` to `_`, no automatic prefixing. Every such rule is a second grammar a
  reader must learn and a generator must reproduce, whose failure mode is a key
  that quietly matches nothing. `flag` accepts a dot in a name, so a flag that
  feeds `database.max_conns` declares itself as `database.max_conns` — and when
  the name *is* wrong, ADR 0061's schema **refuses** an unaddressed key rather
  than ignoring it, so the mistake is loud at startup.

The claim is enforced rather than asserted: `pkg/v1/cli` carries
`var _ coreconfig.Source = (*FlagSourceValue)(nil)`, and
`TestFlagSourceIsTheLastLayerOfAConfigLoad` runs the composition end to end
through the public `config` API.

**`lifecycle` (ADR 0050) — composed by the caller, with nothing added here.**

`cli` has **no** `Signals` field, no shutdown budget, no `sd_notify`, no
`RunConfig`. A long-running command composes `lifecycle.Run` **inside its own
`Action`**, where the `context.Context` it needs already is. The reason is ADR
0050's own: signals are a process-wide, observable side effect, and a library
that installs a handler because it was constructed fights the caller's `main`.
ADR 0050 already made them opt-in for that exact reason; a second, implicit
opt-in one layer up would undo it.

**`errs` (ADR 0005) — the exit-status convention, used and not replaced.**

A sentinel declares its status with `errs.WithExitCode`; a caller reads it with
`errs.ExitCodeOf`. This domain invents no second convention. It adds exactly
one function, and only because of a real trap:

```go
func Status(err error) int   // 0 for nil, errs.ExitCodeOf(err) otherwise
```

`errs.ExitCodeOf(nil)` is **70**, and correctly so — it answers "what status
does THIS ERROR map to", and nil is not an error. But at a CLI boundary success
is the common case, so `os.Exit(errs.ExitCodeOf(app.Execute(…)))` would fail
every successful run of every tool built on this domain. This is the one place
in the SDK where nil must mean 0, so this is where the guard lives.
`TestStatusIsZeroForSuccess` asserts the premise (`ExitCodeOf(nil) == 70`) as
well as the behaviour, so if `errs` ever changed its mind the failing test is
the one whose reason is written down.

**`logger`, `validation`, `trace` — nothing.** `cli` writes to the two
`io.Writer`s its `Config` was given and holds no logger. A command that wants
one constructs it.

---

### D7 — The `Action`'s error is verbatim; the `Action`'s panic is not

**Verbatim** is the decision, and it has three reasons rather than one. Origin-
wins (CLAUDE.md rule 6) already preserves an `*errs.Error`'s code and exit
status through a wrap, so wrapping adds nothing; a plain stdlib error wrapped
with empty `WrapParams` would come back as `INVALID_WRAP_PARAMS`, relabelling a
command failure as an SDK defect; and any wrap breaks the caller's own
`errors.Is` against their own sentinel. It is the same rule
`internal/service/lifecycle` applies to a component's error.

The consequence is the one that makes the domain useful: **a command's own exit
status is never overwritten by the framework that called it.**

A **panic** in an `Action` IS recovered, and the reason is not "hiding the
bug". A panicking Go program exits with status **2**, which sysexits assigns no
meaning and which several supervisors read as a usage error. Recovering loses
nothing — the recovered value AND `debug.Stack()` **captured inside the
deferred function**, so it is the stack of the goroutine that actually failed,
both travel as fields — and what changes is that `main`, rather than the
runtime, decides what the process does next. It keeps the default EX_SOFTWARE
(70): the command line was fine.

The recovered value is a **field** and never the wrap origin, so a
`panic(*errs.Error)` cannot hijack `COMMAND_PANICKED` — including with a
sentinel whose exit status is 0. That case has its own named test.

---

### D8 — Output: two writers, both defaulting to `os.Stderr`

ADR 0030 makes stdout a protocol channel no SDK default may claim, and a zero
value is the choice made by somebody who has not yet learned the question
exists — so it must not be the dangerous one.

- `Config.Output` — the command's own output, handed to the `Action` as
  `Invocation.Output`. Zero → `os.Stderr`.
- `Config.ErrOutput` — the help and the usage after a bad command line. Zero →
  `os.Stderr`.

Two writers rather than one, so a tool that emits a machine-readable document
sets `Output: os.Stdout` in `main` — one visible line, at the one place that
can honestly make the choice — and its help still stays on stderr where it
cannot corrupt the document a consumer is parsing.

An `Action` is *handed* the writer precisely so it never has to reach for a
process stream itself.

---

### D9 — No registry

The same argument `proc` (ADR 0016), `resilience` (ADR 0026), `scheduler`
(ADR 0041) and `lifecycle` (ADR 0050) each made: there is one engine and one
command tree per process, so a registry would have exactly one entry and would
add a way to misconfigure at run time something the compiler already checks.

---

## Consequences

**Positive**

- A `main` is six lines, and the four hundred it replaces were the four hundred
  most likely to contain an `os.Exit` in the wrong place.
- The help cannot drift from the declarations, because there is only one
  declaration.
- The exit status is routable and the two failure families (78 wiring, 64
  usage) are distinguishable by a supervisor.
- A mis-wired tree fails on the first run of the binary, not on the first run
  of the one command nobody tried.
- Nothing the SDK ships can end a consumer's process — enforced across three
  packages and their suites by an AST audit that has been shown to fail.

**Negative, stated rather than hidden**

- **The engine is concurrency-safe; a `Binder`'s destination is not.** No
  per-invocation state is held, but `fs.IntVar(&shared, …)` targets the
  caller's memory, and two concurrent `Execute` calls write it. The SDK cannot
  fix that from here without taking a lock around memory it does not own, for a
  duration it cannot know. The safe pattern — allocate per call, read back
  through `Invocation.Flags` — is documented on `Binder` and demonstrated by
  `TestTheEngineHoldsNothingPerInvocation`.
- **A `Binder` runs at construction.** Twice per process in total, on a
  throwaway set and then on the real one. A `Binder` with side effects beyond
  the set it is given will perform them twice.
- **`Command` is 88 bytes and is passed by value**, once per command per
  process. Measured at 2.6 % of the time it takes to start a Go binary that
  does nothing.
- **No completion, no colour, no interactive prompt, no per-flag environment
  fallback.** Named under "Why not".

## Why not …

**… `cobra` / `urfave/cli` / `kingpin`?** An argument parser is a mechanism,
not a connector to a third-party system, and this SDK writes its mechanisms
(the same call `metrics` made for the OTel data model and `net` for RFC 6455).
The concrete cost is also real: `cobra` brings `pflag` and a `Command` struct
with thirty-odd fields, of which this domain needs six, and its default
`RunE`-less command prints help and exits 0 — the "quiet success" D5 refuses.

**… GNU-style interspersed flags (`tool sub arg --flag`)?** That requires
abandoning `flag`'s "stop at the first non-flag argument" rule, which is
precisely what makes sub-command resolution unambiguous with no lookahead. The
cost of keeping it is that a flag must precede the positional arguments of the
command that declared it; the cost of dropping it is a two-pass parse whose
result depends on which tokens happen to look like flags.

**… an edit-distance suggester?** See D3. The complete answer is already on the
screen. Reconsider if a tool with more than ~30 sibling commands ships and the
help becomes too long to scan — at which point the right fix is probably
grouping, not guessing.

**… shell-completion generation?** It is a real feature and a real amount of
code (three shells, three grammars, each with its own quoting rules), and it
would have to be regenerated and reinstalled out of band. Deferred by name, not
forgotten: the declaration tree already contains everything a generator needs,
which is the point of D4.

**… a `-h` that prints to stdout?** Conventional, and refused. ADR 0030 does
not carve out an exception for "but the operator asked for it": the SDK does
not pick stdout, the caller does, and a caller who wants help on stdout writes
`ErrOutput: os.Stdout`.

**… reading `os.Args` inside `Execute`?** A library that reads the process's
own arguments cannot be tested, cannot be embedded, and cannot be run twice.
`main` passes `os.Args[1:]`, which is one visible line.

**… recovering a `Binder` panic like an `Action` panic?** D5. The two happen at
different times to different people: a `Binder` panic is in `main`'s own
construction, where the stack is the answer; an `Action` panic is in the middle
of the work, where the exit status is.

## What this domain does NOT guarantee

- **Mutual exclusion between concurrent invocations sharing a `Binder`
  destination.** See Consequences. This is a property of the caller's
  variables, not of the engine.
- **That the caller ordered a tree sensibly.** As with ADR 0050's Add order,
  the SDK has nothing to check the declaration against.
- **That a command's own error carries a useful exit status.** A plain stdlib
  error maps to EX_SOFTWARE (70) because that is `errs`'s default; a command
  that wants a specific status declares an `errs` sentinel with
  `WithExitCode`. The SDK will not invent one on its behalf.
- **Anything about what an `Action` does with the context.** No deadline is
  added here.

## Error codes

| Code | Reason | Layer | Exit |
|---|---|---|---|
| `0.2.32.1` | `INVALID_COMMAND` | core | 78 |
| `0.2.32.2` | `AMBIGUOUS_COMMAND` | core | 78 |
| `0.2.32.3` | `DUPLICATE_COMMAND` | core | 78 |
| `0.2.32.4` | `RESERVED_FLAG` | core | 78 |
| `0.3.62.1` | `UNKNOWN_COMMAND` | service | 64 |
| `0.3.62.2` | `MISSING_COMMAND` | service | 64 |
| `0.3.62.3` | `INVALID_FLAGS` | service | 64 |
| `0.3.62.4` | `COMMAND_PANICKED` | service | 70 |

## Measurements

Full report: `internal/service/cli/BENCH.md`. The conclusion is that **nothing
in this domain is worth optimising**, and it is stated with the numbers rather
than with the intuition that parsing happens once:

- one leaf costs **+3 allocations and +40 B** over `flag` doing the same parse
  alone — stable across every run, unlike the nanosecond delta, which sits
  inside the control's own ±9 % spread;
- `flag` itself is 21 of the 24 allocations;
- three levels of nesting cost 3.0× one level, all of it the two extra flag
  sets: the sibling-name scan does not register;
- `New` over a 27-command tree is 52 µs, of which **76.8 % is `flag` binding
  flags** (memory profile, `alloc_objects`) — work the caller pays at parse
  time anyway; this domain's own share is ~13 %, about three allocations per
  command;
- the whole domain, once, is **2.9 % of the 1.99 ms** it takes to start and
  exit a Go binary containing an empty `main`.

The optimisation this design invites — skipping the construction-time `Binder`
probe, worth ~40 µs and 77 % of `New`'s allocations — is **refused** and priced:
2 % of an empty process start, in exchange for whole-tree validation and the
`-h` collision check.

## References

- `internal/core/cli/` — the ports, the two values, the declaration sentinels
- `internal/service/cli/` — the engine, the help, the config adapter, `BENCH.md`
- `pkg/v1/cli/` — the public facade, `Status`, `FlagSource`
- `TestPackageNeverEndsTheProcess` — the executable form of D2
- Go standard library, `flag` package — `ErrHelp`, `failf`, `ContinueOnError`
