<!-- updated: 2026-09-03T00:00:00Z -->
# pkg/v1/client/

## Purpose

Public façade for the outbound half of the network domain (ADR 0029): a guarded
HTTP client whose posture is enforced by the transport. Aliases onto
`internal/core/net` and `internal/service/net/client` plus thin forwarding
constructors. No logic lives here.

## Surface

| Symbol | Role |
|---|---|
| `Client`, `New` | the guarded client; a nil policy is refused |
| `Config` | typed configuration (`json` tags, `Duration` accepts `"5s"`) |
| `Response` | fully-read response: `Status`, `Header`, `Body` |
| `Policy`, `PolicyFunc`, `RequestInfo` | the authorisation port |
| `AllowMethods`, `AllowPaths`, `DenyPaths`, `Policies` | ready-made policies |
| `CallInfo`, `CallHook` | one record per outbound call |
| `RequestDenied`, `UnsafePath`, `ResponseTooLarge`, `CallFailed`, `TooManyRedirects` | sentinels |

## Why-this-shape

- **The guarantee lives in the transport, so `Client.HTTP()` is safe to hand
  out.** That is the whole reason the check is not in `Do`. `TestPolicyIsEnforcedInTheTransport`
  forges a `DELETE` through the raw `*http.Client` and asserts it is refused
  *and* that the upstream was never contacted. It is mutation-checked: moving
  the check out of the `RoundTripper` fails it.
- **Every default is closed** — nil policy, empty conjunction, empty path
  allowlist, empty method set all refuse. An allowlist that lost its contents
  must close, never become a passthrough.
- **`Get`/`Do` return a value, not an `*http.Response`.** One forgotten `Close`
  leaks a connection, and a byte count is only knowable after reading — so a
  hook firing at header time could never report size honestly. This corrected a
  real contradiction in ADR 0029's first draft.
- **A non-2xx returns both the response and an error.** The body is what makes
  the failure diagnosable; the error is what protects the caller who never
  considered the status. A 204 is a normal outcome.
- **`errors.Is` works on the sentinels** because `http.Client` wraps every
  failure in `*url.Error`; `unwrapClientError` unwraps it so the domain code
  stays reachable.

### The dot-segment / encoded-separator pair

Two related refusals, both evaluated **before** any pattern:

- `/v1/supi/..` reaching `Get` is already collapsed to `/v1/` by
  `url.ResolveReference` (RFC 3986), so it surfaces as `RequestDenied`, not
  `UnsafePath`. A reader who expects `UnsafePath` here is not wrong about the
  guard — the guard simply never sees it. The test says so explicitly.
- `/v1/supi/%2e%2e` is *not* normalised: `EscapedPath` keeps `%2e%2e` while
  `.Path` silently decodes to `/v1/supi/..`, which `[^/]+` matches. This is why
  the policy judges the escaped form.
- `/v1/supi/a%2fb` passes an anchored `[^/]+` pattern as one segment, but an
  upstream decoding before routing sees two. The ends disagree, so it is
  refused (`%2f`, `%5c`, either case).

## Do NOT

- Add decoding here. Depending on the codec registry would pull mongo-driver,
  msgpack and cbor into every consumer that only wanted a guarded GET.
- Move the policy check to the call site. That converts a guarantee into a
  convention.
- Hand-edit `README.md` — regenerate with `make docs-readme`.

## Verification

```
bazel test --config=race //pkg/v1/client:client_test
# Fallback:
cd pkg && GOWORK=off go test -race -cover ./v1/client/...
# expected: coverage 100%
```

## Reference

- ADR 0029 — `docs/adr/0029-sdk-net-domain.md`
- `internal/core/net/CLAUDE.md`, `internal/service/net/client/CLAUDE.md`
