# winacl

The Windows DACL reader of the SDK kernel. Stdlib-only.

```go
//go:build windows

import "github.com/kitsunium/sdk/internal/kernel/fs/winacl"

granted, observed := winacl.GrantsAnyone(dir, winacl.ReplaceRights, winacl.ContentRights)
switch {
case granted:
	// Everyone, Authenticated Users or BUILTIN\Users holds a right asked
	// about: observed says which identifier and which bits ("S-1-1-0=0x40").
	return refuse(dir, observed)
case observed != "":
	// No verdict: the list could not be read, or only in part
	// ("GetNamedSecurityInfoW=5", "GetAce#3"). Accept or refuse — that is
	// YOUR rule, not the reader's.
default:
	// Read to the end; nobody meaning "anybody" holds those rights.
}
```

On Windows `os.Stat` has no permission bits: it synthesises `0777` for every
writable directory, so a rule written against a mode refuses every directory a
caller could name. The question such a rule asks — could ANY account write
here? — is answerable; it is the directory's discretionary access control
list. `GrantsAnyone` reads it and answers for the identifiers that mean
anybody, asking one mask of the entries that apply to the directory and
another of the entries the FILES created in it will inherit, which is the half
with no Unix counterpart (a file created `0600` on Unix stays `0600`; on
Windows it takes the directory's inheritable entries).

| Mask | Rights | The question |
|---|---|---|
| `ReplaceRights` | `FILE_DELETE_CHILD`, `WRITE_DAC`, `WRITE_OWNER` | can a stranger take away an entry it did not create? — "world-writable and not sticky" |
| `CreateRights` | `FILE_ADD_SUBDIRECTORY`, `WRITE_DAC`, `WRITE_OWNER` | can a stranger put a directory (or a junction) at a free name? — asked of a path component's holder |
| `ContentRights` | `FILE_WRITE_DATA`, `FILE_APPEND_DATA`, `DELETE`, `WRITE_DAC`, `WRITE_OWNER` | can a stranger alter a file created here? — asked of what files inherit |

The single rights (`RightAddFile`, `RightAddSubdirectory`, `RightDeleteChild`,
`RightDelete`, `RightWriteDAC`, `RightWriteOwner`) are exported to build other
masks; the vocabulary compiles on every platform, the reader only on Windows.

All eight discretionary ACE shapes are decoded, denials are subtracted in list
order per modelled account (a deny to Everyone reaches Authenticated Users), a
NULL DACL is the full grant it is, and an entry too small for the identifier it
claims is never read. Two `advapi32` exports, bound with `syscall.NewLazyDLL`,
no `golang.org/x/sys`.

See `CLAUDE.md` for the rules carried from ADR 0084, 0086 and 0095, and its two
consumers' opposite answers to "could not look".
