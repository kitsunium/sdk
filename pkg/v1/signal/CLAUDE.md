# pkg/v1/signal

Public, stable facade for the typed **signal toolbox**: parse, subscribe, and
forward OS signals. A thin layer over `internal/service/proc/signal` and
`internal/core/proc` — type **aliases** plus ergonomic delegating funcs, no new
types and no logic of its own.

## Why this shape

- `type Signal = coreproc.Signal` and `type Target = svcsignal.Target` are
  aliases so values interoperate across the whole SDK process domain; consumers
  never see a parallel type hierarchy.
- `Parse` delegates to `coreproc.Parse` (canonical `SIGTERM`, bare `TERM`, or
  numeric `15`, all case-insensitive). `Signal.String` round-trips:
  `Parse(s.String()) == s` for every signal in the platform table.
- `Notify` / `Relay` delegate straight to the service package — see its
  `CLAUDE.md` for lifecycle, leak-freedom, and kill(2) semantics.

## Consumer-facing docs

`README.md` is **generated** by gomarkdoc from the package doc comment in
`signal.go` (ADR 0008). Do NOT hand-edit it: change the doc comment and run

```
cd pkg/v1/signal && gomarkdoc --output README.md \
  --repository.url https://github.com/kitsunium/sdk \
  --repository.default-branch main --repository.path /pkg/v1/signal .
```

`make lint` blocks any commit where the file on disk drifts from what gomarkdoc
would emit. Maintainer rationale stays here; consumer prose belongs in the
package doc comment.

## Platform

`Parse`, `String`, and `Notify` are portable. `Relay` delivers through kill(2)
on Unix and, on Windows, through TerminateProcess (a pid) or a console control
event (a process group) — `internal/service/proc/signal/relay_windows.go`.
Elsewhere it returns the typed `UNSUPPORTED_PLATFORM` sentinel rather than
acting, so downstream code compiles and degrades on every GOOS.

The facade suite follows the split: `signal_other_test.go`
(`!unix && !windows`) asserts the refusal, `signal_windows_test.go` the Windows
backend through the facade. The facade test used to carry `!unix` and assert
the refusal on Windows too, long after Windows gained its backend; the first
Windows run of the suite found it (ADR 0095).

## Do not

- add fields/logic here — push behaviour into `internal/service/proc/signal`;
- define error codes — they are central in `internal/core/proc`;
- re-implement `Parse` / `String` — re-export the domain versions.
