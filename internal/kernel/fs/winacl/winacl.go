// Package winacl reads a Windows directory's discretionary access control list
// and answers one question of it: does an identifier meaning ANYBODY —
// Everyone, Authenticated Users, BUILTIN\Users — hold a right, on the directory
// itself or on the files that will be created in it?
//
// # Why a list and not a mode
//
// On Windows os.Stat has no permission bits to read. It SYNTHESISES a mode
// from FILE_ATTRIBUTE_READONLY, so every writable directory reports 0777 with
// no sticky bit and every read-only one 0555, and a rule written against the
// mode — "other-write and not sticky", "other-write at all" — refuses every
// directory a caller could name. The question such a rule asks is perfectly
// answerable on this platform; it is just not a mode. It is the directory's
// DACL, and this package is the one reader of it the SDK has: a second reader
// would be a second place to get eight ACE shapes, the deny subtraction and the
// NULL DACL wrong (ADR 0084, ADR 0086, ADR 0095).
//
// # Two masks, because one list answers two questions with different bits
//
// GrantsAnyone takes onDirectory, asked of the entries that apply to the
// directory itself, and onFilesWithin, asked of the entries the FILES created
// in it will inherit. The second has no Unix counterpart: a file created 0600
// on Unix is 0600 whatever its directory allows, while on Windows the mode
// passed to os.OpenFile means nothing and a new file takes the directory's
// inheritable entries instead. [ReplaceRights], [CreateRights] and
// [ContentRights] name the three questions the SDK's directory rules ask, and
// the single rights they are built from are exported beside them; WHICH mask a
// caller asks, of which directory, is the caller's rule.
//
// # It measures and never decides
//
// GrantsAnyone reports a grant FOUND, or no grant found, and names in observed
// what it found or what stopped it — and observed is empty only when the list
// was read to the end and grants nothing asked about. "Could not look" is
// therefore never "looked, and nobody may write", and what a caller does with
// the first is the caller's decision: the SDK's two callers make opposite ones
// on purpose (ADR 0084 §D5, ADR 0095). That is the rule of this kernel family:
// measure here, refuse in the domain.
//
// # Bound with no dependency
//
// The reader is two advapi32 exports, GetNamedSecurityInfoW and GetAce, bound
// with syscall.NewLazyDLL — golang.org/x/sys is banned SDK-wide (ADR 0018) —
// and the standard library supplies the rest: syscall.StringToSid,
// (*syscall.SID).String and syscall.LocalFree. A SID is compared by byte
// equality on its rendered string, so EqualSid is never bound. The reader is
// in dacl_windows.go and tokens_windows.go; this file, which every platform
// compiles, holds only the vocabulary, so a rule can name its masks in a file
// of its own without a build tag.
package winacl

// Access rights (winnt.h), in the vocabulary GrantsAnyone takes. Each carries
// the name it has on a DIRECTORY; where the same bit means something else on a
// FILE — which onFilesWithin asks about — that name is given too, and the
// duality is the point: an inherited entry granting 0x0002 lets an account
// create entries in the directory AND write the data of every file created
// there.
const (
	// RightAddFile is FILE_ADD_FILE: create a file at a free name. On a file
	// the same bit is FILE_WRITE_DATA.
	RightAddFile uint32 = 0x0002
	// RightAddSubdirectory is FILE_ADD_SUBDIRECTORY: create a directory — or a
	// junction — at a free name. On a file the same bit is FILE_APPEND_DATA.
	RightAddSubdirectory uint32 = 0x0004
	// RightDeleteChild is FILE_DELETE_CHILD: unlink an entry of this directory
	// WITHOUT owning it — the one right that turns an entry somebody created
	// into an entry somebody else may replace.
	RightDeleteChild uint32 = 0x0040
	// RightDelete is DELETE, the standard right that removes the object it is
	// held on — a FILE, when it is asked of what files inherit, rather than an
	// entry of the directory.
	RightDelete uint32 = 0x00010000
	// RightWriteDAC is WRITE_DAC: rewrite the list, and so grant itself the
	// rest.
	RightWriteDAC uint32 = 0x00040000
	// RightWriteOwner is WRITE_OWNER: take ownership, and so rewrite the list.
	RightWriteOwner uint32 = 0x00080000
)

// The three questions the SDK's directory rules ask, as masks. RightWriteDAC
// and RightWriteOwner are in every one of them: an account that can rewrite
// the list, or take ownership and then rewrite it, can grant itself the rest —
// a right to become writable is a right to write.
const (
	// ReplaceRights is what lets its holder take away an entry it did not
	// create — the Windows spelling of the Unix rule that refuses 0777 and
	// accepts 0777|sticky. RightAddFile is deliberately not in it: creating an
	// entry at a free name is exactly what the sticky bit permits.
	ReplaceRights uint32 = RightDeleteChild | RightWriteDAC | RightWriteOwner
	// CreateRights is what lets its holder put a DIRECTORY at a name nobody has
	// taken. A path component is always a directory — a junction and a
	// directory symbolic link are both created with FILE_ADD_SUBDIRECTORY, and
	// a file cannot be a component of a longer path — so this is the question
	// to ask of the directory HOLDING a component.
	CreateRights uint32 = RightAddSubdirectory | RightWriteDAC | RightWriteOwner
	// ContentRights is what lets its holder alter or remove a FILE created in
	// the directory, read off the entries such a file inherits: write or
	// append its data, delete it, or rewrite its list. It is asked through
	// onFilesWithin.
	ContentRights uint32 = RightAddFile | RightAddSubdirectory | RightDelete | RightWriteDAC | RightWriteOwner
)
