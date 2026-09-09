# lifecycle

Package `lifecycle` is the concrete `core/lifecycle.Lifecycle`: components come
up in the order they were added and go down in the exact reverse.

Two things it makes impossible. **A partial start that leaks** — if the fourth
component of six fails, the three that are up are stopped in reverse before
`Start` returns, through the same code path an ordinary `Stop` uses, with
contexts detached from the cancellation that may have caused the failure; the
component that failed is deliberately not stopped. **A shutdown budget one
component can spend on everyone's behalf** — `Config.StopTimeout` is the budget
*one* component gets, so a component that will not finish is abandoned at its
own deadline and every component before it in the order still gets its full
budget.

An expired budget cancels the context handed to that `Stop` — an announcement —
and stops waiting. It never kills the goroutine and never closes anything the
component owns.

`Run` adds the opt-in wiring: signals through `internal/service/proc/signal`,
readiness through `internal/service/proc/sdnotify`. Its zero config wires
neither.

Every wait goes through `kernel/clock.Timed`; a named AST test fails the build
on any wall-clock wait, in production code and in the suite.

ADR 0050. Facade: `pkg/v1/lifecycle`. See `CLAUDE.md`.
