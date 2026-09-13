# pathchain

Path-resolution primitive for the SDK kernel. Stdlib-only.

```go
import "github.com/kitsunium/sdk/internal/kernel/pathchain"

steps, err := pathchain.Resolve("/pub/myapp/locks")
if err != nil {
	return err
}
for _, step := range steps {
	if step.Indirect && step.Container.Perm()&0o002 != 0 {
		// This component is a symbolic link, and the directory holding it is
		// world-writable — anyone could have planted it.
		return refuse(step.Path, step.Target)
	}
}
```

`os.Stat` says what is at the END of a path and `os.Lstat` says the same
without following its LAST component. Neither can say whether the path arrived
where it did by going through somebody else's redirection, and `O_NOFOLLOW`
has the same limit for the same reason — it is a kernel flag about the final
component only.

`Resolve` walks the path from the filesystem root holding an open directory
handle at every step, asking each component's own directory what that
component is: an `lstat` relative to a descriptor rather than a fresh lookup
of a path string. It returns one `StepValue` per component, each carrying the
mode of the directory it was found in — because a symbolic link is not
evidence of anything on its own (`/tmp` is one on macOS, `/var/run` is one on
most Linux distributions) and what separates those from an attack is who could
have created it.

It refuses nothing. A refusal needs a policy and a policy needs a domain; this
package produces the measurement a policy cannot be written without.

A component that does not exist ends the walk with a nil error, so a caller can
audit a directory it is about to create.

The directory handles are `os.Root`, whose Unix implementation is
`openat(dirfd, name, O_NOFOLLOW|O_CLOEXEC|O_DIRECTORY)` and whose Windows
implementation passes `O_NOFOLLOW_ANY`. `syscall.Openat`, the obvious
primitive, exists in go1.27's `syscall` for linux, aix and wasip1 only —
darwin does not even define `SYS_OPENAT` — so a hand-rolled walk would have
served five kernels and silently skipped the sixth.

See ADR 0083 for the design rationale and the layering contract.
