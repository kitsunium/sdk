// Package transform_test — a program that compresses without the codec
// package: the three stdlib schemes round-trip, the caller's ceiling holds
// whether or not a scheme can be told one, and nothing of a codec is linked.
package transform_test

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/pkg/v1/data/transform"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// plainScheme is the Algorithm of a scheme with no ceiling of its own: it
// stores its input as is and implements Compressor and nothing more.
const plainScheme transform.Algorithm = "plain-test"

// plain is a Compressor that cannot be told a ceiling.
type plain struct{}

// Algorithm names the scheme.
func (plain) Algorithm() transform.Algorithm { return plainScheme }

// Compress stores src.
func (plain) Compress(dst, src []byte) ([]byte, error) { return append(dst, src...), nil }

// Decompress returns what was stored.
func (plain) Decompress(dst, src []byte) ([]byte, error) { return append(dst, src...), nil }

// registered keeps the plain scheme in the registry for this test binary.
var registered = coretransform.Register(plain{})

// TestEachSchemeRoundTrips compresses and decompresses through each of the
// three stdlib schemes, appending to a dst that already holds bytes.
func TestEachSchemeRoundTrips(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("nine tails "), 64)
	for _, algo := range []transform.Algorithm{transform.Gzip, transform.Flate, transform.Zlib} {
		box, err := transform.Compress(algo, nil, payload)
		//: compressed, and smaller for a repetitive payload.
		if err != nil || len(box) >= len(payload) {
			t.Fatalf("%s Compress = %d bytes, %v", algo, len(box), err)
		}
		out, err := transform.Decompress(algo, []byte("head:"), box)
		//: appended after what dst held.
		if err != nil || !bytes.Equal(out, append([]byte("head:"), payload...)) {
			t.Fatalf("%s Decompress = %d bytes, %v", algo, len(out), err)
		}
		bounded, err := transform.DecompressBounded(algo, nil, box, int64(len(payload)))
		//: a ceiling of exactly the plaintext's size admits it.
		if err != nil || !bytes.Equal(bounded, payload) {
			t.Fatalf("%s DecompressBounded = %d bytes, %v", algo, len(bounded), err)
		}
	}
}

// TestTheCeilingHolds pins DecompressBounded's refusal for a scheme that
// stops at the ceiling and for one that is judged afterwards.
func TestTheCeilingHolds(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte{0}, 4096)
	for _, algo := range []transform.Algorithm{transform.Gzip, transform.Flate, transform.Zlib, plainScheme} {
		box, err := transform.Compress(algo, nil, payload)
		//: compressed.
		if err != nil {
			t.Fatalf("%s Compress = %v", algo, err)
		}
		out, err := transform.DecompressBounded(algo, []byte("dst"), box, 100)
		//: refused by size, with the code the package names, dst as it was.
		if !errors.Is(err, transform.DecompressedTooLarge) || !errs.HasCode(err, transform.CodeDecompressedTooLarge) || string(out) != "dst" {
			t.Errorf("%s DecompressBounded(100) = %q, %v; want dst back and DecompressedTooLarge", algo, out, err)
		}
		out, err = transform.DecompressBounded(algo, nil, box, -1)
		//: a negative ceiling is zero, not "unlimited".
		if !errors.Is(err, transform.DecompressedTooLarge) || out != nil {
			t.Errorf("%s DecompressBounded(-1) = %d bytes, %v; want DecompressedTooLarge", algo, len(out), err)
		}
	}
}

// TestAnUnregisteredAlgorithmIsRefused pins the one refusal the package makes
// itself: the sentinel as is, and dst untouched.
func TestAnUnregisteredAlgorithmIsRefused(t *testing.T) {
	t.Parallel()
	const missing transform.Algorithm = "zstd"
	type tc struct {
		name string
		call func(dst []byte) ([]byte, error)
	}
	tests := []tc{
		{"Compress", func(dst []byte) ([]byte, error) { return transform.Compress(missing, dst, []byte("x")) }},
		{"Decompress", func(dst []byte) ([]byte, error) { return transform.Decompress(missing, dst, []byte("x")) }},
		{"DecompressBounded", func(dst []byte) ([]byte, error) { return transform.DecompressBounded(missing, dst, []byte("x"), 10) }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		out, err := c.call([]byte("dst"))
		//: the sentinel itself, with its code, and dst as it was.
		if err != transform.UnknownCompressor || !errs.HasCode(err, transform.CodeUnknownCompressor) || string(out) != "dst" {
			t.Errorf("%s = %q, %v; want dst back and UnknownCompressor", c.name, out, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: and Lookup says so without an error.
	if _, ok := transform.Lookup(missing); ok {
		t.Error("Lookup(zstd) found a scheme nothing registered")
	}
}

// TestACorruptStreamIsTheSchemesRefusal feeds each stdlib scheme a stream it
// cannot read: the refusal is that scheme's code and sentinel, re-exported
// here from the core.
func TestACorruptStreamIsTheSchemesRefusal(t *testing.T) {
	t.Parallel()
	type tc struct {
		algo     transform.Algorithm
		code     errs.Code
		sentinel error
	}
	tests := []tc{
		{transform.Gzip, transform.CodeGzipFailed, transform.GzipFailed},
		{transform.Flate, transform.CodeFlateFailed, transform.FlateFailed},
		{transform.Zlib, transform.CodeZlibFailed, transform.ZlibFailed},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		box, err := transform.Compress(c.algo, nil, bytes.Repeat([]byte("kitsune "), 32))
		//: a valid stream to corrupt.
		if err != nil {
			t.Fatalf("Compress = %v", err)
		}
		//: cut in half: no scheme reads a stream that stops mid-block.
		_, err = transform.Decompress(c.algo, nil, box[:len(box)/2])
		//: the scheme's own code, and its sentinel for errors.Is.
		if !errs.HasCode(err, c.code) || !errors.Is(err, c.sentinel) {
			t.Errorf("Decompress(cut %s stream) = %v; want code %v and its sentinel", c.algo, err, c.code)
		}
	}
	for _, c := range tests {
		t.Run(string(c.algo), func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAvailableListsWhatIsRegistered reads the registry: the three stdlib
// schemes this package's import registered, and the test's own.
func TestAvailableListsWhatIsRegistered(t *testing.T) {
	t.Parallel()
	want := []transform.Algorithm{transform.Flate, transform.Gzip, plainScheme, transform.Zlib}
	//: sorted, and nothing else.
	if got := transform.Available(); !slices.Equal(got, want) {
		t.Fatalf("Available() = %v, want %v", got, want)
	}
	//: the test's scheme is the one registered.
	if c, ok := transform.Lookup(plainScheme); !ok || c != registered {
		t.Errorf("Lookup(%s) = %v, %v", plainScheme, c, ok)
	}
}

// TestItLinksNoModuleOutsideTheSDK reads the modules this test binary was
// linked from: the SDK's own, and nothing else — the gate ADR 0156 §1 states.
// Bazel builds without module information, which only `go test` records.
func TestItLinksNoModuleOutsideTheSDK(t *testing.T) {
	t.Parallel()
	info, ok := debug.ReadBuildInfo()
	//: no module information: built by Bazel.
	if !ok || len(info.Deps) == 0 {
		t.Skip("built without module information (Bazel's rules_go)")
	}
	//: every module linked.
	for _, dependency := range info.Deps {
		//: the SDK's own modules, and only those.
		if dependency.Path != "github.com/kitsunium/sdk" && !strings.HasPrefix(dependency.Path, "github.com/kitsunium/sdk/") {
			t.Errorf("the program links %s", dependency.Path)
		}
	}
}

// TestGoListDepsNamesNoCodec asks the go tool what this package depends on:
// no codec package — compressing bytes needs none — and nothing outside the
// SDK and the standard library. It needs the go tool, which a Bazel sandbox
// does not have.
func TestGoListDepsNamesNoCodec(t *testing.T) {
	t.Parallel()
	goTool, err := exec.LookPath("go")
	//: no go tool: nothing to ask.
	if err != nil {
		t.Skip("no go tool on PATH (a Bazel sandbox)")
	}
	output, err := exec.Command(goTool, "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "github.com/kitsunium/sdk/pkg/v1/data/transform").Output()
	//: the go tool answers from the module this test runs in.
	if err != nil {
		t.Skipf("go list could not run here: %v", err)
	}
	//: every non-standard package the facade depends on.
	for line := range strings.FieldsSeq(string(output)) {
		//: a package outside the SDK, or a codec's.
		if !strings.HasPrefix(line, "github.com/kitsunium/sdk/") || strings.Contains(line, "/data/codec") {
			t.Errorf("go list -deps names %s", line)
		}
	}
}

// TestALevelIsTheCallers drives the three level constructors through the
// facade (kitsunium/sdk#900): each compresses at the caller's level — a
// different stream from the default's — and its output reads back through the
// registered scheme of the same Algorithm, since the level rides in the bytes
// and not in the reader. A level that would not compress is clamped, so the
// compressor still shrinks the payload.
func TestALevelIsTheCallers(t *testing.T) {
	t.Parallel()
	words := []string{"request", "served", "route", "status", "cache", "miss", "retry", "orders", "200", "503"}
	var b strings.Builder
	for i := range 8192 {
		b.WriteString(words[(i*7+i/3)%len(words)])
		b.WriteByte(' ')
	}
	payload := []byte(b.String())
	for _, c := range []struct {
		algo  transform.Algorithm
		build func(level int) transform.Compressor
	}{
		{transform.Gzip, transform.NewGzipCompressor},
		{transform.Flate, transform.NewFlateCompressor},
		{transform.Zlib, transform.NewZlibCompressor},
	} {
		def, derr := transform.Compress(c.algo, nil, payload)
		fast, ferr := c.build(1).Compress(nil, payload)
		inert, ierr := c.build(0).Compress(nil, payload)
		if err := errors.Join(derr, ferr, ierr); err != nil {
			t.Fatalf("%s: Compress: %v", c.algo, err)
		}
		if bytes.Equal(fast, def) {
			t.Fatalf("%s: level 1 wrote the default's bytes", c.algo)
		}
		if len(inert) >= len(payload) {
			t.Fatalf("%s: level 0 stored the payload (%d of %d bytes): the clamp did not hold", c.algo, len(inert), len(payload))
		}
		back, rerr := transform.Decompress(c.algo, nil, fast)
		if rerr != nil || !bytes.Equal(back, payload) {
			t.Fatalf("%s: the registered scheme read the level-1 stream back as err=%v, %d bytes", c.algo, rerr, len(back))
		}
	}
}

// countingReader counts the compressed bytes a stream reader takes from its
// source.
type countingReader struct {
	r io.Reader
	n int64
}

// Read implements io.Reader and counts.
func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// TestAStreamStopsAtItsLimit is #892's first acceptance line: NewReader over a
// gzip stream ten times its limit hands over exactly limit bytes, then
// DecompressedTooLarge, having read only part of the compressed source and
// allocated far less than twice the limit — the stream is never held whole.
func TestAStreamStopsAtItsLimit(t *testing.T) {
	const limit int64 = 4 << 20
	var compressed bytes.Buffer
	w, err := transform.NewWriter(transform.Gzip, &compressed)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	chunk := bytes.Repeat([]byte("ten times the limit "), 1<<12)
	for written := int64(0); written < 10*limit; written += int64(len(chunk)) {
		if _, werr := w.Write(chunk); werr != nil {
			t.Fatalf("Write: %v", werr)
		}
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	source := &countingReader{r: bytes.NewReader(compressed.Bytes())}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	r, err := transform.NewReader(transform.Gzip, source, limit)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	n, cerr := io.Copy(io.Discard, r)
	runtime.ReadMemStats(&after)
	if !errors.Is(cerr, transform.DecompressedTooLarge) || n != limit {
		t.Fatalf("handed over %d bytes then err=%v, want %d then DecompressedTooLarge", n, cerr, limit)
	}
	if source.n >= int64(compressed.Len()) {
		t.Fatalf("the reader drained the whole %d-byte source past its limit", compressed.Len())
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= uint64(2*limit) {
		t.Fatalf("reading %d bytes of a stream allocated %d bytes, want under twice the limit", limit, allocated)
	}
	t.Logf("%d of %d compressed bytes read, %d allocated, for a %d-byte limit over a %d-byte stream",
		source.n, compressed.Len(), after.TotalAlloc-before.TotalAlloc, limit, 10*limit)
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestAStreamIsRefusedWithoutAStreamForm pins the facade's two refusals: an
// unregistered Algorithm is UnknownCompressor, and a registered scheme with no
// stream form — the plain test scheme — is StreamingUnsupported, for both
// directions.
func TestAStreamIsRefusedWithoutAStreamForm(t *testing.T) {
	t.Parallel()
	if _, err := transform.NewReader("no-such-scheme", bytes.NewReader(nil), 1); !errors.Is(err, transform.UnknownCompressor) {
		t.Fatalf("NewReader(unregistered) err=%v, want UnknownCompressor", err)
	}
	if _, err := transform.NewWriter("no-such-scheme", io.Discard); !errors.Is(err, transform.UnknownCompressor) {
		t.Fatalf("NewWriter(unregistered) err=%v, want UnknownCompressor", err)
	}
	if _, err := transform.NewReader(plainScheme, bytes.NewReader(nil), 1); !errors.Is(err, transform.StreamingUnsupported) ||
		!errs.HasCode(err, transform.CodeStreamingUnsupported) {
		t.Fatalf("NewReader(plain) err=%v, want StreamingUnsupported", err)
	}
	if _, err := transform.NewWriter(plainScheme, io.Discard); !errors.Is(err, transform.StreamingUnsupported) {
		t.Fatalf("NewWriter(plain) err=%v, want StreamingUnsupported", err)
	}
}
