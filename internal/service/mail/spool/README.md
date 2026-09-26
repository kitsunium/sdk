# spool (service)

Outbound mail made durable: `Send` validates, stamps a Message-ID every retry
keeps, and queues the mail — in a directory that outlives the process, or in
memory — and `Run` hands each mail to a `core/mail.Transport`, retrying on a
growing backoff and dead-lettering after the last attempt. A redelivery of a
mail it delivered is dropped; the one resend left, after a crash between the
relay's acceptance and the acknowledgement, carries the same Message-ID.
Public facade: `pkg/v1/mail` (`NewSpool`). ADR 0111. See `CLAUDE.md`.
