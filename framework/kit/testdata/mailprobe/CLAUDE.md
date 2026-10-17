<!-- updated: 2026-10-18T00:00:00Z -->
# framework/kit/testdata/mailprobe — the smallest product, with the mail imported

A test fixture, not a package a product imports: `coreprobe` with
`framework/kit/mail` blank-imported — and through it `framework/kit/queue`
and `framework/kit/store` (ADR 0179 Q4) — and one mailer declared.
`initgraph_external_test.go`'s `TestAProductThatImportsTheMailLinksIt` builds
it and finds linked the mail subsystem's outbox and transports
(`framework/internal/kit/mailkit`), the SDK's mail spool under them and the
SDK's SMTP and capture transports beyond their inits, beside the queue and
store subsystems' engines, and nothing of the workflows' nor of the jobs'
(step 7) — the half of ADR
0179 §7 that keeps the other from passing by testing nothing, for step 6.

## Contents

| File | Holds |
|---|---|
| `main.go` | the product: `Line`, its `render` command, its mailer `Notify`, `main`, and the blank import of `framework/kit/mail` |
| `README.md` | written by hand: a `main` under `testdata/` is no package docs/api records, so tools/genindex writes no README for it (ADR 0167) |

## Rules

- It imports `framework/internal/kit`, `framework/kit/mail` and the standard library only.
- Its README is written by hand and says what its package comment says: an edit of one is an edit of the other.

## Verify

```sh
cd framework && GOWORK=off go test ./kit/ -run 'ImportsTheMail' -v
```
