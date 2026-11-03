package s3

import (
	"errors"
	"io/fs"
	"net/http"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// isNotFound reports whether err is S3 saying the key is not there: NoSuchKey
// from GetObject, NotFound — a bare 404 — from HeadObject.
func isNotFound(err error) bool {
	code := errorCodeOf(err)
	//: the two spellings of an absent key.
	if code == "NoSuchKey" || code == "NotFound" {
		//: absent.
		return true
	}
	//: a HEAD answers no body, so a 404 may carry no code at all.
	return statusOf(err) == http.StatusNotFound && !isBucketMissing(err)
}

// isBucketMissing reports whether err is S3 saying the bucket is not there.
func isBucketMissing(err error) bool {
	//: only the code tells a missing bucket from a missing key.
	return errorCodeOf(err) == "NoSuchBucket"
}

// errorCodeOf is the error code S3 named in err, "" when it named none.
func errorCodeOf(err error) string {
	//: an API error carries the code S3 answered.
	if apiErr, named := errors.AsType[smithy.APIError](err); named {
		//: as S3 named it.
		return apiErr.ErrorCode()
	}
	//: a transport failure, a bare status: no code.
	return ""
}

// statusOf is the HTTP status err ended on, 0 when it carries none.
func statusOf(err error) int {
	//: the response error wraps the API error and carries the status.
	if response, answered := errors.AsType[*smithyhttp.ResponseError](err); answered {
		//: as S3 answered.
		return response.HTTPStatusCode()
	}
	//: a transport failure, a timeout: no answer at all.
	return 0
}

// vendorError types a failed S3 request under this package's range, the
// vendor's error kept in the chain: BucketMissing, AccessDenied, Throttled or,
// for anything else, RequestFailed.
func vendorError(cause error, fields ...errs.FieldValue) error {
	//: the sentinel the failure is, then the cause wrapped under it.
	return errs.Wrap(cause, classify(cause).Params(), fields...)
}

// classify picks the sentinel a failed request is: the code first, the
// status for an answer with no body.
func classify(cause error) *errs.Error {
	code, status := errorCodeOf(cause), statusOf(cause)
	switch {
	case code == "NoSuchBucket":
		//: the bucket is not there.
		return BucketMissing
	case denied(code, status):
		//: the credentials may not make this request.
		return AccessDenied
	case throttled(code, status):
		//: retrying later may succeed.
		return Throttled
	default:
		//: anything else, the cause kept.
		return RequestFailed
	}
}

// denied reports whether S3 refused the request's credentials.
func denied(code string, status int) bool {
	switch code {
	case "AccessDenied", "AllAccessDisabled", "InvalidAccessKeyId", "SignatureDoesNotMatch":
		//: named by its code.
		return true
	default:
		//: a forbidden answer with no code.
		return status == http.StatusForbidden
	}
}

// throttled reports whether S3 asked for the request to come back later.
func throttled(code string, status int) bool {
	switch code {
	case "SlowDown", "Throttling", "ThrottlingException", "RequestLimitExceeded":
		//: named by its code.
		return true
	default:
		//: an unavailable or too-many-requests answer with no code.
		return status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests
	}
}

// notExist is the *fs.PathError a missing name is, so errors.Is against
// fs.ErrNotExist answers as on every other engine.
func notExist(op, name string) error {
	//: a struct literal of a stdlib type, not a minted error string.
	return &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
}

// invalid is the *fs.PathError of an operation the name's kind refuses: a
// directory read as a file, a file listed as a directory.
func invalid(op, name string) error {
	//: fs.ErrInvalid, as the memory engine answers.
	return &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
}

// failRead labels a failed read — open, stat, readdir — READ_FAILED, the
// cause kept so errors.Is against fs.ErrNotExist still answers.
func failRead(cause error, name string) error {
	//: origin wins: a vendor sentinel keeps its code, READ_FAILED joins its trail.
	return errs.Wrap(cause, corevfs.ReadFailed.Params(), errs.String("path", name))
}

// failWrite labels a failed write — put, mkdir, remove — WRITE_FAILED.
func failWrite(cause error, name string) error {
	//: origin wins, as for failRead.
	return errs.Wrap(cause, corevfs.WriteFailed.Params(), errs.String("path", name))
}

// failPublish labels a failed publication PUBLISH_FAILED: a PutObject that
// failed left the previous object in place, so the call is safe to retry.
func failPublish(cause error, name string) error {
	//: origin wins, as for failRead.
	return errs.Wrap(cause, corevfs.PublishFailed.Params(), errs.String("path", name))
}

// verdict refuses name with a core vfs sentinel and no cause.
func verdict(sentinel *errs.Error, name string) error {
	//: origin wins: the sentinel's identity, the path as a diagnostic.
	return errs.Wrap(sentinel, errs.WrapParams{}, errs.String("path", name))
}
