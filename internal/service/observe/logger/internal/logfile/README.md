# logfile

Package `logfile` is the hardened open both file sinks share
(`internal/service/observe/logger/sink/file` and `internal/service/observe/logger/writer/rotfile`).
`Open` refuses a path whose final component is a symbolic link twice — an
`os.Lstat` before the open, `O_NOFOLLOW` at the open on every Unix — and opens
for appending with the caller's mode; `RefuseSymlink` and `ExplainOpenFailure`
are its two refusals, both carrying `path` and, for a link, `kind=symlink`. It
declares no error code: each sink passes a `RefusalSpec` with its own wraps, so
every refusal leaves under that sink's code and wording. CWE-59, ADR 0018,
ADR 0082. See `CLAUDE.md`.
