//go:build localstack

// The S3 engine against LocalStack: the conformance rows the in-memory bucket
// runs in every lane, the docstore cycle, and the torn-read row, over the
// emulator's real PutObject, ListObjectsV2 and DeleteObjects. Excluded from
// every default lane by its tag (rule 12); run it as the s3 writer's suite:
//
//	docker run --rm -p 4566:4566 localstack/localstack
//	cd third-party/aws && GOWORK=off go test -tags localstack -race ./vfs/s3/...
//
// LOCALSTACK_ENDPOINT overrides the endpoint (default http://localhost:4566).
package s3

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
)

// localstackEndpoint is where the emulator listens.
func localstackEndpoint() string {
	if e := os.Getenv("LOCALSTACK_ENDPOINT"); e != "" {
		return e
	}
	return "http://localhost:4566"
}

// bucketSeq names one bucket per test, so the rows run in isolation.
var bucketSeq atomic.Int64

// localstackFS creates a fresh bucket and returns the engine rooted under a
// prefix of it.
func localstackFS(t *testing.T) corevfs.FullFS {
	t.Helper()
	const region = "eu-west-3"
	cfg := Config{
		Bucket: fmt.Sprintf("kitsunium-vfs-%d-%d", time.Now().UnixNano()%1_000_000, bucketSeq.Add(1)),
		Prefix: "root", Region: region, Endpoint: localstackEndpoint(), Credentials: staticCreds{},
		MaxKeys: 2, Timeout: 30 * time.Second,
	}
	raw := awss3.NewFromConfig(aws.Config{Region: region, Credentials: credAdapter{provider: staticCreds{}}},
		func(o *awss3.Options) { o.BaseEndpoint, o.UsePathStyle = aws.String(cfg.Endpoint), true })
	_, err := raw.CreateBucket(t.Context(), &awss3.CreateBucketInput{Bucket: aws.String(cfg.Bucket)})
	if err != nil {
		t.Fatalf("CreateBucket: %v (is LocalStack running at %s?)", err, cfg.Endpoint)
	}
	filesystem, err := New(cfg)
	if err != nil {
		t.Fatalf("New = %v, want nil", err)
	}
	return filesystem
}

func TestLocalStackPassesTheVfsConformanceRows(t *testing.T) {
	runConformance(t, localstackFS)
}

func TestLocalStackKeepsADocstore(t *testing.T) {
	rowDocstoreOverS3(t, localstackFS)
}

// TestLocalStackPublicationIsNeverTorn rewrites one object while readers
// open it: every read is the whole old content or the whole new one.
func TestLocalStackPublicationIsNeverTorn(t *testing.T) {
	filesystem := localstackFS(t)
	versions := [][]byte{bytes.Repeat([]byte("a"), 64<<10), bytes.Repeat([]byte("b"), 96<<10)}
	if err := filesystem.WriteAtomic("blob", versions[0], 0o644); err != nil {
		t.Fatalf("WriteAtomic = %v", err)
	}
	stop := make(chan struct{})
	var torn atomic.Int64
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, err := fs.ReadFile(filesystem, "blob")
				if err != nil || !(bytes.Equal(got, versions[0]) || bytes.Equal(got, versions[1])) {
					torn.Add(1)
				}
			}
		}()
	}
	for i := range 1000 {
		if err := filesystem.WriteAtomic("blob", versions[i%2], 0o644); err != nil {
			t.Fatalf("WriteAtomic #%d = %v", i, err)
		}
	}
	close(stop)
	readers.Wait()
	if n := torn.Load(); n != 0 {
		t.Fatalf("%d reads saw neither complete version", n)
	}
}
