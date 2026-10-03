# semver

SemVer 2.0.0 precedence and Go pseudo-versions for the SDK kernel — stdlib-only,
no domain vocabulary, no allocation. Written with Go's leading `v`, and the
`vMAJOR` / `vMAJOR.MINOR` shorthands, exactly as `golang.org/x/mod/semver`
reads them; it replaces that module and the three pseudo-version functions of
`golang.org/x/mod/module` in the SDK. Consumers outside the SDK's internals
import the public alias, `pkg/v1/semver`.

```go
import "github.com/kitsunium/sdk/internal/kernel/semver"

semver.IsValid("v1.2.3-rc.1+build.5")       // true
semver.Compare("v1.9.12", "v1.10.0")        // -1: numbers, not text
semver.Compare("v1.0.0-beta.11", "v1.0.0-beta.2") // 1: 11 > 2
semver.Prerelease("v1.0.0-rc.1+build")      // "-rc.1"

slices.SortFunc(tags, semver.Compare)       // invalid strings first, then precedence

semver.IsPseudoVersion("v1.2.4-0.20260924100234-23e4c32e7484") // true
semver.PseudoVersionRev("v1.2.4-0.20260924100234-23e4c32e7484") // "23e4c32e7484", true
semver.PseudoVersionTime("v1.2.4-0.20260924100234-23e4c32e7484") // 2026-09-24 10:02:34 UTC, true
```

Edges worth knowing:

- **No errors.** An invalid string is below every version in `Compare` and
  equal to every other invalid string, and has no pre-release; the
  pseudo-version readings answer `false`.
- **Build metadata takes no part in precedence**: `v1.0.0+a` and `v1.0.0+b`
  compare equal.
- **Numbers of any size order exactly** — they are compared as decimal
  strings, never converted.
- **A pseudo-version is recognised by its shape**; a stamp whose digits are no
  real instant is still one, and `PseudoVersionTime` is what reports `false`.
- **Six functions, on purpose.** No `Canonical`, `Major`, `MajorMinor`,
  `Build`, `Sort` or `Max` — nothing calls them.

See `CLAUDE.md` for the design and `BENCH.md` for the numbers against x/mod.
