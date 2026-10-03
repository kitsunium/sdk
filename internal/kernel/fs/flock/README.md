# flock

A non-blocking exclusive file lock for the SDK kernel. Stdlib-only.

```go
import "github.com/kitsunium/sdk/internal/kernel/fs/flock"

if !flock.Native {
	return refuseThisPlatform() // your own code, at construction
}
for {
	held, err := flock.TryLock(file)
	if err != nil {
		return wrap(err) // the kernel's errno, never "busy"
	}
	if held {
		break
	}
	waitOnYourClock(ctx) // the primitive never blocks; you poll
}
defer flock.Unlock(file)
```

`TryLock` takes an exclusive lock on the whole of a file and answers at once:
held, held elsewhere (`false, nil` — an answer, never an error), or a failure
of the call (the errno, unwrapped). There is no blocking variant: a blocking
`flock(2)`, and a `LockFileEx` without `LOCKFILE_FAIL_IMMEDIATELY`, park the
thread inside a syscall no context cancellation can reach, so a caller with a
deadline polls on its own clock instead.

| GOOS | Primitive | `Native` |
|---|---|---|
| linux, darwin, freebsd, openbsd, netbsd, dragonfly (android, ios through their tags) | `flock(LOCK_EX\|LOCK_NB)` — advisory, per open file description | `true` |
| windows | `LockFileEx` over every byte of the file, `LOCKFILE_FAIL_IMMEDIATELY` — mandatory | `true` |
| js, wasip1, plan9, aix, solaris, illumos | none: `errors.ErrUnsupported` from every call | `false` |

The two kernels agree on what excludes processes — a second open description
of the file is refused while the first holds the lock, and a holder's death
releases it — and are OPPOSITES on a second lock through the same description:
`flock(2)` converts it and returns at once, `LockFileEx` refuses it. So a
caller that shares one descriptor between goroutines puts a gate of its own in
front of `TryLock`; the primitive promises nothing there.

See `CLAUDE.md` for the measurements, the consumers, and what the package
deliberately does not do (ADR 0081, ADR 0159).
