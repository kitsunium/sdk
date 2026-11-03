package s3

import (
	"io/fs"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Remove removes one file, or one empty directory's marker.
func (f *s3FS) Remove(name string) error {
	//: the same refusals every engine gives.
	if perr := corevfs.ValidateWritePath(name); perr != nil {
		//: INVALID_PATH.
		return perr
	}
	_, found, serr := f.statObject(name)
	//: the HeadObject failed.
	if serr != nil {
		//: WRITE_FAILED.
		return failWrite(serr, name)
	}
	//: a file: one DeleteObject.
	if found {
		//: WRITE_FAILED when it fails.
		return f.deleteKey(f.objectKey(name), name)
	}
	return f.removeDir(name)
}

// removeDir removes the empty directory name: DIRECTORY_NOT_EMPTY when a key
// other than its marker lies beneath it, fs.ErrNotExist when none does.
func (f *s3FS) removeDir(name string) error {
	ctx, cancel := f.call()
	defer cancel()
	prefix := f.dirPrefix(name)
	out, lerr := f.api.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket: aws.String(f.bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(2),
	})
	//: the listing failed.
	if lerr != nil {
		//: WRITE_FAILED, typed.
		return failWrite(vendorError(lerr), name)
	}
	//: nothing under it: no such directory.
	if len(out.Contents) == 0 {
		//: fs.ErrNotExist, as os.Remove answers.
		return failWrite(&fs.PathError{Op: "remove", Path: name, Err: fs.ErrNotExist}, name)
	}
	//: a key other than the marker is an entry.
	if len(out.Contents) > 1 || aws.ToString(out.Contents[0].Key) != prefix {
		//: not a recursive delete: RemoveAll is that decision.
		return verdict(corevfs.DirectoryNotEmpty, name)
	}
	//: the marker alone.
	return f.deleteKey(prefix, name)
}

// deleteKey is one DeleteObject.
func (f *s3FS) deleteKey(key, name string) error {
	ctx, cancel := f.call()
	defer cancel()
	_, derr := f.api.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(f.bucket), Key: aws.String(key)})
	//: typed under the package's range.
	if derr != nil {
		//: WRITE_FAILED.
		return failWrite(vendorError(derr), name)
	}
	//: gone.
	return nil
}

// RemoveAll removes name and every key beneath it: a DeleteObject, then a
// DeleteObjects per listed page. A name that does not exist is success.
func (f *s3FS) RemoveAll(name string) error {
	//: the same refusals every engine gives.
	if perr := corevfs.ValidateWritePath(name); perr != nil {
		//: INVALID_PATH.
		return perr
	}
	//: the file itself, if any: S3 deletes an absent key without complaint.
	if derr := f.deleteKey(f.objectKey(name), name); derr != nil {
		//: WRITE_FAILED.
		return derr
	}
	prefix := f.dirPrefix(name)
	//: page after page until nothing is left beneath it.
	for {
		keys, lerr := f.keysUnder(prefix)
		//: the listing failed.
		if lerr != nil {
			//: WRITE_FAILED.
			return failWrite(lerr, name)
		}
		//: nothing left.
		if len(keys) == 0 {
			return nil
		}
		//: one page deleted, then listed again from the start.
		if derr := f.deleteKeys(keys); derr != nil {
			//: WRITE_FAILED.
			return failWrite(derr, name)
		}
	}
}

// keysUnder lists one page of the keys under prefix, at any depth.
func (f *s3FS) keysUnder(prefix string) (keys []types.ObjectIdentifier, err error) {
	ctx, cancel := f.call()
	defer cancel()
	out, lerr := f.api.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket: aws.String(f.bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(f.maxKeys),
	})
	//: typed under the package's range.
	if lerr != nil {
		//: the vendor's cause kept.
		return nil, vendorError(lerr)
	}
	keys = make([]types.ObjectIdentifier, 0, len(out.Contents))
	//: each key, as DeleteObjects names it.
	for _, object := range out.Contents {
		keys = append(keys, types.ObjectIdentifier{Key: object.Key})
	}
	//: one page.
	return keys, nil
}

// deleteKeys is one DeleteObjects of keys; a key it reports failed fails it.
func (f *s3FS) deleteKeys(keys []types.ObjectIdentifier) error {
	ctx, cancel := f.call()
	defer cancel()
	out, derr := f.api.DeleteObjects(ctx, &awss3.DeleteObjectsInput{
		Bucket: aws.String(f.bucket), Delete: &types.Delete{Objects: keys, Quiet: aws.Bool(true)},
	})
	//: the request failed.
	if derr != nil {
		//: typed.
		return vendorError(derr)
	}
	//: a key S3 could not delete: the tree is not gone.
	if len(out.Errors) > 0 {
		//: RequestFailed, naming the first.
		return errs.Wrap(RequestFailed, errs.WrapParams{},
			errs.String("key", aws.ToString(out.Errors[0].Key)), errs.String("s3_code", aws.ToString(out.Errors[0].Code)))
	}
	//: the page is gone.
	return nil
}

// SyncDir answers as the memory engine does — S3 has no directory entry to
// flush, a PutObject that returned is stored — with the same refusals: a file
// is NOT_REGULAR_FILE, an absent directory DIRECTORY_SYNC_FAILED over
// fs.ErrNotExist. It implements core/data/vfs's DirSyncer sibling.
func (f *s3FS) SyncDir(name string) error {
	//: the root allowed, as ValidatePath allows it.
	if perr := corevfs.ValidatePath(name); perr != nil {
		//: INVALID_PATH.
		return perr
	}
	info, serr := f.stat(name)
	//: absent, or a failed lookup.
	if serr != nil {
		//: DIRECTORY_SYNC_FAILED, the cause kept.
		return errs.Wrap(serr, corevfs.DirectorySyncFailed.Params(), errs.String("path", name))
	}
	//: a file is not a directory.
	if !info.IsDir() {
		//: NOT_REGULAR_FILE.
		return verdict(corevfs.NotRegularFile, name)
	}
	//: nothing to flush.
	return nil
}
