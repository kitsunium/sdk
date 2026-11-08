# cgroupfs

Where this process sits in the control-group filesystem — the one reading of
`/proc/self/mountinfo` and `/proc/self/cgroup` the proc family's engines share.

```go
func Mounts(readFile func(string) ([]byte, error)) (v2, v1 []MountValue)
func Membership(content string) (v2Path, v1Path string)
func UnderMountRoot(mountRoot, cgroupPath string) (relative string, ok bool)
func OwnGroup(readFile func(string) ([]byte, error)) (dir string, ok bool)
```

`proc/memlimit` walks the limit files above the process's cgroup with it,
`proc/cgroup`'s `Probe` names the caller's own group with it, and `proc/self`
reads the container id from the membership with it.

Internal to `internal/service/proc`. See `CLAUDE.md`.
