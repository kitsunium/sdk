# pkg/v1/logger/writer/

## Purpose

Blank-import activation package for the **dependency-free** logger writers —
`"console"` and `"file"` (ADR 0012). A single
`import _ "github.com/kitsunium/sdk/pkg/v1/logger/writer"` registers both
factories so `logger.NewMulti` resolves those names. Mirrors the
`import _ ".../pkg/v1/codec"` pattern.

## Contents

| File | Role |
|---|---|
| `writer.go` | package doc + blank imports of `internal/service/writer/{console,file}` |

No exported symbols — the package exists purely for its registration
side-effects. `README.md` is generated from the package doc comment via
`make docs-readme` (ADR 0008).

## Conventions

- **No third-party deps.** This package pulls only the in-tree console/file
  factories. The AWS writers (`s3`, `cloudwatch`) are NOT imported here — they
  live in `third-party/aws/writer/{s3,cloudwatch}` and a consumer blank-imports them
  individually so the AWS SDK enters the build only on explicit opt-in.
- **Side-effect import.** Like `pkg/v1/codec`, importing the package is the API;
  there is nothing to call.

## Do NOT

- Add the AWS writers here — that would drag the AWS SDK into every consumer of
  the dep-free pair, defeating the isolation ADR 0012 mandates.
- Add exported functions/types — keep this a pure registration shim.

## Verification

```
bazel build //pkg/v1/logger/writer:writer
# Fallback
cd pkg/v1 && GOWORK=off go build ./logger/writer/...
```
