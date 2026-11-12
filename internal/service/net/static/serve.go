package static

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// sniffBytes is how much of a file http.DetectContentType reads to guess a
// type, the same bound http.ServeContent uses.
const sniffBytes int = 512

// serveFile answers with one file of the tree: cached for good when the
// caller marks its name content-hashed, typed without relying on the host's
// tables for the types a page cannot run without, and ranged, conditional and
// HEAD-aware through http.ServeContent when the file can seek.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, name string, file fs.File, info fs.FileInfo) {
	header := w.Header()
	//: a content-hashed name never changes content, so it is never revalidated.
	if h.immutable != nil && h.immutable(name) {
		header.Set("Cache-Control", ImmutableCacheControl)
	}
	//: the types nosniff makes load-bearing, the same on every host.
	if contentType, pinned := pinnedType(path.Ext(name)); pinned {
		header.Set("Content-Type", contentType)
	}
	//: the sidecar the client admits, in the file's place; a client that admits
	//: neither reads on.
	if h.precompressed {
		header.Add("Vary", "Accept-Encoding")
		//: served and done.
		if h.serveSidecar(w, r, name, file) {
			return
		}
	}
	//: the common case: ranges, conditional requests, HEAD, all of it.
	if content, seekable := file.(io.ReadSeeker); seekable {
		http.ServeContent(w, r, name, info.ModTime(), content)
		return
	}
	//: a tree whose files cannot seek, an archive's: whole, never ranged.
	stream(w, r, name, file, info)
}

// serveSidecar serves name.br or name.gz in name's place when the tree holds it,
// a regular file, and the client admits its coding, typed as the identity file
// is; it reports whether it answered. brotli first: where both exist it is the
// smaller. The file's own type is read from it, sniffing at most 512 bytes, when
// its name says nothing: the sidecar's bytes would type as gzip.
func (h *Handler) serveSidecar(w http.ResponseWriter, r *http.Request, name string, identity fs.File) bool {
	header := w.Header()
	accept := r.Header.Get("Accept-Encoding")
	//: the codings a sidecar exists for, best first.
	for _, sidecar := range sidecarCodings {
		//: this client does not admit it.
		if !corenet.AcceptsEncoding(accept, sidecar.token) {
			continue
		}
		file, info, found := open(h.fsys, name+sidecar.suffix)
		//: no such sidecar, or one the tree cannot open.
		if found != present {
			continue
		}
		defer closeQuietly(file)
		content, seekable := file.(io.ReadSeeker)
		//: a directory or a device in the sidecar's place, or one that cannot
		//: be ranged: the file itself is served.
		if !info.Mode().IsRegular() || !seekable {
			continue
		}
		//: the file's type, not the coding's.
		if contentType, known := typeOf(header, name, identity); known {
			header.Set("Content-Type", contentType)
		}
		header.Set("Content-Encoding", sidecar.token)
		http.ServeContent(w, r, name, info.ModTime(), content)
		//: answered.
		return true
	}
	//: the file itself.
	return false
}

// sidecarCodings are the codings a sidecar can carry, in the order they are
// preferred.
var sidecarCodings = [...]struct {
	token, suffix string
}{
	{token: "br", suffix: ".br"},
	{token: "gzip", suffix: ".gz"},
}

// typeOf returns the identity file's Content-Type: the one already set, its
// name's, or sniffed from its first bytes; known is false when none reads.
func typeOf(header http.Header, name string, identity fs.File) (contentType string, known bool) {
	//: set already: by pinnedType.
	if contentType = header.Get("Content-Type"); contentType != "" {
		//: as set.
		return contentType, true
	}
	//: the host's table.
	if contentType = mime.TypeByExtension(path.Ext(name)); contentType != "" {
		//: from the name.
		return contentType, true
	}
	var head [sniffBytes]byte
	n, err := io.ReadFull(identity, head[:])
	//: nothing readable: net/http will say nothing either.
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		//: unknown.
		return "", false
	}
	//: the identity file's bytes.
	return http.DetectContentType(head[:n]), true
}

// stream answers with a file that cannot seek: the whole body with a 200, its
// length from its stat, its type from its name or its first bytes. A read that
// fails before the status is written is a 500; one that fails after it can
// only cut the body, which the declared length makes visible to the client.
func stream(w http.ResponseWriter, r *http.Request, name string, file fs.File, info fs.FileInfo) {
	header := w.Header()
	body := bufio.NewReaderSize(file, sniffBytes)
	head, err := body.Peek(sniffBytes)
	//: a file that fails on its first read is a 500 while one can still be sent.
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		//: 500.
		failed(w)
		return
	}
	//: typed by its name, then by its first bytes.
	if header.Get("Content-Type") == "" {
		contentType := mime.TypeByExtension(path.Ext(name))
		//: a name that says nothing: the bytes do.
		if contentType == "" {
			contentType = http.DetectContentType(head)
		}
		header.Set("Content-Type", contentType)
	}
	//: the length the stat promises, so a short read is a visible cut.
	if size := info.Size(); size >= 0 {
		header.Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	//: HEAD is GET without the body.
	if r.Method == http.MethodHead {
		return
	}
	//: a failure now cannot change the status already sent.
	if _, err := io.Copy(w, body); err != nil {
		return
	}
}

// pinnedType returns the Content-Type of the extensions a page cannot run
// without, independent of the host's MIME tables. The standard library reads
// /etc/mime.types, or the Windows registry, and lets it override its own
// table; under nosniff a script or a stylesheet served as text/plain is
// refused by the browser, and a WebAssembly module without application/wasm
// cannot be compiled while it streams.
func pinnedType(extension string) (contentType string, pinned bool) {
	//: the extension, case-insensitively, as the host tables compare it.
	switch strings.ToLower(extension) {
	//: a page.
	case ".html", ".htm":
		return "text/html; charset=utf-8", true
	//: a script or a module: executed only under a JavaScript type.
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8", true
	//: a stylesheet: applied only under text/css.
	case ".css":
		return "text/css; charset=utf-8", true
	//: data, and a source map.
	case ".json", ".map":
		return "application/json", true
	//: a WebAssembly module: streamed compilation requires this type.
	case ".wasm":
		return "application/wasm", true
	//: a vector image.
	case ".svg":
		return "image/svg+xml", true
	//: a web application manifest.
	case ".webmanifest":
		return "application/manifest+json", true
	//: every other type is the host's table's, then the bytes'.
	default:
		return "", false
	}
}
