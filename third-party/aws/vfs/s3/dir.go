package s3

import (
	"io"
	"io/fs"
)

// dirFile is an open directory: listed on its first ReadDir, then handed
// out in order.
type dirFile struct {
	fsys    *s3FS
	name    string
	entries []fs.DirEntry
	info    fileInfo
	offset  int
	listed  bool
}

// Stat describes the directory.
func (d *dirFile) Stat() (info fs.FileInfo, err error) {
	//: no request.
	return d.info, nil
}

// Read refuses: a directory is not read as a file.
func (d *dirFile) Read(_ []byte) (n int, err error) {
	//: READ_FAILED over fs.ErrInvalid.
	return 0, failRead(invalid("read", d.name), d.name)
}

// Close has nothing to release.
func (d *dirFile) Close() error {
	//: the listing is in memory.
	return nil
}

// ReadDir returns the next n entries, or every remaining one when n <= 0,
// as fs.ReadDirFile specifies.
func (d *dirFile) ReadDir(n int) (entries []fs.DirEntry, err error) {
	//: the listing is taken once, on the first call.
	if !d.listed {
		listed, lerr := d.fsys.list(d.name)
		//: a failed listing is retried by the next call.
		if lerr != nil {
			//: READ_FAILED.
			return nil, lerr
		}
		d.entries, d.listed = listed, true
	}
	rest := d.entries[d.offset:]
	//: everything left, and no io.EOF.
	if n <= 0 {
		d.offset = len(d.entries)
		//: possibly empty.
		return rest, nil
	}
	//: the end, reported as io.EOF.
	if len(rest) == 0 {
		//: as fs.ReadDirFile specifies.
		return nil, io.EOF
	}
	taken := min(n, len(rest))
	d.offset += taken
	//: the next page.
	return rest[:taken], nil
}
