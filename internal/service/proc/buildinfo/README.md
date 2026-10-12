# buildinfo (internal/service/proc/buildinfo)

What the running binary was built from. Internal service implementation behind
the public `pkg/v1/proc/buildinfo` and `pkg/v1/proc/process` facades (`Build`,
`ParseBuild`) — consumers import a facade, not this package.

## API

```go
func ReadBuild() (BuildValue, bool)
func ParseBuild(info *debug.BuildInfo) BuildValue

func (b *BuildValue) Module(path string) (ModuleValue, bool)
```

- `ReadBuild` / `ParseBuild` read `runtime/debug.BuildInfo` into a main module
  and its dependencies, each followed through its replacement, with a release,
  a commit and a local directory kept apart. Pseudo-versions are recognised
  and split by `internal/kernel/semver`, whose suite holds the grammar to the
  Go toolchain's own pattern — not by a pattern written here.
- It imports neither `runtime/metrics` nor the spawn engine: reading a build
  initialises only what reading a build needs (ADR 0172).

## Errors

None. A binary without build information is a `false`.
