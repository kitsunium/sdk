//go:build windows

// Package lock — the DACL answer, shared with the other packages that ask the
// same question of their own directories.
//
// "Can an account meaning anybody put an entry here, or take one away?" is not
// a question about locks. internal/service/queue asks it of its queue and state
// directories, and on Windows it has exactly one answer in this repository:
// this package's DACL reader (ADR 0084, ADR 0086). A second reader would be a
// second place to get the eight ACE shapes, the deny subtraction and the NULL
// DACL wrong, so the reader is exported rather than copied (ADR 0095).
package lock

// Access rights, in the vocabulary GrantsAnyone takes (winnt.h). Each carries
// the name it has on a DIRECTORY; the file-side name of the same bit is noted
// where the two differ, because onFilesWithin asks about files.
const (
	// RightAddFile is FILE_ADD_FILE: create a file at a free name. On a file
	// the same bit is FILE_WRITE_DATA.
	RightAddFile uint32 = fileAddFile
	// RightAddSubdirectory is FILE_ADD_SUBDIRECTORY: create a directory — or a
	// junction — at a free name. On a file the same bit is FILE_APPEND_DATA.
	RightAddSubdirectory uint32 = fileAddSubdirectory
	// RightDeleteChild is FILE_DELETE_CHILD: unlink an entry this account did
	// not create.
	RightDeleteChild uint32 = fileDeleteChild
	// RightWriteDAC is WRITE_DAC: rewrite the list, and so grant itself the rest.
	RightWriteDAC uint32 = writeDAC
	// RightWriteOwner is WRITE_OWNER: take ownership, and so rewrite the list.
	RightWriteOwner uint32 = writeOwner
	// ReplaceRights is what lets its holder take away an entry it did not
	// create — the Windows spelling of "world-writable and not sticky".
	ReplaceRights uint32 = replaceRights
	// ContentRights is what lets its holder alter or remove a FILE created in
	// the directory, read off the entries such a file inherits.
	ContentRights uint32 = contentRights
)

// GrantsAnyone reports whether dir's DACL grants a right in onDirectory to an
// identifier meaning anybody — Everyone, Authenticated Users, BUILTIN\Users —
// or a right in onFilesWithin through the entries the files created in dir
// inherit. A zero mask asks nothing of its half.
//
// It is the reader NewFileLocker's directory rules run on, unchanged: every
// discretionary ACE shape decoded, denials subtracted in list order, a NULL
// DACL read as the full grant it is. observed names the grant found
// ("S-1-1-0=0x40"), or — with granted false — what kept a verdict from being
// reached: the Win32 status ("GetNamedSecurityInfoW=5"), a path that is not
// one, or the entry the walk could not fetch ("GetAce#3"). It is EMPTY only
// when the list was read to the end and grants nothing asked about. The reader
// itself fails open (ADR 0084 §D5); what a caller does with "no verdict" is
// the caller's decision — the lock accepts and logs, the queue refuses.
func GrantsAnyone(dir string, onDirectory, onFilesWithin uint32) (granted bool, observed string) {
	//: the one reader, as the lock directory's own rules call it.
	return dirGrantsAnyone(dir, onDirectory, onFilesWithin)
}
