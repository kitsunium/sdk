package s3

import (
	"io/fs"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
)

// delimiter splits a key into the names of a path.
const delimiter string = "/"

// ReadDir lists the directory name, sorted by name.
func (f *s3FS) ReadDir(name string) (entries []fs.DirEntry, err error) {
	//: the same refusals every engine gives.
	if perr := corevfs.ValidatePath(name); perr != nil {
		//: INVALID_PATH.
		return nil, perr
	}
	//: one listing, page by page.
	return f.list(name)
}

// list reads every entry of the directory name, page by page, and tells an
// empty directory from an absent one and from a file.
func (f *s3FS) list(name string) (entries []fs.DirEntry, err error) {
	prefix := f.dirPrefix(name)
	var token *string
	marker := false
	//: one ListObjectsV2 per page of Config.MaxKeys keys.
	for {
		out, lerr := f.page(prefix, token)
		//: a failed page fails the listing: half a directory is not one.
		if lerr != nil {
			//: READ_FAILED, typed.
			return nil, failRead(lerr, name)
		}
		page, sawMarker := f.entriesOf(name, prefix, out)
		entries = append(entries, page...)
		marker = marker || sawMarker
		//: the last page.
		if !aws.ToBool(out.IsTruncated) || out.NextContinuationToken == nil {
			break
		}
		token = out.NextContinuationToken
	}
	//: something, or the marker of an empty directory.
	if len(entries) > 0 || marker || name == rootName {
		//: io/fs requires them sorted by name.
		return sortEntries(entries), nil
	}
	//: no key under it: a file, or nothing.
	return nil, f.notADirectory(name)
}

// page is one ListObjectsV2 of the entries under prefix.
func (f *s3FS) page(prefix string, token *string) (listing *awss3.ListObjectsV2Output, err error) {
	ctx, cancel := f.call()
	defer cancel()
	listing, lerr := f.api.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket: aws.String(f.bucket), Prefix: aws.String(prefix), Delimiter: aws.String(delimiter),
		MaxKeys: aws.Int32(f.maxKeys), ContinuationToken: token,
	})
	//: typed under the package's range.
	if lerr != nil {
		//: the vendor's cause kept.
		return nil, vendorError(lerr)
	}
	//: the page.
	return listing, nil
}

// entriesOf turns one page into entries: each common prefix a directory, each
// key a file, the directory's own marker skipped and reported.
func (f *s3FS) entriesOf(dir, prefix string, out *awss3.ListObjectsV2Output) (entries []fs.DirEntry, marker bool) {
	entries = make([]fs.DirEntry, 0, len(out.CommonPrefixes)+len(out.Contents))
	//: the subdirectories.
	for _, common := range out.CommonPrefixes {
		child := strings.TrimSuffix(strings.TrimPrefix(aws.ToString(common.Prefix), prefix), delimiter)
		//: a key another client wrote with "//" or "." is not a name.
		if validChild(child) {
			entries = append(entries, dirEntry{fsys: f, path: childPath(dir, child), name: child, dir: true})
		}
	}
	//: the files.
	for _, object := range out.Contents {
		key := aws.ToString(object.Key)
		//: the directory's own marker is not one of its entries.
		if key == prefix {
			marker = true
			continue
		}
		child := strings.TrimPrefix(key, prefix)
		//: as for the prefixes.
		if validChild(child) {
			entries = append(entries, dirEntry{fsys: f, path: childPath(dir, child), name: child})
		}
	}
	//: this page's names.
	return entries, marker
}

// validChild reports whether child is one path element io/fs accepts.
func validChild(child string) bool {
	//: one element, valid, not the root.
	return child != "" && child != rootName && fs.ValidPath(child) && !strings.Contains(child, delimiter)
}

// sortEntries sorts entries by name and keeps one entry per name — the file
// when a key and a prefix share it, as Open prefers the file.
func sortEntries(entries []fs.DirEntry) []fs.DirEntry {
	slices.SortStableFunc(entries, func(a, b fs.DirEntry) int {
		//: by name, a file before a directory of the same name.
		if c := strings.Compare(a.Name(), b.Name()); c != 0 {
			return c
		}
		//: files first.
		return boolOrder(a.IsDir()) - boolOrder(b.IsDir())
	})
	//: duplicates collapse onto their first.
	return slices.CompactFunc(entries, func(a, b fs.DirEntry) bool { return a.Name() == b.Name() })
}

// boolOrder orders false before true.
func boolOrder(b bool) int {
	//: 1 for true.
	if b {
		return 1
	}
	//: 0 for false.
	return 0
}

// notADirectory is the error a listing with no key is: fs.ErrInvalid for a
// file, fs.ErrNotExist for nothing.
func (f *s3FS) notADirectory(name string) error {
	_, found, oerr := f.statObject(name)
	//: the HeadObject failed.
	if oerr != nil {
		//: READ_FAILED.
		return failRead(oerr, name)
	}
	//: a file is not listed.
	if found {
		//: fs.ErrInvalid, as the memory engine answers.
		return failRead(invalid("readdir", name), name)
	}
	//: fs.ErrNotExist.
	return failRead(notExist("readdir", name), name)
}
