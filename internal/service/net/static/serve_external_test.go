// Package static_test — one file's response: HEAD, ranges, the types a page
// cannot run without, and a tree whose files cannot seek.
package static_test

import (
	"io/fs"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kitsunium/sdk/internal/service/net/static"
)

// TestHeadAnswersWhatGetWouldWithoutTheBody pins HEAD on every kind of
// answer: the same status and the same headers as GET, and no body.
func TestHeadAnswersWhatGetWouldWithoutTheBody(t *testing.T) {
	t.Parallel()
	handler := build(t, site(), static.Config{SinglePageApp: true})
	//: every kind of answer, asked both ways.
	for _, target := range []string{"/app.js", "/", "/guide", "/guide/", "/settings", "/missing.js"} {
		fetched := get(handler, http.MethodGet, target)
		headed := get(handler, http.MethodHead, target)
		//: the same answer.
		if headed.Code != fetched.Code {
			t.Errorf("HEAD %s = %d, GET = %d", target, headed.Code, fetched.Code)
		}
		//: described the same way.
		for _, key := range []string{"Content-Type", "Content-Length", "Cache-Control", "Location"} {
			//: header by header.
			if headed.Header().Get(key) != fetched.Header().Get(key) {
				t.Errorf("HEAD %s %s = %q, GET = %q", target, key, headed.Header().Get(key), fetched.Header().Get(key))
			}
		}
		//: a served file's HEAD carries no body.
		if fetched.Code == http.StatusOK && headed.Body.Len() != 0 {
			t.Errorf("HEAD %s wrote a %d-byte body", target, headed.Body.Len())
		}
	}
}

// TestAFileThatSeeksIsServedInRanges pins that a seekable file goes through
// http.ServeContent: a range request is answered 206 with that range.
func TestAFileThatSeeksIsServedInRanges(t *testing.T) {
	t.Parallel()
	handler := build(t, site(), static.Config{})
	request := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	request.Header.Set("Range", "bytes=0-6")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	//: the range, and only it.
	if response.Code != http.StatusPartialContent || response.Body.String() != "console" {
		t.Fatalf("GET /app.js bytes=0-6 = %d %q, want 206 \"console\"", response.Code, response.Body.String())
	}
}

// TestPinnedTypesDoNotDependOnTheHost changes this process's MIME table the
// way a host's /etc/mime.types or registry does — the standard library lets
// them override its own — and pins that the types a page cannot run without
// are served unchanged: under nosniff, a script typed text/plain is refused by
// the browser. It is not parallel: it changes a table the whole process reads,
// and puts it back.
func TestPinnedTypesDoNotDependOnTheHost(t *testing.T) {
	hostile := map[string]string{
		".js":   "text/plain; charset=utf-8",
		".mjs":  "text/plain; charset=utf-8",
		".css":  "text/plain; charset=utf-8",
		".wasm": "application/octet-stream",
	}
	builtin := map[string]string{
		".js":   "text/javascript; charset=utf-8",
		".mjs":  "text/javascript; charset=utf-8",
		".css":  "text/css; charset=utf-8",
		".wasm": "application/wasm",
	}
	//: the host's table, as a misconfigured host would have it.
	for extension, contentType := range hostile {
		//: the change must take, or the test proves nothing.
		if err := mime.AddExtensionType(extension, contentType); err != nil || mime.TypeByExtension(extension) != contentType {
			t.Fatalf("could not set %s to %s: %v", extension, contentType, err)
		}
	}
	t.Cleanup(func() {
		//: the standard library's own table back.
		for extension, contentType := range builtin {
			//: a valid extension and type are always accepted.
			if err := mime.AddExtensionType(extension, contentType); err != nil {
				t.Errorf("restore %s: %v", extension, err)
			}
		}
	})
	handler := build(t, fstest.MapFS{
		"app.js":     {Data: []byte("x")},
		"module.mjs": {Data: []byte("x")},
		"style.css":  {Data: []byte("x")},
		"code.wasm":  {Data: []byte("\x00asm")},
	}, static.Config{})
	//: each pinned type, served.
	for target, want := range map[string]string{
		"/app.js":     builtin[".js"],
		"/module.mjs": builtin[".mjs"],
		"/style.css":  builtin[".css"],
		"/code.wasm":  builtin[".wasm"],
	} {
		//: the pinned type, whatever the host says.
		if got := get(handler, http.MethodGet, target).Header().Get("Content-Type"); got != want {
			t.Errorf("GET %s Content-Type = %q, want %q", target, got, want)
		}
	}
}

// noSeekFS is a tree whose files cannot seek — what an archive's files are —
// and whose readFails file fails on its first read.
type noSeekFS struct {
	fstest.MapFS
	readFails string
}

// Open returns the map's file behind a type that only reads.
func (n noSeekFS) Open(name string) (fs.File, error) {
	file, err := n.MapFS.Open(name)
	//: the map's own answer.
	if err != nil {
		return nil, err
	}
	//: a directory keeps its own methods; the handler never reads one.
	if info, statErr := file.Stat(); statErr == nil && info.IsDir() {
		return file, nil
	}
	return readOnlyFile{file: file, fails: name == n.readFails}, nil
}

// readOnlyFile reads, stats and closes, and nothing else.
type readOnlyFile struct {
	file  fs.File
	fails bool
}

// Read reads from the file, or fails when told to.
func (r readOnlyFile) Read(p []byte) (int, error) {
	//: the configured failure.
	if r.fails {
		return 0, errDiskFailure
	}
	return r.file.Read(p)
}

// Stat stats the file.
func (r readOnlyFile) Stat() (fs.FileInfo, error) { return r.file.Stat() }

// Close closes the file.
func (r readOnlyFile) Close() error { return r.file.Close() }

// TestAFileThatCannotSeekIsServedWhole pins the path http.ServeContent cannot
// take: a file with no Seek — an archive's — is streamed with a 200, its
// declared length and a type — pinned, from the host's table, or sniffed — and
// HEAD writes no body.
func TestAFileThatCannotSeekIsServedWhole(t *testing.T) {
	t.Parallel()
	body := "console.log('streamed')"
	handler := build(t, noSeekFS{MapFS: fstest.MapFS{
		"app.js":     {Data: []byte(body)},
		"notes":      {Data: []byte("plain words, no extension")},
		"index.html": {Data: []byte("<title>shell</title>")},
	}}, static.Config{SinglePageApp: true})
	response := get(handler, http.MethodGet, "/app.js")
	//: whole, with its length and its pinned type.
	if response.Code != http.StatusOK || response.Body.String() != body {
		t.Fatalf("GET /app.js = %d %q, want 200 and the whole file", response.Code, response.Body.String())
	}
	//: the declared length.
	if got := response.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %q, want %d", got, len(body))
	}
	//: the pinned type.
	if got := response.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("Content-Type = %q, want the pinned JavaScript type", got)
	}
	//: a name that says nothing: typed by its bytes.
	if got := get(handler, http.MethodGet, "/notes").Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("GET /notes Content-Type = %q, want the sniffed text/plain", got)
	}
	headed := get(handler, http.MethodHead, "/app.js")
	//: HEAD: the same headers, no body.
	if headed.Code != http.StatusOK || headed.Body.Len() != 0 || headed.Header().Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("HEAD /app.js = %d, %d-byte body, length %q", headed.Code, headed.Body.Len(), headed.Header().Get("Content-Length"))
	}
	//: the fallback streams the shell the same way.
	if shell := get(handler, http.MethodGet, "/settings"); shell.Code != http.StatusOK || shell.Body.String() != "<title>shell</title>" {
		t.Errorf("GET /settings = %d %q, want the shell", shell.Code, shell.Body.String())
	}
}

// TestAStreamThatFailsFirstIsA500 pins that a file which cannot be read at
// all is answered 500 while a status can still be sent — its first bytes are
// read before the status is written.
func TestAStreamThatFailsFirstIsA500(t *testing.T) {
	t.Parallel()
	handler := build(t, noSeekFS{MapFS: fstest.MapFS{"app.js": {Data: []byte("x")}}, readFails: "app.js"}, static.Config{})
	//: the tree's failure, seen by a wrapper.
	if got := wrappedStatus(handler, "/app.js"); got != http.StatusInternalServerError {
		t.Fatalf("GET /app.js: the wrapper saw %d, want 500", got)
	}
}

// TestReadingAStreamedFileFailsOnlyAfterTheStatus documents the limit of what
// a status can say: a failure past the bytes read before the status — the
// sniffing window — cannot change a 200 already sent, so the body is cut short
// of its declared length, which a client sees as a broken response.
func TestReadingAStreamedFileFailsOnlyAfterTheStatus(t *testing.T) {
	t.Parallel()
	handler := build(t, cutFS{whole: []byte(strings.Repeat("0123456789", 200)), after: 1000}, static.Config{})
	response := get(handler, http.MethodGet, "/data.txt")
	//: the status was already a 200.
	if response.Code != http.StatusOK {
		t.Fatalf("GET /data.txt = %d, want 200", response.Code)
	}
	//: the body stops short of the length it declared.
	if declared := response.Header().Get("Content-Length"); declared != "2000" || response.Body.Len() >= 2000 {
		t.Errorf("declared %s, wrote %d bytes: the cut must be visible", declared, response.Body.Len())
	}
}

// cutFS serves one file, data.txt, whose reads fail after a number of bytes.
type cutFS struct {
	whole []byte
	after int
}

// Open serves data.txt only.
func (c cutFS) Open(name string) (fs.File, error) {
	//: the one file.
	if name != "data.txt" {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &cutFile{info: fstest.MapFS{"data.txt": {Data: c.whole}}, whole: c.whole, after: c.after}, nil
}

// cutFile reads its first bytes, then fails.
type cutFile struct {
	info  fstest.MapFS
	whole []byte
	after int
	read  int
}

// Read hands out bytes until the cut, then fails.
func (c *cutFile) Read(p []byte) (int, error) {
	//: past the cut: the storage failed.
	if c.read >= c.after {
		return 0, errDiskFailure
	}
	n := copy(p, c.whole[c.read:c.after])
	c.read += n
	return n, nil
}

// Stat reports the whole file's size.
func (c *cutFile) Stat() (fs.FileInfo, error) { return fs.Stat(c.info, "data.txt") }

// Close closes nothing.
func (c *cutFile) Close() error { return nil }
