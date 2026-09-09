# session

Package `session` declares the SDK server-side session port: the `Store` that
owns a session's lifetime, the `Sealer` that renders its identifier as a cookie
value, the opaque redacting `ID`, and the immutable `SessionValue` a store hands
back — plus the typed sentinels (NotFound / Expired / InvalidID / InvalidConfig /
IdentifierCollision / EntropyFailed / StoreUnavailable / SealInvalid /
FixationRefused).

A session is not a token. A token carries its claims and cannot be revoked; a
session is an opaque handle whose facts live on the server, so `Destroy` takes
effect on the next request.

`Store.Regenerate` is the only call that binds a subject, and it always mints a
new identifier — session fixation is prevented by the absence of any other way
to spell a login, not by remembering a step.

The SDK ships the store and the sealing. The firewall, the voters, the login
conventions and writing the cookie onto an HTTP response belong to the framework.

Concrete stores and the sealer live in `internal/service/session`; facade:
`pkg/v1/session`. ADR 0045. See `CLAUDE.md`.
