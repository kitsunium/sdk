<!-- updated: 2026-10-17T23:45:00Z -->
# framework/app/mail — the mailers, for a product that declares them

A subsystem of `framework/app`, enabled by a blank import:
`import _ "github.com/kitsunium/sdk/framework/app/mail"` (ADR 0179 §1,
step 6). It plugs in the outbox kit's mailers keep — the SDK's mail spool,
sealed on the database that keeps the mailer's service, else in the data
directory or in memory — and the transports that empty it, the SDK's SMTP
transport and its capture transport (`mailengine.Enable`). A product that
declares no mailer links none of it; one that declares a mailer without the
import is refused at the start, every mailer at once, the refusal naming this
import and carrying `CodeSubsystemMissing` (0.4.2.83) — and, for a mailer
whose outbox is on a database, `framework/app/queue` too, which this package
imports.

It imports `framework/app/queue` and `framework/app/store` (ADR 0179 Q4): a
mailer's outbox on a database is a queue of the queue subsystem, joined to
the transaction of the database the store subsystem opened, and sealed under
kit's data keys, kept in a store of kit's own. `framework/app` does not
import this package (ADR 0179 step 10; it did until then, Q8): a product
that declares a mailer imports it, and `kit gen` writes the line for a
designed product (§5).

kit's own mailer code — `Mailer`, `Send` and its transaction rules (ADR 0178
C1, C7 item 7), the mailbox the Studio reads, the SMTP URL read again before
every delivery, the sealing of an outbox on a database — names the SDK's mail
types and nothing of its spool or transports, and stays in the core; it needs
no `Enable` of the core's (unlike `framework/app/store`'s `EnableStores`).

## Contents

| File | Holds |
|---|---|
| `mail.go` | the package documentation, the blank imports of `framework/app/queue` and `framework/app/store`, and `enable`, which calls `mailengine.Enable` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It holds no behaviour and exports nothing: the call it makes is into `framework/internal/app/mailengine`; the refusal naming the import lives in the core (`subsystem.go`, `mail_plug.go`, `subsystems_internal_test.go`).
- The call runs from a blank package-level value (`var _ = …()`), never an `init`: the SDK's convention (`KTN-FUNC-NOINIT`).

## Verify

```sh
cd framework && GOWORK=off go vet ./app/mail/ && GOWORK=off go test ./app/ -run 'ImportsTheMail|TheSmallestProduct' && GOWORK=off go test ./internal/app/ -run 'AMailer'
```
