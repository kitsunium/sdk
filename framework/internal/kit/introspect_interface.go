package kit

import (
	"io/fs"
	"os"
)

// openFiler opens a file under a root: what readSeen needs of an os.Root.
type openFiler interface {
	OpenFile(name string, flag int, perm os.FileMode) (*os.File, error)
}

// sourceRoot is a root the source endpoint looks at a name in, then opens
// it: what readSource needs of an os.Root.
type sourceRoot interface {
	OpenFile(name string, flag int, perm os.FileMode) (*os.File, error)
	Lstat(name string) (fs.FileInfo, error)
}
