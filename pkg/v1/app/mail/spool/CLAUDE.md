# pkg/v1/app/mail/spool/

## Purpose

The public facade for the durable mail outbox (ADR 0111, ADR 0141): type
aliases over `internal/service/app/mail/spool`, its six sentinels aliased from
`internal/core/app/mail/spool` (ADR 0160), and two functions — `New`, and
`AttemptFrom` for a transport that wants the attempt its context carries.

It was published by `pkg/v1/app/mail` under `Spool*` names until it moved here,
so that a program that only composes and sends mail links no queue: the outbox
stands on `data/queue`, and a durable one on `data/vfs` and `data/sql`
beneath it. A program that wants it imports both packages — `mail` for the
message and the transport, this one for the outbox.

Consumer-facing prose lives in the package doc comment in `spool.go` and is
rendered into `README.md` by `gomarkdoc` (rule 10 / ADR 0008). This file is the
maintainer's half.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Spool` | alias | the outbox: `Send`, `SendWithID`, `Run`, `DeadLetters`, `Close` — `SendWithID` reaches consumers through the alias, so the facade adds no function for it (ADR 0141) |
| `Config` | alias | `Transport` and `MaxAttempts` required; `Dir`, `Clock`, `From`, `Backoff`, `SendTimeout`, `MaxMessageBytes`, `PollInterval`, `Observe`, `Annotate`, `NewID` optional |
| `Event`, `EventKind`, `Attempt`, `DeadLetter` | aliases | onto `EventValue`, `EventKind`, `AttemptValue`, `DeadLetterValue` |
| `EventQueued` … `EventDuplicate` | constants | the five event kinds |
| `DefaultSendTimeout`, `DefaultRetryBase`, `DefaultRetryMax`, `DefaultMaxMessageBytes`, `DeliveredMemory` | constants | the spool's clamps |
| `MaxIDBytes` | constant | the bound on a spooled mail's identifier, whoever minted it (ADR 0141) |
| `Misconfigured`, `Closed`, `MessageUndecodable`, `MessageUnencodable`, `TransportPanicked`, `InvalidMailID` | vars | the sentinels (`0.3.81.*`), aliased from the core; `InvalidMailID` is `SendWithID`'s refusal of an identifier no mail can keep |
| `New`, `AttemptFrom` | funcs | the constructor and the context accessor |

The sentinels drop the `Spool` prefix their core names carry (`SpoolClosed` is
`spool.Closed`), because the package name already says it.

## Why this shape

- **A package of its own, not a corner of `mail`.** `go list -deps` of
  `pkg/v1/app/mail` named 23 SDK packages with the spool in it and 7 without:
  every program that sends one mail was linking a queue broker, a filesystem
  engine and an SQL port. A child package is not linked by its parent's
  importers (ADR 0155), so the split costs the outbox's users nothing.
- **Aliases, not wrappers**, as everywhere in `pkg/v1`: the event a caller's
  `Observe` receives and the attempt its transport reads are the engine's own
  values.
- **The configuration is the engine's.** `Config` names a directory, a queue
  poll interval and a transport — one implementation's vocabulary, so the
  alias points at the service (ADR 0074).

## Do NOT

- **Do NOT import `pkg/v1/app/mail` from here.** The two facades share the
  core types by alias already; a facade import would only couple them.
- **Do NOT re-export the spool from `pkg/v1/app/mail`.** That is the link it
  was split out to remove.
- **Do NOT hand-edit `README.md`.** Edit the package doc comment in `spool.go`
  and run `go generate ./v1/app/mail/spool/` (rule 10).

## Verification

```bash
cd pkg && GOWORK=off go test -race ./v1/app/mail/spool/
cd pkg && go generate ./v1/app/mail/spool/   # regenerates README.md from the doc comment
```
