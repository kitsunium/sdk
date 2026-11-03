package s3

import (
	"bytes"
	"context"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// fakeObject is one key of the in-memory bucket.
type fakeObject struct {
	modified time.Time
	metadata map[string]string
	data     []byte
}

// fakeBucket answers the six requests the filesystem sends as S3 answers
// them — NoSuchKey from GetObject, NotFound from HeadObject, a delimited and
// paged ListObjectsV2 — so every verb runs without a network. failWith, when
// set, fails every request with that error.
type fakeBucket struct {
	objects  map[string]fakeObject
	failWith error
	mu       sync.Mutex
	puts     int
}

// newFakeBucket returns an empty bucket.
func newFakeBucket() *fakeBucket {
	return &fakeBucket{objects: map[string]fakeObject{}}
}

func (b *fakeBucket) HeadObject(_ context.Context, in *awss3.HeadObjectInput, _ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failWith != nil {
		return nil, b.failWith
	}
	object, ok := b.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &types.NotFound{}
	}
	return &awss3.HeadObjectOutput{
		ContentLength: aws.Int64(int64(len(object.data))), Metadata: maps.Clone(object.metadata),
		LastModified: aws.Time(object.modified),
	}, nil
}

func (b *fakeBucket) GetObject(_ context.Context, in *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failWith != nil {
		return nil, b.failWith
	}
	object, ok := b.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	return &awss3.GetObjectOutput{
		Body: io.NopCloser(bytes.NewReader(slices.Clone(object.data))), ContentLength: aws.Int64(int64(len(object.data))),
		Metadata: maps.Clone(object.metadata), LastModified: aws.Time(object.modified),
	}, nil
}

func (b *fakeBucket) PutObject(_ context.Context, in *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failWith != nil {
		return nil, b.failWith
	}
	b.puts++
	b.objects[aws.ToString(in.Key)] = fakeObject{
		data: data, metadata: maps.Clone(in.Metadata), modified: time.Unix(1_800_000_000+int64(b.puts), 0).UTC(),
	}
	return &awss3.PutObjectOutput{}, nil
}

func (b *fakeBucket) DeleteObject(_ context.Context, in *awss3.DeleteObjectInput, _ ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failWith != nil {
		return nil, b.failWith
	}
	delete(b.objects, aws.ToString(in.Key))
	return &awss3.DeleteObjectOutput{}, nil
}

func (b *fakeBucket) DeleteObjects(_ context.Context, in *awss3.DeleteObjectsInput, _ ...func(*awss3.Options)) (*awss3.DeleteObjectsOutput, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failWith != nil {
		return nil, b.failWith
	}
	for _, id := range in.Delete.Objects {
		delete(b.objects, aws.ToString(id.Key))
	}
	return &awss3.DeleteObjectsOutput{}, nil
}

// ListObjectsV2 lists in key order; with a delimiter, keys that hold one
// past the prefix roll up into common prefixes, and a page holds at most
// MaxKeys of keys and prefixes together, as S3 counts them.
func (b *fakeBucket) ListObjectsV2(_ context.Context, in *awss3.ListObjectsV2Input, _ ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failWith != nil {
		return nil, b.failWith
	}
	prefix, delim, after := aws.ToString(in.Prefix), aws.ToString(in.Delimiter), aws.ToString(in.ContinuationToken)
	limit := int(aws.ToInt32(in.MaxKeys))
	keys := slices.Sorted(maps.Keys(b.objects))
	out := &awss3.ListObjectsV2Output{IsTruncated: aws.Bool(false)}
	var last string
	for _, key := range keys {
		if !strings.HasPrefix(key, prefix) || (after != "" && (key <= after || (strings.HasSuffix(after, delim) && delim != "" && strings.HasPrefix(key, after)))) {
			continue
		}
		item, isPrefix := key, false
		if delim != "" {
			if i := strings.Index(key[len(prefix):], delim); i >= 0 {
				item, isPrefix = key[:len(prefix)+i+len(delim)], true
			}
		}
		if isPrefix && item == last {
			continue
		}
		if len(out.Contents)+len(out.CommonPrefixes) == limit {
			out.IsTruncated, out.NextContinuationToken = aws.Bool(true), aws.String(last)
			break
		}
		if isPrefix {
			out.CommonPrefixes = append(out.CommonPrefixes, types.CommonPrefix{Prefix: aws.String(item)})
		} else {
			object := b.objects[key]
			out.Contents = append(out.Contents, types.Object{
				Key: aws.String(key), Size: aws.Int64(int64(len(object.data))), LastModified: aws.Time(object.modified),
			})
		}
		last = item
	}
	out.KeyCount = aws.Int32(int32(len(out.Contents) + len(out.CommonPrefixes)))
	return out, nil
}

// fail makes every later request fail with err.
func (b *fakeBucket) fail(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failWith = err
}
