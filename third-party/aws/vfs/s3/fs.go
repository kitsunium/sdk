package s3

import (
	"context"
	"io"
	"io/fs"
	"path"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
)

// rootName is the name of the filesystem's root, as io/fs spells it.
const rootName string = "."

// defaultFileMode and defaultDirMode are what Stat reports for an object, or
// a directory, that carries no ModeMetadata — one another client wrote.
const (
	defaultFileMode fs.FileMode = 0o644
	defaultDirMode  fs.FileMode = 0o755
)

// octal is the base ModeMetadata is written in.
const octal int = 8

// s3FS is the filesystem New returns: one bucket, one key prefix, every call
// bounded by timeout, every object at most maxObject bytes — MaxObjectBytes,
// lowered by the tests that prove the refusal.
type s3FS struct {
	api       objectAPI
	bucket    string
	prefix    string
	maxObject int64
	timeout   time.Duration
	maxKeys   int32
}

// New returns the filesystem rooted at cfg.Prefix in cfg.Bucket. It sends no
// request: a missing bucket is BucketMissing on the first call that reaches
// it.
func New(cfg Config) (filesystem corevfs.FullFS, err error) {
	//: everything decidable without S3 is decided here.
	if verr := cfg.validate(); verr != nil {
		//: nothing built.
		return nil, verr
	}
	//: the client is lazy: no request yet.
	return newFS(cfg, newClient(&cfg)), nil
}

// newFS builds the filesystem over api; the tests hand it an in-memory bucket.
func newFS(cfg Config, api objectAPI) *s3FS {
	prefix := ""
	//: a prefix is a directory of the bucket, so its keys follow a slash.
	if cfg.Prefix != "" {
		prefix = cfg.Prefix + "/"
	}
	//: the validated configuration, kept.
	return &s3FS{
		api: api, bucket: cfg.Bucket, prefix: prefix, maxKeys: cfg.MaxKeys, timeout: cfg.Timeout, maxObject: MaxObjectBytes,
	}
}

// call returns the context one request runs under: the port's methods take
// none, so every request is bounded by the configured timeout instead.
func (f *s3FS) call() (context.Context, context.CancelFunc) {
	//: a fresh deadline per request.
	return context.WithTimeout(context.Background(), f.timeout)
}

// objectKey is the key of the file name.
func (f *s3FS) objectKey(name string) string {
	//: the prefix is empty or ends in a slash.
	return f.prefix + name
}

// dirPrefix is the key prefix every entry of the directory name starts with;
// it is also the key of the directory's marker object.
func (f *s3FS) dirPrefix(name string) string {
	//: the root's entries start with the prefix itself.
	if name == rootName {
		//: "" at the bucket's root.
		return f.prefix
	}
	//: a directory's entries follow its name and a slash.
	return f.prefix + name + "/"
}

// Open opens name: the object's body for a file, a listing for a directory.
func (f *s3FS) Open(name string) (file fs.File, err error) {
	//: the same refusals every engine gives.
	if perr := corevfs.ValidatePath(name); perr != nil {
		//: INVALID_PATH.
		return nil, perr
	}
	//: the root always exists.
	if name == rootName {
		//: a directory with the defaults.
		return &dirFile{fsys: f, name: name, info: dirInfo(name, defaultDirMode, time.Time{})}, nil
	}
	ctx, cancel := f.call()
	out, gerr := f.api.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(f.bucket), Key: aws.String(f.objectKey(name))})
	//: an object: the file, read as the caller reads it.
	if gerr == nil {
		//: the deadline ends when the caller closes the file.
		return &objectFile{body: out.Body, cancel: cancel, info: objectInfo(name, out.ContentLength, out.Metadata, out.LastModified)}, nil
	}
	cancel()
	//: any failure but an absent key is S3's.
	if !isNotFound(gerr) {
		//: typed under the package's range, READ_FAILED in the trail.
		return nil, failRead(vendorError(gerr), name)
	}
	info, found, serr := f.statDir(name)
	//: no object, and the directory lookup failed.
	if serr != nil {
		//: READ_FAILED.
		return nil, failRead(serr, name)
	}
	//: neither a file nor a directory.
	if !found {
		//: fs.ErrNotExist in the chain.
		return nil, failRead(notExist("open", name), name)
	}
	//: a directory, listed on the first ReadDir.
	return &dirFile{fsys: f, name: name, info: info}, nil
}

// Stat describes name without reading it.
func (f *s3FS) Stat(name string) (info fs.FileInfo, err error) {
	//: the same refusals every engine gives.
	if perr := corevfs.ValidatePath(name); perr != nil {
		//: INVALID_PATH.
		return nil, perr
	}
	described, serr := f.stat(name)
	//: READ_FAILED, the cause kept.
	if serr != nil {
		//: fs.ErrNotExist for an absent name.
		return nil, failRead(serr, name)
	}
	//: the file or the directory.
	return described, nil
}

// Lstat is Stat: an object store holds no link (fs.ReadLinkFS).
func (f *s3FS) Lstat(name string) (info fs.FileInfo, err error) {
	//: nothing to not follow.
	return f.Stat(name)
}

// ReadLink refuses every name, as the memory engine does: there is no link
// to read. An absent name is fs.ErrNotExist (fs.ReadLinkFS).
func (f *s3FS) ReadLink(name string) (target string, err error) {
	//: an absent name is absent, a present one is no link.
	if _, serr := f.Stat(name); serr != nil {
		//: the Stat failure, READ_FAILED already.
		return "", serr
	}
	//: READ_FAILED over fs.ErrInvalid.
	return "", failRead(invalid("readlink", name), name)
}

// stat describes name: the object first, the directory second.
func (f *s3FS) stat(name string) (info fileInfo, err error) {
	//: the root always exists.
	if name == rootName {
		//: a directory with the defaults.
		return dirInfo(name, defaultDirMode, time.Time{}), nil
	}
	object, found, oerr := f.statObject(name)
	//: a request that failed is not an absence.
	if oerr != nil || found {
		//: the file, or the failure.
		return object, oerr
	}
	dir, found, derr := f.statDir(name)
	//: no object, and the directory lookup failed.
	if derr != nil {
		//: as S3 answered.
		return fileInfo{}, derr
	}
	//: neither.
	if !found {
		//: fs.ErrNotExist.
		return fileInfo{}, notExist("stat", name)
	}
	//: the directory.
	return dir, nil
}

// statObject is a HeadObject of the file name: its description and whether
// it exists.
func (f *s3FS) statObject(name string) (info fileInfo, found bool, err error) {
	ctx, cancel := f.call()
	defer cancel()
	out, herr := f.api.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(f.bucket), Key: aws.String(f.objectKey(name))})
	//: an absent key is an answer, every other failure an error.
	if herr != nil {
		//: absent, or S3's failure typed.
		return fileInfo{}, false, f.absentOr(herr)
	}
	//: the object.
	return objectInfo(name, out.ContentLength, out.Metadata, out.LastModified), true, nil
}

// statDir describes the directory name: its marker's mode and time when it
// has one, the defaults when it exists only because a key extends it.
func (f *s3FS) statDir(name string) (info fileInfo, found bool, err error) {
	ctx, cancel := f.call()
	defer cancel()
	out, herr := f.api.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(f.bucket), Key: aws.String(f.dirPrefix(name))})
	//: the marker MkdirAll wrote.
	if herr == nil {
		//: its recorded mode and time.
		return dirInfo(name, modeOf(out.Metadata, defaultDirMode), aws.ToTime(out.LastModified)), true, nil
	}
	//: any failure but an absent marker is S3's.
	if aerr := f.absentOr(herr); aerr != nil {
		//: typed.
		return fileInfo{}, false, aerr
	}
	exists, lerr := f.dirExists(name)
	//: a directory another client made by writing beneath it.
	if lerr != nil || !exists {
		//: absent, or the listing's failure.
		return fileInfo{}, false, lerr
	}
	//: the defaults.
	return dirInfo(name, defaultDirMode, time.Time{}), true, nil
}

// dirExists reports whether any key starts with the directory's prefix — its
// marker included: one ListObjectsV2 of one key.
func (f *s3FS) dirExists(name string) (exists bool, err error) {
	//: the root always exists.
	if name == rootName {
		//: present.
		return true, nil
	}
	ctx, cancel := f.call()
	defer cancel()
	out, lerr := f.api.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket: aws.String(f.bucket), Prefix: aws.String(f.dirPrefix(name)), MaxKeys: aws.Int32(1),
	})
	//: S3's failure, typed.
	if lerr != nil {
		//: not an absence.
		return false, vendorError(lerr)
	}
	//: one key is enough.
	return len(out.Contents) > 0, nil
}

// absentOr is nil when err is an absent key and err typed otherwise.
func (f *s3FS) absentOr(err error) error {
	//: an absent key is an answer.
	if isNotFound(err) {
		//: not an error.
		return nil
	}
	//: typed under the package's range.
	return vendorError(err)
}

// ReadFile reads the whole file name.
func (f *s3FS) ReadFile(name string) (content []byte, err error) {
	//: the same refusals every engine gives.
	if perr := corevfs.ValidatePath(name); perr != nil {
		//: INVALID_PATH.
		return nil, perr
	}
	//: the root is a directory.
	if name == rootName {
		//: READ_FAILED over fs.ErrInvalid.
		return nil, failRead(invalid("read", name), name)
	}
	ctx, cancel := f.call()
	defer cancel()
	out, gerr := f.api.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(f.bucket), Key: aws.String(f.objectKey(name))})
	//: no object: a directory, or nothing.
	if gerr != nil {
		//: as Open would answer.
		return nil, f.readFailure(gerr, name)
	}
	data, rerr := io.ReadAll(out.Body)
	closeErr := out.Body.Close()
	//: the read's failure comes first; a body read whole whose close failed
	//: did not arrive whole either.
	if rerr == nil {
		rerr = closeErr
	}
	//: a body cut short is a failed read, not a short file.
	if rerr != nil {
		//: READ_FAILED, the cause kept.
		return nil, failRead(vendorError(rerr), name)
	}
	//: the whole object.
	return data, nil
}

// readFailure is the error a GetObject that failed on name is: fs.ErrInvalid
// for a directory, fs.ErrNotExist for nothing, the typed failure otherwise.
func (f *s3FS) readFailure(gerr error, name string) error {
	//: S3's own failure.
	if !isNotFound(gerr) {
		//: typed, READ_FAILED in the trail.
		return failRead(vendorError(gerr), name)
	}
	exists, derr := f.dirExists(name)
	//: the listing failed.
	if derr != nil {
		//: READ_FAILED.
		return failRead(derr, name)
	}
	//: a directory is not read as a file.
	if exists {
		//: fs.ErrInvalid.
		return failRead(invalid("read", name), name)
	}
	//: fs.ErrNotExist.
	return failRead(notExist("open", name), name)
}

// modeOf reads the permission bits ModeMetadata recorded, fallback when the
// object carries none or carries something that is not a mode.
func modeOf(metadata map[string]string, fallback fs.FileMode) fs.FileMode {
	recorded, ok := metadata[ModeMetadata]
	//: another client's object.
	if !ok {
		//: the default.
		return fallback
	}
	bits, perr := strconv.ParseUint(recorded, octal, 32)
	//: only permission bits are recorded; anything else is not ours.
	if perr != nil || bits == 0 || fs.FileMode(bits)&^fs.ModePerm != 0 {
		//: the default.
		return fallback
	}
	//: as written.
	return fs.FileMode(bits)
}

// modeText is perm as ModeMetadata records it.
func modeText(perm fs.FileMode) string {
	//: octal, no leading zero.
	return strconv.FormatUint(uint64(perm.Perm()), octal)
}

// objectInfo describes the file name from what S3 answered of its object.
func objectInfo(name string, size *int64, metadata map[string]string, modified *time.Time) fileInfo {
	//: a regular file with its recorded mode.
	return fileInfo{name: path.Base(name), size: aws.ToInt64(size), mode: modeOf(metadata, defaultFileMode), modTime: aws.ToTime(modified)}
}

// dirInfo describes the directory name.
func dirInfo(name string, perm fs.FileMode, modified time.Time) fileInfo {
	//: a directory has no size.
	return fileInfo{name: path.Base(name), mode: fs.ModeDir | perm, modTime: modified}
}
