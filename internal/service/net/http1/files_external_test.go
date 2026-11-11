package http1_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	stdnet "net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/server"
	"github.com/kitsunium/sdk/internal/service/net/static"
)

const (
	filesRoute = 7
	filesSize  = 8 << 20
)

// filesPattern is size bytes that make a byte out of place visible.
func filesPattern(size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i%251) ^ byte(i>>16)
	}
	return b
}

// fileBackends names the backends a loop group can be asked for here.
func fileBackends() []corenet.LoopBackend {
	if runtime.GOOS != "linux" {
		return []corenet.LoopBackend{corenet.LoopBackendPortable}
	}
	return []corenet.LoopBackend{corenet.LoopBackendIOURing, corenet.LoopBackendEpoll, corenet.LoopBackendPortable}
}

// startFiles serves fsys under /assets/ on backend, reporting false when the
// backend is absent here.
func startFiles(t *testing.T, b corenet.LoopBackend, fsys fs.FS, cfg static.Config) (string, bool) {
	t.Helper()
	routes, err := http1.Routes(http1.RouteValue{ID: filesRoute, Path: "/assets/", Prefix: true})
	if err != nil {
		t.Fatal(err)
	}
	files, err := http1.Files(filesRoute, fsys, cfg)
	if err != nil {
		t.Fatal(err)
	}
	app := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Status = 404 })
	h := http1.HandleLoop(corenet.Chain[corenet.HTTP1Handler](app, routes, files), http1.Parser{})
	srv := server.New()
	srv.Group("files", server.Listen("tcp", "127.0.0.1:0"), server.Plaintext(), server.Loop(corenet.LoopValue{Backend: b, Loops: 1})).HandleLoop(h)
	if err := srv.Start(t.Context()); err != nil {
		if b == corenet.LoopBackendIOURing {
			t.Skipf("io_uring absent: %v", err)
		}
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv.State().Listeners[0].Address, true
}

// fileGet sends one request and returns the response, its body read to its end.
func fileGet(t *testing.T, addr, method, target string, header ...string) (*http.Response, []byte) {
	t.Helper()
	c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	fmt.Fprintf(c, "%s %s HTTP/1.1\r\nHost: h\r\nConnection: close\r\n%s\r\n", method, target, strings.Join(header, "\r\n")+headerSep(header))
	res, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: method})
	if err != nil {
		t.Fatalf("read response of %s %s: %v", method, target, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body of %s %s: %v", method, target, err)
	}
	return res, body
}

func headerSep(h []string) string {
	if len(h) == 0 {
		return ""
	}
	return "\r\n"
}

// treeOnDisk writes the tree the conformance reads and returns its root.
func treeOnDisk(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "app.js"), filesPattern(filesSize))
	mustWrite(t, filepath.Join(root, "sub", "index.html"), []byte("<h1>sub</h1>"))
	return root
}

func mustWrite(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestFiles_answers is the acceptance: every backend, 8 MiB byte-exact, head,
// range, conditional, not-found, the policies.
func TestFiles_answers(t *testing.T) {
	for _, b := range fileBackends() {
		t.Run(b.String(), func(t *testing.T) {
			root := treeOnDisk(t)
			addr, ok := startFiles(t, b, os.DirFS(root), static.Config{})
			if !ok {
				return
			}
			want := filesPattern(filesSize)
			res, body := fileGet(t, addr, "GET", "/assets/app.js")
			if res.StatusCode != 200 || !bytes.Equal(body, want) {
				t.Fatalf("GET: status %d, %d bytes want %d", res.StatusCode, len(body), filesSize)
			}
			for _, kv := range [][2]string{
				{"Content-Type", "text/javascript; charset=utf-8"},
				{"Cache-Control", static.RevalidateCacheControl},
				{"Accept-Ranges", "bytes"},
				{"X-Content-Type-Options", "nosniff"},
				{"Content-Security-Policy", static.DefaultContentSecurityPolicy},
				{"Referrer-Policy", static.DefaultReferrerPolicy},
			} {
				if got := res.Header.Get(kv[0]); got != kv[1] {
					t.Fatalf("%s = %q, want %q", kv[0], got, kv[1])
				}
			}
			if res.Header.Get("Last-Modified") == "" {
				t.Fatal("no Last-Modified")
			}
			etag := res.Header.Get("ETag")
			if etag == "" {
				t.Fatal("no ETag")
			}

			head, hb := fileGet(t, addr, "HEAD", "/assets/app.js")
			if head.StatusCode != 200 || len(hb) != 0 || head.ContentLength != filesSize || head.Header.Get("Content-Type") == "" {
				t.Fatalf("HEAD: status %d, %d bytes, length %d", head.StatusCode, len(hb), head.ContentLength)
			}

			r0, b0 := fileGet(t, addr, "GET", "/assets/app.js", "Range: bytes=0-99")
			wantRange := "bytes 0-99/" + strconv.Itoa(filesSize)
			if r0.StatusCode != 206 || r0.Header.Get("Content-Range") != wantRange || !bytes.Equal(b0, want[:100]) {
				t.Fatalf("Range: status %d, Content-Range %q, %d bytes", r0.StatusCode, r0.Header.Get("Content-Range"), len(b0))
			}

			nm, nb := fileGet(t, addr, "GET", "/assets/app.js", "If-None-Match: "+etag)
			if nm.StatusCode != 304 || len(nb) != 0 {
				t.Fatalf("If-None-Match: status %d, %d bytes", nm.StatusCode, len(nb))
			}

			r416, _ := fileGet(t, addr, "GET", "/assets/app.js", "Range: bytes="+strconv.Itoa(filesSize)+"-")
			if r416.StatusCode != 416 || r416.Header.Get("Content-Range") != "bytes */"+strconv.Itoa(filesSize) {
				t.Fatalf("416: status %d, Content-Range %q", r416.StatusCode, r416.Header.Get("Content-Range"))
			}

			dir, _ := fileGet(t, addr, "GET", "/assets/sub")
			if dir.StatusCode != 301 || dir.Header.Get("Location") != "./sub/" {
				t.Fatalf("directory without slash: status %d, Location %q", dir.StatusCode, dir.Header.Get("Location"))
			}
			idx, ib := fileGet(t, addr, "GET", "/assets/sub/")
			if idx.StatusCode != 200 || string(ib) != "<h1>sub</h1>" {
				t.Fatalf("directory: status %d, body %q", idx.StatusCode, ib)
			}
			miss, _ := fileGet(t, addr, "GET", "/assets/absent.js")
			if miss.StatusCode != 404 {
				t.Fatalf("absent: status %d", miss.StatusCode)
			}

			mustWrite(t, filepath.Join(root, "none-empty"), nil)
			if err := os.Remove(filepath.Join(root, "sub", "index.html")); err != nil {
				t.Fatal(err)
			}
			nolist, nolistB := fileGet(t, addr, "GET", "/assets/sub/")
			if nolist.StatusCode != 404 || bytes.Contains(nolistB, []byte("index.html")) {
				t.Fatalf("listing: status %d, body %q", nolist.StatusCode, nolistB)
			}

			post, _ := fileGet(t, addr, "POST", "/assets/app.js", "Content-Length: 0")
			if post.StatusCode != 405 || post.Header.Get("Allow") != "GET, HEAD" {
				t.Fatalf("POST: status %d, Allow %q", post.StatusCode, post.Header.Get("Allow"))
			}
		})
	}
}

// TestFiles_copyPathAndPipeline: a tree with no descriptor — a MapFS, as an
// embed.FS — is served through the copy path, and a request pipelined behind a
// file answer waits behind it, in order.
func TestFiles_copyPathAndPipeline(t *testing.T) {
	const size = 3 << 20
	data := filesPattern(size)
	tree := fstest.MapFS{"big.bin": {Data: data}, "small.txt": {Data: []byte("hello")}}
	for _, b := range fileBackends() {
		t.Run(b.String(), func(t *testing.T) {
			addr, ok := startFiles(t, b, tree, static.Config{})
			if !ok {
				return
			}
			res, body := fileGet(t, addr, "GET", "/assets/big.bin")
			if res.StatusCode != 200 || !bytes.Equal(body, data) {
				t.Fatalf("copy path: status %d, %d bytes want %d", res.StatusCode, len(body), size)
			}
			c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))
			fmt.Fprintf(c, "GET /assets/big.bin HTTP/1.1\r\nHost: h\r\n\r\nGET /assets/small.txt HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n")
			br := bufio.NewReader(c)
			first, err := http.ReadResponse(br, &http.Request{Method: "GET"})
			if err != nil {
				t.Fatal(err)
			}
			b1, err := io.ReadAll(first.Body)
			if err != nil || !bytes.Equal(b1, data) {
				t.Fatalf("first answer: %d bytes, %v", len(b1), err)
			}
			second, err := http.ReadResponse(br, &http.Request{Method: "GET"})
			if err != nil {
				t.Fatal(err)
			}
			b2, _ := io.ReadAll(second.Body)
			if string(b2) != "hello" {
				t.Fatalf("second answer = %q", b2)
			}
		})
	}
}

// TestFiles_noEscape: a link inside the tree, to a file or a directory outside
// it, is a 404, through os.DirFS and an os.Root's FS, and the witness's bytes
// never appear.
func TestFiles_noEscape(t *testing.T) {
	const secret = "the witness outside the tree"
	for _, b := range fileBackends() {
		t.Run(b.String(), func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "root")
			outside := filepath.Join(base, "outside")
			for _, d := range []string{root, outside} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			mustWrite(t, filepath.Join(outside, "secret.txt"), []byte(secret))
			mustWrite(t, filepath.Join(root, "plain.txt"), []byte("in the tree"))
			if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "leak.txt")); err != nil {
				t.Skipf("no symbolic links here: %v", err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
				t.Skipf("no symbolic links here: %v", err)
			}
			rootFS, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rootFS.Close() })
			for name, fsys := range map[string]fs.FS{"dirfs": os.DirFS(root), "rootfs": rootFS.FS()} {
				t.Run(name, func(t *testing.T) {
					addr, ok := startFiles(t, b, fsys, static.Config{})
					if !ok {
						return
					}
					for _, target := range []string{"/assets/leak.txt", "/assets/linkdir/secret.txt"} {
						res, body := fileGet(t, addr, "GET", target)
						if res.StatusCode != 404 || bytes.Contains(body, []byte(secret)) {
							t.Fatalf("%s: status %d, %d bytes (%q)", target, res.StatusCode, len(body), body)
						}
					}
					res, body := fileGet(t, addr, "GET", "/assets/plain.txt")
					if res.StatusCode != 200 || string(body) != "in the tree" {
						t.Fatalf("plain: status %d, %q", res.StatusCode, body)
					}
				})
			}
		})
	}
}

// TestFiles_closeMidBodyLeaksNothing closes the client in the middle of a
// body: no descriptor is left, on every backend. Linux only: /proc/self/fd.
func TestFiles_closeMidBodyLeaksNothing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the descriptor table is read from /proc/self/fd")
	}
	for _, b := range fileBackends() {
		t.Run(b.String(), func(t *testing.T) {
			addr, ok := startFiles(t, b, os.DirFS(treeOnDisk(t)), static.Config{})
			if !ok {
				return
			}
			// One served whole first: everything a backend keeps for good is
			// then open, and only the close's leak is left to count.
			res, _ := fileGet(t, addr, "GET", "/assets/app.js")
			if res.StatusCode != 200 {
				t.Fatalf("status %d", res.StatusCode)
			}
			etag := res.Header.Get("ETag")
			before := countFDs(t)
			// The answers that send no body close their reader too.
			for range 20 {
				fileGet(t, addr, "HEAD", "/assets/app.js")
				fileGet(t, addr, "GET", "/assets/app.js", "If-None-Match: "+etag)
				fileGet(t, addr, "GET", "/assets/app.js", "Range: bytes=1-2")
				fileGet(t, addr, "GET", "/assets/app.js", "Range: bytes=999999999-")
			}
			for countFDs(t) > before {
				time.Sleep(10 * time.Millisecond)
			}
			for range 5 {
				c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				_ = c.SetDeadline(time.Now().Add(30 * time.Second))
				fmt.Fprint(c, "GET /assets/app.js HTTP/1.1\r\nHost: h\r\n\r\n")
				if _, err := io.ReadFull(c, make([]byte, 64<<10)); err != nil {
					t.Fatal(err)
				}
				if tc, ok := c.(*stdnet.TCPConn); ok {
					_ = tc.SetLinger(0)
				}
				_ = c.Close()
			}
			// A body the portable backend's writer lost holds its file until the
			// copy path's stall timer (10 s): the bound leaves it room.
			deadline := time.Now().Add(15 * time.Second)
			for countFDs(t) > before && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if after := countFDs(t); after > before {
				t.Fatalf("descriptors: %d before, %d after five closed bodies: %v", before, after, openFDs(t))
			}
		})
	}
}

func countFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd: %v", err)
	}
	return len(ents)
}

// refusingFileIO is a LoopIO whose backend refuses every file: the answer must
// fall back to the copy path, the head written once and the body whole.
type refusingFileIO struct {
	*fakeIO
	refused int
}

func (r *refusingFileIO) WriteFile(corenet.LoopConnRef, []byte, corenet.LoopFileSpanValue) error {
	r.refused++
	return corenet.LoopFileRefused
}

// TestFiles_refusedFallsBackToCopy: a backend that refuses a descriptor (none
// left, no splice in the kernel) answers through the copy path, byte-exact.
func TestFiles_refusedFallsBackToCopy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	const size = 200 << 10
	want := filesPattern(size)
	mustWrite(t, filepath.Join(root, "f.bin"), want)
	routes, err := http1.Routes(http1.RouteValue{ID: filesRoute, Path: "/assets/", Prefix: true})
	if err != nil {
		t.Fatal(err)
	}
	files, err := http1.Files(filesRoute, os.DirFS(root), static.Config{})
	if err != nil {
		t.Fatal(err)
	}
	app := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Status = 404 })
	h := corenet.Chain[corenet.HTTP1Handler](app, routes, files)
	a, out := startPend(h, 64<<10)
	io := &refusingFileIO{fakeIO: newFakeIO(0)}
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(io, ref)
	a.OnData(io, ref, []byte("GET /assets/f.bin HTTP/1.1\r\nHost: h\r\n\r\n"))
	a.OnBatchEnd(io)
	work := func() {
		t.Helper()
		for n := 0; len(out.jobs) > 0; n++ {
			if n > 10000 {
				t.Fatal("the copy path does not end")
			}
			job := out.jobs[0]
			out.jobs = out.jobs[1:]
			a.Worker().Work(0, job)
			a.OnDone(io, job)
			a.OnBatchEnd(io)
		}
	}
	work()
	if io.refused == 0 {
		t.Fatal("the backend was never asked")
	}
	got := io.out[ref]
	i := bytes.Index(got, []byte("\r\n\r\n"))
	if i < 0 || !bytes.Equal(got[i+4:], want) {
		t.Fatalf("got %d bytes, a body of %d, want the %d-byte file", len(got), len(got)-i-4, size)
	}
}

// openFDs names each open descriptor, for the failure message.
func openFDs(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd: %v", err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		target, _ := os.Readlink("/proc/self/fd/" + e.Name())
		names = append(names, e.Name()+"->"+target)
	}
	return names
}

// closeFile is a MapFS file that counts its closes, its own io.ReaderAt.
type closeFile struct {
	fs.File
	r      *bytes.Reader
	closed *int
}

func (f *closeFile) ReadAt(p []byte, off int64) (int, error) { return f.r.ReadAt(p, off) }

func (f *closeFile) Close() error {
	*f.closed++
	return f.File.Close()
}

// closeCountFS wraps a tree so each file counts its closes.
type closeCountFS struct {
	fs.FS
	closed *int
}

func (c closeCountFS) Open(name string) (fs.File, error) {
	f, err := c.FS.Open(name)
	if err != nil {
		return nil, err
	}
	data, err := fs.ReadFile(c.FS, name)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &closeFile{File: f, r: bytes.NewReader(data), closed: c.closed}, nil
}

// TestFiles_copyStallReleasesTheFile: a chunk the output refused, whose
// OnWritable never comes, is given up on the stall's timer: the reader closes,
// the connection does, without a byte more written.
func TestFiles_copyStallReleasesTheFile(t *testing.T) {
	closed := 0
	tree := closeCountFS{FS: fstest.MapFS{"f.bin": {Data: make([]byte, 1<<20)}}, closed: &closed}
	routes, err := http1.Routes(http1.RouteValue{ID: filesRoute, Path: "/assets/", Prefix: true})
	if err != nil {
		t.Fatal(err)
	}
	files, err := http1.Files(filesRoute, tree, static.Config{})
	if err != nil {
		t.Fatal(err)
	}
	app := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Status = 404 })
	a, out := startPend(corenet.Chain[corenet.HTTP1Handler](app, routes, files), 64<<10)
	io := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 2, Gen: 1}
	a.OnOpen(io, ref)
	a.OnData(io, ref, []byte("GET /assets/f.bin HTTP/1.1\r\nHost: h\r\n\r\n"))
	a.OnBatchEnd(io)
	job := out.jobs[0]
	out.jobs = nil
	a.Worker().Work(0, job)
	a.OnDone(io, job)
	a.OnBatchEnd(io)
	// One chunk is read and its Write refused: the stall is armed.
	io.full = true
	for len(out.jobs) > 0 {
		j := out.jobs[0]
		out.jobs = out.jobs[1:]
		a.Worker().Work(0, j)
		a.OnDone(io, j)
		a.OnBatchEnd(io)
	}
	if closed != 0 {
		t.Fatalf("closed %d times before the stall", closed)
	}
	a.OnTimeout(io, ref)
	if closed != 1 || !io.closed[ref] {
		t.Fatalf("after the stall: closed %d, connection closed %v", closed, io.closed[ref])
	}
	a.OnClose(io, ref, nil)
	if closed != 1 {
		t.Fatalf("closed %d times after OnClose, want once", closed)
	}
}
