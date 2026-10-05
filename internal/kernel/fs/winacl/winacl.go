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
