# pkg/v1/health/

## Purpose

The public surface of the health domain (ADR 0060): startup, readiness and
liveness probes, and the two check types that keep them apart.

`README.md` is generated from the package doc comment (rule 10) — edit
`health.go`, then `cd pkg/v1 && GOWORK=off go generate ./health/...`.

## Why-this-shape

The liveness registration takes a **context-free** function and the
readiness/startup registration takes a **context-carrying** one. That is not a
stylistic asymmetry: it is what makes the classic mis-wiring — a database check
on liveness, which turns a dependency incident into a restart storm — fail to
compile rather than fail in production.

A caller who genuinely needs it can still write one, through a closure that
visibly discards the deadline. Possible, visible, reviewable.

## Do NOT

- Expose `errs.PrivateOf` output in a probe body. A probe endpoint is routinely
  reachable from more places than its authors assume, and a raw driver error
  names hosts, ports and sometimes credentials.
- Treat `Drain` as a check failure. It is a state: readiness stops serving
  permanently and liveness keeps answering, so the orchestrator stops routing
  before the drain begins.

## Verification

```
cd pkg && GOWORK=off go test -race ./v1/health/...
```
