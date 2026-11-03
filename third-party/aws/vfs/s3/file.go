package s3

import (
	"context"
	"errors"
	"io"
	"io/fs"
)

// objectFile is an open file: the GetObject body, read as the caller reads,
// under a deadline that ends at Close.
type objectFile struct {
	body   io.ReadCloser
	cancel context.CancelFunc
	info   fileInfo
}

// Stat describes the file as GetObject answered.
func (o *objectFile) Stat() (info fs.FileInfo, err error) {
	//: no request.
	return o.info, nil
}

// Read reads the body. io.EOF is returned as is, as io.Reader requires.
func (o *objectFile) Read(p []byte) (n int, err error) {
	n, rerr := o.body.Read(p)
	//: the end of the object, or bytes read.
	if rerr == nil || errors.Is(rerr, io.EOF) {
		//: unwrapped, so io.ReadAll stops.
		return n, rerr
	}
	//: a body cut short.
	return n, failRead(vendorError(rerr), o.info.name)
}

// Close releases the body and ends the request's deadline.
func (o *objectFile) Close() error {
	cerr := o.body.Close()
	o.cancel()
	//: a close failure frees nothing more to retry.
	if cerr != nil {
		//: READ_FAILED.
		return failRead(vendorError(cerr), o.info.name)
	}
	//: released.
	return nil
}
