# pkg/v1/app/health/

## Purpose

The public surface of the health domain (ADR 0060): startup, readiness and
liveness probes, and the two check types that keep them apart.

`README.md` is generated from the package doc comment (rule 10) — edit
`health.go`, then `cd pkg/v1 && GOWORK=off go generate ./app/health/...`.

## Asking a running process

`Ask` (with `AskConfig`, `DefaultAskTimeout`, `MaxAskDrainBytes` and the four
`Ask*` sentinels) is the client half — what a container's HEALTHCHECK runs in an
image with no shell and no curl (ADR 0131). It returns the status the process
answered and a nil error exactly on 200. The address is the one the process
LISTENS on; an unspecified host is dialled on the loopback of its family.

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

## Generated

`facade_gen.go` is kit's (ADR 0165): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/app/health.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the package comment and the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```
cd pkg && GOWORK=off go test -race ./v1/app/health/...
```
