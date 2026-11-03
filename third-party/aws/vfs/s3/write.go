package s3

import (
	"bytes"
	"io/fs"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// WriteFile writes data at name: one PutObject. perm applies on creation; a
// rewrite keeps the mode the file had.
func (f *s3FS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	//: the name, the mode and the size, before any request.
	if gerr := f.guardWrite(name, data, perm); gerr != nil {
		//: INVALID_PATH, INVALID_PERMISSION or OBJECT_TOO_LARGE.
		return gerr
	}
	//: a parent that exists, a target that is no directory.
	if terr := f.checkFileTarget(name); terr != nil {
		//: WRITE_FAILED or NOT_REGULAR_FILE.
		return terr
	}
	existing, found, serr := f.statObject(name)
	//: the mode the rewritten file keeps is read first.
	if serr != nil {
		//: WRITE_FAILED.
		return failWrite(serr, name)
	}
	//: a rewrite must not widen a file someone narrowed.
	if found {
		perm = existing.mode.Perm()
	}
	//: one PutObject.
	if perr := f.put(f.objectKey(name), data, perm); perr != nil {
		//: WRITE_FAILED, typed.
		return failWrite(perr, name)
	}
	//: stored.
	return nil
}

// WriteAtomic publishes data at name: one PutObject, atomic per key, so a
// reader sees the old object or the new one and a failure leaves the old one
// in place. perm applies on every publication.
func (f *s3FS) WriteAtomic(name string, data []byte, perm fs.FileMode) error {
	//: the name, the mode and the size, before any request.
	if gerr := f.guardWrite(name, data, perm); gerr != nil {
		//: INVALID_PATH, INVALID_PERMISSION or OBJECT_TOO_LARGE.
		return gerr
	}
	//: a parent that exists, a target that is no directory.
	if terr := f.checkFileTarget(name); terr != nil {
		//: WRITE_FAILED or NOT_REGULAR_FILE.
		return terr
	}
	//: one PutObject: the previous object stays whole when it fails.
	if perr := f.put(f.objectKey(name), data, perm); perr != nil {
		//: PUBLISH_FAILED, typed: safe to retry.
		return failPublish(perr, name)
	}
	//: published.
	return nil
}

// guardWrite refuses what no write may carry, before any request.
func (f *s3FS) guardWrite(name string, data []byte, perm fs.FileMode) error {
	//: the same refusals every engine gives.
	if perr := corevfs.ValidateWritePath(name); perr != nil {
		//: INVALID_PATH.
		return perr
	}
	//: a mode is permission bits and not zero.
	if merr := corevfs.ValidatePerm(perm); merr != nil {
		//: INVALID_PERMISSION.
		return merr
	}
	//: one PutObject carries at most MaxObjectBytes.
	if int64(len(data)) > f.maxObject {
		//: nothing sent.
		return errs.Wrap(ObjectTooLarge, errs.WrapParams{}, errs.String("path", name), errs.Int("bytes", len(data)))
	}
	//: writable.
	return nil
}

// checkFileTarget refuses a file whose parent is not a directory, or whose
// name is one: a missing parent is not invented (ADR 0056).
func (f *s3FS) checkFileTarget(name string) error {
	parent := path.Dir(name)
	exists, derr := f.dirExists(parent)
	//: the listing failed.
	if derr != nil {
		//: WRITE_FAILED.
		return failWrite(derr, name)
	}
	//: no parent: a file in the way, or nothing.
	if !exists {
		//: NOT_REGULAR_FILE or fs.ErrNotExist.
		return f.missingParent(parent, name)
	}
	isDir, ierr := f.dirExists(name)
	//: the listing failed.
	if ierr != nil {
		//: WRITE_FAILED.
		return failWrite(ierr, name)
	}
	//: a directory is not overwritten by a file.
	if isDir {
		//: NOT_REGULAR_FILE.
		return verdict(corevfs.NotRegularFile, name)
	}
	//: writable.
	return nil
}

// missingParent is the error a write under the absent directory parent is:
// NOT_REGULAR_FILE when a file stands where a directory should, WRITE_FAILED
// over fs.ErrNotExist otherwise.
func (f *s3FS) missingParent(parent, name string) error {
	blocker, blocked, berr := f.fileAncestor(parent)
	//: a HeadObject failed.
	if berr != nil {
		//: WRITE_FAILED.
		return failWrite(berr, name)
	}
	//: a file in the way.
	if blocked {
		//: NOT_REGULAR_FILE, naming it.
		return verdict(corevfs.NotRegularFile, blocker)
	}
	//: fs.ErrNotExist, as os.WriteFile answers.
	return failWrite(notExist("open", parent), name)
}

// fileAncestor finds the first of dir and its ancestors that is a file.
func (f *s3FS) fileAncestor(dir string) (blocker string, blocked bool, err error) {
	parts := strings.Split(dir, "/")
	//: from the top, as a path is walked.
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		_, found, serr := f.statObject(prefix)
		//: a failed lookup, or the file.
		if serr != nil || found {
			//: as found.
			return prefix, found, serr
		}
	}
	//: no file anywhere above.
	return "", false, nil
}

// MkdirAll makes name and every missing parent: a marker object per
// directory, carrying perm. A file anywhere on the path is NOT_REGULAR_FILE.
func (f *s3FS) MkdirAll(name string, perm fs.FileMode) error {
	//: the name and the mode, before any request.
	if gerr := f.guardWrite(name, nil, perm); gerr != nil {
		//: INVALID_PATH or INVALID_PERMISSION.
		return gerr
	}
	blocker, blocked, berr := f.fileAncestor(name)
	//: a HeadObject failed.
	if berr != nil {
		//: WRITE_FAILED.
		return failWrite(berr, name)
	}
	//: a file in the way.
	if blocked {
		//: NOT_REGULAR_FILE, naming it.
		return verdict(corevfs.NotRegularFile, blocker)
	}
	parts := strings.Split(name, "/")
	//: each missing directory, from the top.
	for i := range parts {
		dir := strings.Join(parts[:i+1], "/")
		//: a directory that exists keeps its marker, or its absence of one.
		if merr := f.mkdir(dir, perm); merr != nil {
			//: WRITE_FAILED.
			return failWrite(merr, name)
		}
	}
	//: every directory exists.
	return nil
}

// mkdir writes the marker of dir unless dir already exists.
func (f *s3FS) mkdir(dir string, perm fs.FileMode) error {
	exists, derr := f.dirExists(dir)
	//: present already, or a failed lookup.
	if derr != nil || exists {
		//: nothing to write.
		return derr
	}
	//: an empty object whose key ends in a slash.
	return f.put(f.dirPrefix(dir), nil, perm)
}

// put is one PutObject of data at key, perm recorded as ModeMetadata.
func (f *s3FS) put(key string, data []byte, perm fs.FileMode) error {
	ctx, cancel := f.call()
	defer cancel()
	_, perr := f.api.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(f.bucket), Key: aws.String(key), Body: bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))), Metadata: map[string]string{ModeMetadata: modeText(perm)},
	})
	//: typed under the package's range.
	if perr != nil {
		//: the vendor's cause kept.
		return vendorError(perr)
	}
	//: stored: a PutObject that returned is durable.
	return nil
}
