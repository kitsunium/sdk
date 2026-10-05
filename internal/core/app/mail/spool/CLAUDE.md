# internal/core/app/mail/spool/

## Purpose

The core mirror of `internal/service/app/mail/spool`, the durable mail outbox
(ADR 0111, ADR 0141). It holds that package's error codes and sentinels and
nothing else: ADR 0160 puts every `errs.Define` of a service package in the
core package at the same path, so a domain's codes are found in one layer and
the facade's sentinels alias the core.

There is no port here. The spool is one engine over `core/app/mail.Transport`
and the queue domain; its configuration and its event values are the
service's, and `pkg/v1/app/mail/spool` aliases them there (ADR 0074).

## Surface

| File | Surface |
|---|---|
| `codes_gen.go` | `CodeSpoolMisconfigured` … `CodeInvalidMailID` — range `0.3.81.*`; `SpoolMisconfigured`, `SpoolClosed`, `MessageUndecodable`, `MessageUnencodable`, `TransportPanicked`, `InvalidMailID` (`errs.Define`) — written by kit gen from `design/app/mail/spool.yaml` (ADR 0164) |
| `errors.go` | hand-written beside them: `exitConfig`, `httpBadRequest`, `httpUnavailable` |

## Error codes

Range `0.3.81.*`, allocated to `internal/service/app/mail/spool` (ADR 0111) and
declared here since ADR 0160 — the layer byte still reads 3, because a code
keeps the value its allocation gave it whichever layer declares it.

| Code | Reason | Status | When |
|---|---|---|---|
| `0.3.81.1` | `SPOOL_MISCONFIGURED` | exit 78 | `New` refused its `Config`, or `Config.NewID` minted an identifier no mail can keep |
| `0.3.81.2` | `SPOOL_CLOSED` | HTTP 503 | `Send` or `SendWithID` after `Close` |
| `0.3.81.3` | `MESSAGE_UNDECODABLE` | — | a spooled record that is not a spooled mail; that delivery's failure |
| `0.3.81.4` | `MESSAGE_UNENCODABLE` | HTTP 400 | `encoding/json` refused the record — a `Date` outside the years 0 to 9999 |
| `0.3.81.5` | `TRANSPORT_PANICKED` | — | an attempt whose transport panicked, recovered as that attempt's failure |
| `0.3.81.6` | `INVALID_MAIL_ID` | HTTP 400 | `SendWithID` got an identifier that is empty, too long or not a dot-atom |

The two HTTP statuses are integer literals behind named constants: a core
package does not import `net/http` for two numbers.

## Do NOT

- **Renumber a code** to make it read as a core one (`0.2.*`). The value is a
  wire contract — a dashboard, an alert rule or a client branches on it.
- **Put mechanism here.** Stamping, queueing, the delivered-ID ledger and the
  backoff are the service's.
- **Name the identifier, the mail or a server reply in a Public.** The fields
  carry the rule broken and a length; a Public travels to strangers.
- **Import `net/http`** for a status code — write the number and name it.

## Verification

```
bazel test --config=race //internal/kernel/errs:errs_test
# OR
cd internal/core && GOWORK=off go vet ./app/mail/spool
cd internal/kernel && GOWORK=off go test -race -run 'TestAudit' ./errs
```
