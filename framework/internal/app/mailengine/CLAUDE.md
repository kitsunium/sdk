<!-- updated: 2026-10-17T23:45:00Z -->
# framework/internal/app/mailengine — the mailers' outbox and their transports

What `framework/app/mail` plugs into kit (`plug.Mail`, ADR 0179 §1, step 6):
the outbox kit's mailers keep — the SDK's mail spool (`pkg/v1/app/mail/spool`),
on the database that keeps the mailer's service in the queue subsystem's
sealed broker, else in the data directory or in memory —, the transports that
empty it — the SDK's SMTP transport and its capture transport —, the SDK's
verdict on a mail, and the conversions between the spool's values and plug's.
kit's core imports neither the SDK's spool nor a transport: it opens every
outbox through `plug` with a `plug.MailSpoolConfig`, holds it as a
`plug.MailSpool`, and hands its mail to a `plug.MailSink`; a program that
does not import `framework/app/mail` links none of this package nor of the
SDK's spool and transports (`framework/app`'s
`TestTheSmallestProductLinksOnlyTheSubsystemsStillInTheCore`) and is refused at
the start for every mailer it declares (`CodeSubsystemMissing`).

The core still names the SDK's mail types — `Mailer.Send` takes a
`mail.Message`, `From` a `mail.Address` —, so `pkg/v1/app/mail` stays in its
graph, and with it the SDK's mail service half and `net/smtp`, initialised and
not linked: a mail crosses the contract as a `plug.MailValue`, the SDK's value
held as `any`, which this package asserts back.

## Contents

| File | Holds |
|---|---|
| `mailengine.go` | `Enable`, which sets `plug.Mail` (`open`, `validate` — the SDK's `mail.Validate` —, `capture`, `parseSMTP` — the SDK's `mail.ParseURL`, the relay's server and TLS mode spelled for kit (`tlsName`), its configuration, which holds the password, handed on as `any` and never kept —, `newSMTP` — the SDK's `mail.NewSMTP` over what `parseSMTP` read —, `refused` — each `plug.MailRefusal` the SDK's sentinel: `spool.Closed`, `spool.MessageUnencodable`, `mail.InvalidURL`); `messageOf`, `addressOf` |
| `spool.go` | `open`, the SDK's spool over a `plug.MailSpoolConfig`: kit's attempt as its transport (`attempts`), the app's clock, the events (`observeOf`, `eventOf`), the backoff (`backoffOf`), and — for an outbox on a database — the SDK's broker the queue subsystem opened there (`queueengine.BrokerOf`) and the database's transaction manager the store subsystem opened, a `Deferrer` that is an `Aborter` too (`deferrerOf`, over `storeengine.SQLOf`); `spool`, the SDK's spool as kit's core uses it (`Send`, `SendWithID`, `DeadLetters` counted, `Run`, `Close`); `attemptFrom` |
| `transport.go` | `transport` (the SMTP transport), `captured` (the capture transport, `Composed` its last message), and `attempts`, the SDK's `mail.Transport` the spool hands each attempt to, which runs kit's with the attempt its context carries |
| `clock.go` | `sdkClock`, the app's clock (`plug.Clock`) as the SDK's spool stamps and waits on it — a test's manual clock included |
| `mailengine_compliance.go` | the compile-time assertions of every plug interface the spool and the transports implement, of the SDK's transport `attempts` is, and of the clock |
| `mailengine_internal_test.go` | the backoff's curve at kit's bounds, each refusal its sentinel and no other, the capture and SMTP transports |
| `doc.go`, `README.md` | the package comment (kit writes it from the design, ADR 0167); the README, written by `tools/genindex` from `docs/api` (`make docs-readme`) |

## Rules

- It imports `plug`, `storeengine` and `queueengine` (the SDK's handles of a database the store subsystem opened and of a queue the queue subsystem opened, typed — ADR 0179 Q4: the mailers depend on the queues and the stores) and the SDK, never kit's core: the core imports no `*kit` package (ADR 0179 §3), and kit's own suites plug it in through `framework/app/mail` (`framework/internal/app/subsystems_test.go`).
- The bytes at rest are unchanged: the same spool directories (`<data>/<service>/outbox/<mailer>`), the same sealed tables (`kit_queue__<service>__<mailer>__outbox`); the sealing is kit's, in the queue subsystem's broker.
- A refusal of the SDK reaches the core as the SDK's error, which the core wraps in its own codes (`CodeMailQueue`, `CodeMailEncode`, `CodeMailConfig`, `CodeMailPanic`) where its open and send paths wrap it; the core never matches a mail sentinel itself — it asks `plug.MailDriverConfig`'s `Refused`.

## Verify

```sh
cd framework && GOWORK=off go vet ./internal/app/mailengine/ && GOWORK=off go test -race ./internal/app/mailengine/ ./internal/app/ ./app/ -run 'Mail|Outbox|SMTP|Transact|Subsystem|TheCore|TheSmallest|ImportsThe'
```
