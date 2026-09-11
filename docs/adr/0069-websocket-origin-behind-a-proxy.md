# ADR 0069 — behind a proxy that announces the scheme, the default origin rule refuses instead of guessing

- **Status**: Accepted
- **Date**: 2026-09-11
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0047](0047-sdk-net-websocket.md) §D7 — the default origin rule, not the rest of the handshake
- **Related**: [ADR 0029](0029-sdk-net-domain.md) (the net domain), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (refuse where any SDK-chosen answer would be arbitrary)

## Context

ADR 0047 §D7 checks the `Origin` header by default, because the browser's
same-origin policy does not apply to WebSocket: any page may open a connection
and the browser attaches the user's cookies to the handshake. The default rule
compares the origin with the request's own — host and port always, and the
**scheme only when TLS ended in this process**, since only then does the server
know the browser used `https`. An `http` page on the same host is otherwise the
downgrade the check exists to notice: whoever can inject into that page gets an
encrypted socket carrying the user's cookies.

Behind a TLS-terminating proxy the request arrives in plaintext whatever the
browser used. `r.TLS` is nil, the scheme is invisible, and the rule compares
host and port alone — so the downgrade passes by default, and only
`AllowOrigins`, which names the scheme outright, closes it. ADR 0047 stated
that gap; it did not close it. A deployment that never read the sentence is
left with a check that looks on and is half blind.

`X-Forwarded-Proto` was refused as an input for a good reason: where no proxy
overwrites it, the client wrote it, so trusting its VALUE would let a stranger
choose the comparison.

## Decision

The default rule reads the **presence** of a scheme-announcing header, never
its value, and refuses when it finds one on a connection this process did not
terminate:

- the headers read are RFC 7239's `Forwarded` and the de-facto
  `X-Forwarded-Proto`. Headers a FORWARD proxy adds on the client's side
  (`Via`, `X-Forwarded-For`) are deliberately not read: they say a request was
  relayed, not that a scheme was translated;
- where `r.TLS` is non-nil the scheme is known first-hand and the announcement
  is ignored;
- the refusal names what to configure: `AllowOrigins`, or `AllowAnyOrigin` for
  an endpoint whose authentication is not ambient.

Presence-only is what makes reading a client-writable header safe here: adding
one can make this check **stricter** and never looser, so it opens no way in.
It is ADR 0031's refuse branch — behind a proxy the SDK cannot know the scheme,
and every value it could assume would be arbitrary.

## Consequences

- A deployment behind a TLS-terminating proxy that relied on the default now
  gets `403` with a message naming `AllowOrigins`, instead of an origin check
  that could not see a downgrade. That is a behaviour change, and the one the
  ADR is for: the alternative is a check that reports success without doing its
  job.
- A deployment where this process terminates TLS is unchanged, announcement or
  not.
- A non-browser client (no `Origin`) is unchanged: there is no ambient
  credential to abuse.
- A client that adds `X-Forwarded-Proto` to its own request against a server
  with no proxy only makes its own handshake refused.

## Breaking changes

Behavioural, for one shape: a plaintext deployment behind a scheme-announcing
proxy, using the default origin rule. It gains a refusal it did not have, and
`AllowOrigins` — already the documented answer for that shape — restores it.
No API changes.

## Why not

- **Consult the value of `X-Forwarded-Proto`.** It is the client's where no
  proxy overwrites it, so the comparison would be chosen by whoever connects.
- **Trust the value from a configured proxy list.** That is the classic shape,
  and the classic failure: a list written once, a topology that moves, and a
  header believed again. It also adds configuration to a domain that so far
  needs none, for a guarantee `AllowOrigins` already gives exactly.
- **Keep the gap and document it louder.** ADR 0047 already documented it. A
  security default that depends on a paragraph being read is not a default.

## Deferred

- **Parsing `Forwarded`.** The presence is the whole signal; a parser would
  invite trusting the value it extracts.

## References

- `internal/service/net/websocket/handshake.go` (`verifyOrigin`,
  `forwardedScheme`, `sameOrigin`), `TestTheDefaultRuleRefusesWhenAProxyAnnouncedTheScheme`.
- RFC 7239 §4 (`Forwarded`), RFC 6455 §4.1 (the Origin header in the handshake).
