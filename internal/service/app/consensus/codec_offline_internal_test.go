package consensus

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	svctransform "github.com/kitsunium/sdk/internal/service/data/transform"
)

// The offline readers decode with the codec the expected metadata names
// when the caller gives none: a snapshot saved by a node on gzip opens
// without naming gzip, where it used to be decoded with flate and fail as
// corrupt, and a codec this build does not link is refused
// VERSION_UNSUPPORTED before any byte is read (D18, #548).
func TestTheOfflineReadersUseTheCodecTheMetadataNames(t *testing.T) {
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 4, 8}, every: 1000, size: 1,
		configure: func(_ *testNode, cfg *Config) {
			cfg.SnapshotCodec = svctransform.GzipCompressor.(coretransform.BoundedDecompressor)
		},
	})
	tn := c.nodes[0]
	c.leader(5 * time.Second)
	for range 10 {
		propose(t, tn.node, "k=v")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var buf bytes.Buffer
	meta, err := tn.node.SaveSnapshot(ctx, "op:save", &buf)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Codec != string(svctransform.GzipCompressor.Algorithm()) {
		t.Fatalf("saved with codec %q", meta.Codec)
	}
	r, err := OpenSnapshot(bytes.NewReader(buf.Bytes()), &RestoreOptions{Expected: meta, MaxDecoded: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); err != nil {
		t.Fatalf("a gzip snapshot opened with no codec given: %v", err)
	}
	unknown := meta
	unknown.Codec = "unknown-codec"
	if _, err := OpenSnapshot(bytes.NewReader(buf.Bytes()), &RestoreOptions{Expected: unknown, MaxDecoded: 1 << 30}); !errors.Is(err, coreconsensus.VersionUnsupported) {
		t.Fatalf("a codec this build does not link: %v", err)
	}
}
