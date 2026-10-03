# spool (service)

Outbound mail made durable: `Send` validates, stamps a Message-ID every retry
keeps, and queues the mail — in a directory that outlives the process, or in
memory — and `Run` hands each mail to a `core/app/mail.Transport`, retrying on a
growing backoff and dead-lettering after the last attempt. A redelivery of a
mail it delivered is dropped; the one resend left, after a crash between the
relay's acceptance and the acknowledgement, carries the same Message-ID.
`SendWithID` queues a mail under an identifier its caller minted, which must be
a dot-atom of at most `MaxIDBytes` (`INVALID_MAIL_ID` otherwise); a repeated
one is dropped at delivery once its mail was delivered.
Public facade: `pkg/v1/app/mail/spool` (`New`). ADR 0111, ADR 0141. See `CLAUDE.md`.
