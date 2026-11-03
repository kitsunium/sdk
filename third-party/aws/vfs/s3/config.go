package s3

import (
	"io/fs"
	"time"

	"github.com/kitsunium/sdk/internal/core/observe/logger/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// MaxObjectBytes is the largest file WriteFile and WriteAtomic publish: the
// single-PutObject limit, 5 GiB.
const MaxObjectBytes int64 = 5 << 30

// ModeMetadata is the user-metadata key a file's or a directory's
// permission bits are recorded under, in octal (x-amz-meta-mode).
const ModeMetadata string = "mode"

// maxListKeys is the most keys one ListObjectsV2 page returns, whatever is
// asked: the ceiling Config.MaxKeys is held to.
const maxListKeys int32 = 1000

// Config configures New: the bucket and prefix the filesystem is rooted at,
// how to reach them, and the bounds every call keeps.
type Config struct {
	// Bucket is the bucket's name. Required.
	Bucket string
	// Prefix roots the filesystem under a key prefix: "assets" makes the name
	// "a.txt" the key "assets/a.txt". Empty roots it at the bucket. It must be
	// a clean relative path (io/fs.ValidPath), "." excepted.
	Prefix string
	// Region is the bucket's AWS region. Required.
	Region string
	// Endpoint overrides the S3 endpoint, in path style — LocalStack, MinIO,
	// any S3-compatible store. Empty means the region's.
	Endpoint string
	// Credentials signs every request, re-read on each signature. Required,
	// as for the s3 writer.
	Credentials writer.CredentialProvider
	// MaxKeys is how many keys one ListObjectsV2 page asks for, 1 to 1000; a
	// listing larger than a page is read page by page. Zero is refused
	// rather than defaulted (ADR 0031).
	MaxKeys int32
	// Timeout bounds every request, since the port's methods take no
	// context. Zero is refused: its two readings, "no bound" and "fail at
	// once", are opposites (ADR 0031).
	Timeout time.Duration
}

// validate refuses, by field, a Config no filesystem can be built from. It
// sends nothing: every refusal here is decidable without S3.
func (c *Config) validate() error {
	//: where the requests go, then how they are bounded.
	if targetErr := c.validateTarget(); targetErr != nil {
		//: named, so the caller knows which field.
		return targetErr
	}
	//: the first bound that is wrong, in declaration order.
	return c.validateBounds()
}

// validateTarget refuses a missing bucket, region or signer, and a prefix
// that is not a clean relative path.
func (c *Config) validateTarget() error {
	//: the first field that is wrong, in declaration order.
	switch {
	//: a bucket, a region and a signer are what every request needs.
	case c.Bucket == "":
		//: named, so the caller knows which field.
		return misconfigured("Bucket")
	case c.Region == "":
		//: named.
		return misconfigured("Region")
	case c.Credentials == nil:
		//: the default credential chain is not wired, as for the writers.
		return misconfigured("Credentials")
	//: a prefix is a clean relative path, so a key never holds "//", ".."
	//: or a leading slash a listing could not split back into names.
	case c.Prefix != "" && (c.Prefix == "." || !fs.ValidPath(c.Prefix)):
		//: named.
		return misconfigured("Prefix")
	default:
		//: every request has somewhere to go.
		return nil
	}
}

// validateBounds refuses a page size outside (0, maxListKeys] and a timeout
// that is not positive.
func (c *Config) validateBounds() error {
	//: ADR 0031: a page of zero keys has no sensible reading.
	if c.MaxKeys <= 0 || c.MaxKeys > maxListKeys {
		//: named.
		return misconfigured("MaxKeys")
	}
	//: ADR 0031: "no bound" and "fail at once" are opposite readings of zero.
	if c.Timeout <= 0 {
		//: named.
		return misconfigured("Timeout")
	}
	//: a filesystem can be built.
	return nil
}

// misconfigured refuses the configuration, naming the field at fault.
func misconfigured(field string) error {
	//: origin wins: the sentinel's identity, the field as a diagnostic.
	return errs.Wrap(S3FSMisconfigured, errs.WrapParams{}, errs.String("field", field))
}
