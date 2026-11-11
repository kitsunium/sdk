package http1

import (
	"bytes"
	"cmp"
	"errors"
	"io"
	"io/fs"
	"mime"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/static"
)

const (
	// filesTagBits place a Files link's tag in a job's top bits; the tag sets
	// them, and the rest of the job is the link's own entry index.
	filesTagBits uint64 = 48
	// filesEntryMask keeps the entry index below the tag.
	filesEntryMask uint64 = 1<<filesTagBits - 1
	// filesFieldCap bounds a copied Range, If-None-Match or If-Modified-Since:
	// a longer value is treated as absent, a validator never matched by a
	// fragment of itself and a range never parsed from a truncation.
	filesFieldCap int = 256
	// filesIndex is the file a directory is served by, the single-page
	// application's shell too, as the static tree's indexName is.
	filesIndex string = "index.html"
	// filesAllow is the Allow of a 405.
	filesAllow string = "GET, HEAD"
	// filesOctet is the type a name says nothing of.
	filesOctet string = "application/octet-stream"
	// httpDate is the layout of Last-Modified and If-Modified-Since.
	httpDate string = "Mon, 02 Jan 2006 15:04:05 GMT"
	// statusPartial answers one Range.
	statusPartial int = 206
	// statusMoved redirects a directory named without its slash.
	statusMoved int = 301
	// statusRangeBad answers a Range that cannot be satisfied.
	statusRangeBad int = 416
	// statusNotAllowed answers a method the tree does not serve.
	statusNotAllowed int = 405
	// statusNotFound answers a name the tree does not hold.
	statusNotFound int = 404
	// filesEntries is how many job entries a lane starts with.
	filesEntries int = 8
	// nibbleLow keeps the low half of a byte.
	nibbleLow byte = 0x0f
	// hexUpper are the digits an escape is spelled with.
	hexUpper string = "0123456789ABCDEF"
	// unreservedSet is RFC 3986's unreserved characters beside the letters
	// and digits.
	unreservedSet string = "-_.~"
)

var (
	// methodGet is the other method the tree serves.
	methodGet = []byte("GET")
	// wildcardMatch is the value of an If-None-Match that matches any revision.
	wildcardMatch = []byte("*")
	// filesTags numbers the Files links of the process, the top bits of their
	// jobs.
	filesTags atomic.Uint64
)

// Files returns the link that serves a route's file tree: the request a router
// link gave route id is answered from fsys, on the lane's worker, and any other
// goes to the next handler; GET and HEAD only, the rest 405. Open and stat run on
// the worker, so the lane's thread never blocks on a disk. A name is the path
// under the Prefix route, percent-decoded and confined to the tree: a symbolic
// link anywhere in it is refused 404 when fsys can name one (fs.ReadLinkFS, as
// os.DirFS and an os.Root's FS do), so serve an os.Root's FS where the tree is
// not yours alone. No listing: a directory answers its index.html or 404, and
// a directory without its slash is redirected to it, cfg's SinglePageApp the
// shell for a route with no extension. One Range (RFC 9110 §14.2) is 206, a
// malformed or unsatisfiable one 416; If-None-Match, then If-Modified-Since,
// is 304. The security headers, Cache-Control and the MIME types are those of
// the static tree (internal/service/net/static). A body whose backend has
// LoopFileWriter and whose fs.File is a *os.File leaves by sendfile or splice;
// any other — embed.FS, a tree on a backend without it — through the copy path,
// read on the worker. A cfg the static tree refuses is corenet.StaticMisconfigured,
// route 0 or a nil fsys corenet.HTTP1Misconfigured. It reserves the top 16 bits of
// HTTP1ResponseValue.Job: a handler behind it keeps its own Jobs under them.
func Files(route int, fsys fs.FS, cfg static.Config) (corenet.Middleware[corenet.HTTP1Handler], error) {
	//: a route is a positive identifier.
	if route <= 0 {
		//: refused.
		return nil, misconfigured("Files", "route")
	}
	//: no tree to serve.
	if fsys == nil {
		//: refused.
		return nil, misconfigured("Files", "fsys")
	}
	//: the policies are the static tree's: its constructor judges them.
	if _, err := static.NewHandler(fsys, cfg); err != nil {
		//: as refused there.
		return nil, err
	}
	tag := filesTags.Add(1) << filesTagBits
	//: the link, one per chain, built once.
	return func(next corenet.HTTP1Handler) corenet.HTTP1Handler {
		//: its own tag, the route and the fields every answer carries.
		return &FilesLayer{
			link: newLink(next), route: route, fsys: fsys, tag: tag, immutable: cfg.Immutable, spa: cfg.SinglePageApp,
			base: filesBase(cfg), allow: filesAllowField(filesBase(cfg)),
			nextWorker: findWorker(next), nextCanceller: findCanceller(next),
		}
	}, nil
}

// filesBase renders the fields every answer of the tree carries: the static
// tree's policies, validated by its constructor, no control character in them.
func filesBase(cfg static.Config) []byte {
	csp := cmp.Or(cfg.ContentSecurityPolicy, static.DefaultContentSecurityPolicy)
	ref := cmp.Or(cfg.ReferrerPolicy, static.DefaultReferrerPolicy)
	//: the policies, then nosniff, as the static tree sends them.
	return []byte("Content-Security-Policy: " + csp + "\r\nX-Content-Type-Options: nosniff\r\nReferrer-Policy: " + ref + "\r\n")
}

// filesAllowField is base with the Allow of a 405.
func filesAllowField(base []byte) []byte {
	//: what a refusal says it would have taken.
	return append(append(make([]byte, 0, len(base)+32), base...), "Allow: "+filesAllow+"\r\n"...)
}

// fileJob is what a request hands its worker: every byte the request's answer
// needs, copied, since the request does not outlive the batch.
type fileJob struct {
	// name is the decoded path under the route, slash whether it ended in "/".
	name  []byte
	slash bool
	// head is whether the method is HEAD.
	head bool
	// rangeSpec, ifNoneMatch and ifModifiedSince are the fields' values, each
	// nil when absent or past filesFieldCap.
	rangeSpec, ifNoneMatch, ifModifiedSince []byte
}

// filesLane is one lane's job table: entries the lane fills and the worker
// reads, and the free list both sides touch.
type filesLane struct {
	// table holds the entries, replaced when it grows.
	table atomic.Pointer[[]*fileJob]
	// mu guards free, the worker returning what the lane takes.
	mu   sync.Mutex
	free []uint32
}

// ServeHTTP1 implements corenet.HTTP1Handler.
func (l *FilesLayer) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	//: not this link's route.
	if req.Route != l.route {
		l.next.ServeHTTP1(req, res)
		//: the next handler's.
		return
	}
	//: only reading is answered, before the name is looked at.
	if !bytes.Equal(req.Method, methodGet) && !bytes.Equal(req.Method, methodHead) {
		res.Status, res.Header = statusNotAllowed, l.allow
		//: 405.
		return
	}
	lane := l.laneOf(req.Lane)
	idx, job := lane.take()
	job.fill(req)
	res.Pending, res.Job = true, l.tag|uint64(idx)
}

// EndBatch implements corenet.HTTP1Batcher, forwarding to the next handler.
func (l *FilesLayer) EndBatch(lane int) {
	l.endBatch(lane)
}

// fill copies what the answer needs out of req, whose bytes end with the batch.
func (j *fileJob) fill(req *corenet.HTTP1RequestValue) {
	p := req.Path[min(req.RoutePrefix, len(req.Path)):]
	j.slash = len(p) > 0 && p[len(p)-1] == '/'
	j.name = appendDecoded(j.name[:0], bytes.Trim(p, "/"))
	j.head = bytes.Equal(req.Method, methodHead)
	j.rangeSpec = copyField(j.rangeSpec[:0], req, "range")
	//: a conditional range this link does not evaluate is served whole.
	if _, ok := req.Field("if-range"); ok {
		j.rangeSpec = j.rangeSpec[:0]
	}
	j.ifNoneMatch = copyField(j.ifNoneMatch[:0], req, "if-none-match")
	j.ifModifiedSince = copyField(j.ifModifiedSince[:0], req, "if-modified-since")
}

// copyField appends the value of the named field to dst when it is present
// and within the cap, else leaves dst empty.
func copyField(dst []byte, req *corenet.HTTP1RequestValue, name string) []byte {
	v, ok := req.Field(name)
	//: absent, or past what any honest client sends.
	if !ok || len(v) > filesFieldCap {
		//: none.
		return dst
	}
	//: the value.
	return append(dst, v...)
}

// appendDecoded appends p, percent-decoded, to dst: the codec has already
// refused every escape of '/', '.', '\' and NUL and every malformed one.
func appendDecoded(dst, p []byte) []byte {
	//: a byte at a time.
	for i := 0; i < len(p); {
		c := p[i]
		//: an escape, its two digits validated by the codec.
		if c == '%' && i+2 < len(p) {
			dst = append(dst, unhex(p[i+1])<<4|unhex(p[i+2]))
			i += escapeLen
			continue
		}
		dst = append(dst, c)
		i++
	}
	//: decoded.
	return dst
}

// unhex is a hexadecimal digit's value.
func unhex(c byte) byte {
	//: by range; the codec let nothing else through.
	switch {
	case c >= 'a':
		//: a to f.
		return c - 'a' + 10
	case c >= 'A':
		//: A to F.
		return c - 'A' + 10
	default:
		//: 0 to 9.
		return c - '0'
	}
}

// laneOf returns lane i's table, built on its first request.
func (l *FilesLayer) laneOf(i int) *filesLane {
	//: the common case: the lane has been seen.
	if p := l.lanes.Load(); p != nil && i < len(*p) && (*p)[i] != nil {
		//: no lock taken.
		return (*p)[i]
	}
	//: once per lane.
	return l.growLane(i)
}

// growLane builds lane i's table and publishes a list that holds it.
func (l *FilesLayer) growLane(i int) *filesLane {
	l.mu.Lock()
	defer l.mu.Unlock()
	var cur []*filesLane
	//: the list as the other lanes left it.
	if p := l.lanes.Load(); p != nil {
		cur = *p
	}
	//: built meanwhile by nobody else, but checked the same.
	if i < len(cur) && cur[i] != nil {
		//: already there.
		return cur[i]
	}
	next := make([]*filesLane, max(len(cur), i+1))
	copy(next, cur)
	lane := &filesLane{}
	lane.grow()
	next[i] = lane
	l.lanes.Store(&next)
	//: published.
	return lane
}

// take returns a free job entry, growing the table when none is.
func (fl *filesLane) take() (uint32, *fileJob) {
	fl.mu.Lock()
	//: none free: the worker has them all.
	if len(fl.free) == 0 {
		fl.grow()
	}
	idx := fl.free[len(fl.free)-1]
	fl.free = fl.free[:len(fl.free)-1]
	fl.mu.Unlock()
	//: the entry, the worker's until it is given back.
	return idx, (*fl.table.Load())[idx]
}

// put gives entry idx back, on the worker or the lane's thread.
func (fl *filesLane) put(idx uint32) {
	fl.mu.Lock()
	fl.free = append(fl.free, idx)
	fl.mu.Unlock()
}

// grow doubles the table, the new entries free; mu held, or the lane new.
func (fl *filesLane) grow() {
	var cur []*fileJob
	//: the table so far.
	if p := fl.table.Load(); p != nil {
		cur = *p
	}
	n := len(cur)
	next := make([]*fileJob, n, max(2*n, filesEntries))
	copy(next, cur)
	//: the new entries, each with its buffers grown once.
	for i := n; i < cap(next); i++ {
		next = append(next, &fileJob{})
		fl.free = append(fl.free, uint32(i))
	}
	fl.table.Store(&next)
}

// CancelJob gives back the entry of a job that will never run: the loop adapter
// calls it when a queue refused the job or its connection closed first, and a
// link holding entries hands on a job that is not its own.
func (l *FilesLayer) CancelJob(lane int, job uint64) {
	//: another link's.
	if job&^filesEntryMask != l.tag {
		//: handed on when there is a link behind.
		if l.nextCanceller != nil {
			l.nextCanceller.CancelJob(lane, job)
		}
		return
	}
	l.laneOf(lane).put(uint32(job & filesEntryMask))
}

// WorkHTTP1 implements corenet.HTTP1Worker on the lane's worker: it resolves
// the file job this link queued, and hands any other job to the next handler's
// worker.
func (l *FilesLayer) WorkHTTP1(lane int, job uint64, res *corenet.HTTP1ResponseValue) {
	//: another link's, or the handler's.
	if job&^filesEntryMask != l.tag {
		//: a worker behind it, or a misuse of Pending.
		if l.nextWorker != nil {
			l.nextWorker.WorkHTTP1(lane, job, res)
		} else {
			res.Status = statusInternal
		}
		return
	}
	idx := uint32(job & filesEntryMask)
	fl := l.laneOf(lane)
	j := (*fl.table.Load())[idx]
	l.resolve(j, res)
	fl.put(idx)
}

// resolve fills res with what j's name is: a file, a directory's index, a
// redirect, or nothing. Every failure says as little as the status can.
func (l *FilesLayer) resolve(j *fileJob, res *corenet.HTTP1ResponseValue) {
	name := cleanName(string(j.name))
	res.Header = append(res.Header[:0], l.base...)
	h := l.openName(name)
	//: the name opened, or it did not.
	if h.found {
		l.resolveFound(j, res, name, h)
	} else {
		l.resolveAbsent(j, res, name, h)
	}
}

// resolveFound answers a name that opened: a directory, a regular file, or
// anything else, which is 404.
func (l *FilesLayer) resolveFound(j *fileJob, res *corenet.HTTP1ResponseValue, name string, h treeFile) {
	//: what the name is.
	switch {
	case h.info.IsDir():
		closeTreeFile(h.file)
		l.resolveDir(j, res, name)
	case h.info.Mode().IsRegular() && !j.slash:
		l.fileAnswer(j, res, name, h)
	default:
		closeTreeFile(h.file)
		res.Status = statusNotFound
	}
}

// resolveAbsent answers a name that did not open: the shell of a single-page
// application's route, a 404, or the tree's own failure.
func (l *FilesLayer) resolveAbsent(j *fileJob, res *corenet.HTTP1ResponseValue, name string, h treeFile) {
	//: what the failure was.
	switch {
	case h.err == fs.ErrNotExist && l.spaRoute(name):
		l.resolveShell(j, res)
	case h.err == fs.ErrNotExist:
		res.Status = statusNotFound
	default:
		res.Status = statusInternal
	}
}

// spaRoute reports whether name, naming nothing, is a route of a single-page
// application: a missing asset — anything with an extension — stays a 404.
func (l *FilesLayer) spaRoute(name string) bool {
	//: the shell answers only a path with no extension.
	return l.spa && path.Ext(name) == ""
}

// treeFile is an opened name.
type treeFile struct {
	file  fs.File
	info  fs.FileInfo
	found bool
	err   error
}

// openName opens name in the tree, confined: a tree that can name its links
// has every prefix of the name checked first, so a planted link inside the
// root cannot lead anywhere, the final file re-checked against what the open
// returned. A file that is not a regular one or a directory is refused before
// any open, which on a FIFO would block the worker.
func (l *FilesLayer) openName(name string) treeFile {
	lfs, canLink := l.fsys.(fs.ReadLinkFS)
	//: every prefix of the name, the root excepted.
	if canLink && name != "." {
		//: a link in the way, or a name that holds nothing.
		if err := checkLinks(lfs, name); err != nil {
			return treeFile{err: err}
		}
	}
	f, err := l.fsys.Open(name)
	//: the open failed: the name, or the tree?
	if err != nil {
		return treeFile{err: filesLookupErr(err)}
	}
	info, err := f.Stat()
	//: an entry that opened and cannot say what it is.
	if err != nil {
		closeTreeFile(f)
		return treeFile{err: err}
	}
	//: what was opened is what was checked: a swap after the check shows here.
	if canLink {
		if linfo, lerr := lfs.Lstat(name); lerr != nil || !sameFile(linfo, info) {
			closeTreeFile(f)
			return treeFile{err: fs.ErrNotExist}
		}
	}
	//: opened.
	return treeFile{file: f, info: info, found: true}
}

// checkLinks lstats every element of name, without following, and refuses a
// link (in or out of the root: the rule is the same) and anything but a
// regular file or a directory, before any open that would follow or block.
func checkLinks(lfs fs.ReadLinkFS, name string) error {
	//: each element of the name in turn.
	for i := 0; ; {
		next := strings.IndexByte(name[i:], '/')
		end := len(name)
		//: the next element's end, or the last one.
		if next >= 0 {
			end = i + next
		}
		info, err := lfs.Lstat(name[:end])
		//: nothing, or a name the file system refuses.
		if err != nil {
			return filesLookupErr(err)
		}
		//: a link, or a thing that is not a file or a directory.
		if info.Mode()&fs.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fs.ErrNotExist
		}
		//: the last element.
		if next < 0 {
			return nil
		}
		i += next + 1
	}
}

// sameFile compares two infos of the same name by their sizes, modification
// times and types: enough to catch a file swapped between check and open, the
// os.SameFile identity being refused to a tree that may be embed.FS.
func sameFile(a, b fs.FileInfo) bool {
	//: the same regular file or directory, by what a swap changes.
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode().Type() == b.Mode().Type()
}

// filesLookupErr maps a failed lookup: a name the file system refuses is
// absent, as the static tree's own rule has it, anything else the tree's.
func filesLookupErr(err error) error {
	//: nothing by that name, or a name it cannot hold.
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
		//: a 404.
		return fs.ErrNotExist
	}
	//: the tree's own failure: a 500.
	return err
}

// resolveDir serves a directory by its index.html: redirecting first to the
// name with a trailing slash, so its page's relative links resolve inside it.
// A directory with no index answers 404, never a listing.
func (l *FilesLayer) resolveDir(j *fileJob, res *corenet.HTTP1ResponseValue, name string) {
	index := filesIndex
	//: under the root the name is itself.
	if name != "." {
		index = name + "/" + filesIndex
	}
	h := l.openName(index)
	//: a regular index, or nothing.
	switch {
	case h.found && h.info.Mode().IsRegular():
		//: the name's slash first, the root has none to add.
		if !j.slash && name != "." {
			closeTreeFile(h.file)
			redirectSlash(res, name)
			return
		}
		l.fileAnswer(j, res, index, h)
	case h.found:
		closeTreeFile(h.file)
		res.Status = statusNotFound
	case h.err == fs.ErrNotExist:
		res.Status = statusNotFound
	default:
		res.Status = statusInternal
	}
}

// resolveShell answers the single-page application's shell for a route.
func (l *FilesLayer) resolveShell(j *fileJob, res *corenet.HTTP1ResponseValue) {
	h := l.openName(filesIndex)
	//: the shell, or nothing to serve.
	switch {
	case h.found && h.info.Mode().IsRegular():
		l.fileAnswer(j, res, filesIndex, h)
	case h.found:
		closeTreeFile(h.file)
		res.Status = statusNotFound
	case h.err == fs.ErrNotExist:
		res.Status = statusNotFound
	default:
		res.Status = statusInternal
	}
}

// redirectSlash sends a directory named without its slash to the name with
// one, as the static tree does: the Location starts with "./" and is one
// escaped segment, so no directory name reads as another host.
func redirectSlash(res *corenet.HTTP1ResponseValue, name string) {
	res.Status = statusMoved
	res.Header = append(res.Header, "Location: ./"...)
	res.Header = appendEscaped(res.Header, path.Base(name))
	res.Header = append(res.Header, "/\r\n"...)
}

// appendEscaped appends the bytes of s, percent-encoded but for a path
// segment's unreserved characters, as url.PathEscape does for a segment.
func appendEscaped(dst []byte, s string) []byte {
	//: byte by byte, ASCII unreserved kept.
	for i := range len(s) {
		c := s[i]
		//: kept as they are.
		if unreservedByte(c) {
			dst = append(dst, c)
			continue
		}
		dst = append(dst, '%', hexUpper[c>>4], hexUpper[c&nibbleLow])
	}
	//: encoded.
	return dst
}

// unreservedByte reports a letter, a digit or an unreserved punctuation mark.
func unreservedByte(c byte) bool {
	lower := c | 0x20
	//: RFC 3986's unreserved set.
	return (lower >= 'a' && lower <= 'z') || (c >= '0' && c <= '9') || strings.IndexByte(unreservedSet, c) >= 0
}

// fileAnswer writes h's response: the policies, the validators, 304 when they
// match, 206 for one Range, 416 for an impossible one, else the whole file.
func (l *FilesLayer) fileAnswer(j *fileJob, res *corenet.HTTP1ResponseValue, name string, h treeFile) {
	etag := appendETag(make([]byte, 0, 32), h.info)
	//: the validators answer before anything is sent.
	if j.notModified(etag, h.info) {
		closeTreeFile(h.file)
		res.Status = statusNotModified
		res.Header = l.appendCommon(res.Header, name, h.info, etag)
		return
	}
	ra, ranged := readerOf(h.file)
	size := h.info.Size()
	off, n, status := int64(0), size, statusOK
	//: one Range, GET only, on a tree that can seek.
	if len(j.rangeSpec) > 0 && !j.head && ranged {
		off, n, status = parseRange(j.rangeSpec, size)
	}
	//: a Range that cannot be satisfied: no body, what the size is.
	if status == statusRangeBad {
		closeTreeFile(h.file)
		res.Status = statusRangeBad
		res.Header = l.appendCommon(res.Header, name, h.info, etag)
		res.Header = append(res.Header, "Content-Range: bytes */"...)
		res.Header = strconv.AppendInt(res.Header, size, 10)
		res.Header = append(res.Header, "\r\n"...)
		return
	}
	res.Status = status
	res.Header = l.appendCommon(res.Header, name, h.info, etag)
	//: 206's own field.
	if status == statusPartial {
		res.Header = appendContentRange(res.Header, off, n, size)
	}
	//: an empty body has nothing to send.
	if n == 0 {
		closeTreeFile(h.file)
		return
	}
	res.File = corenet.HTTP1FileValue{R: ra, Off: off, Len: n}
}

// readerOf gives the file as an io.ReaderAt, seekable or sequential.
func readerOf(f fs.File) (io.ReaderAt, bool) {
	//: seekable: *os.File, an embed.FS file.
	if ra, ok := f.(io.ReaderAt); ok {
		//: the file itself.
		return ra, true
	}
	//: a tree that cannot seek: whole, never ranged.
	return &sequentialFile{f: f}, false
}

// sequentialFile is an fs.File read strictly in order as the copy path needs:
// every ReadAt continues where the previous stopped.
type sequentialFile struct {
	f fs.File
}

// ReadAt implements io.ReaderAt, ignoring off: the copy path reads in order.
func (s *sequentialFile) ReadAt(p []byte, _ int64) (int, error) {
	//: the file's own read.
	return s.f.Read(p)
}

// Close implements io.Closer.
func (s *sequentialFile) Close() error {
	//: the file's own close.
	return s.f.Close()
}

// appendCommon appends the fields of the answer: Cache-Control, Content-Type,
// Last-Modified, the validator and Accept-Ranges.
func (l *FilesLayer) appendCommon(dst []byte, name string, info fs.FileInfo, etag []byte) []byte {
	dst = append(dst, "Cache-Control: "...)
	//: a content-hashed name is cached for good.
	if l.immutable != nil && l.immutable(name) {
		dst = append(dst, static.ImmutableCacheControl...)
	} else {
		dst = append(dst, static.RevalidateCacheControl...)
	}
	dst = append(dst, "\r\nContent-Type: "...)
	dst = append(dst, contentTypeOf(name)...)
	dst = append(dst, "\r\nLast-Modified: "...)
	dst = info.ModTime().UTC().AppendFormat(dst, httpDate)
	dst = append(dst, "\r\nETag: "...)
	dst = append(dst, etag...)
	dst = append(dst, "\r\nAccept-Ranges: bytes\r\n"...)
	//: the fields.
	return dst
}

// contentTypeOf is the type of name: the pinned ones, then the host's table,
// else the type that promises nothing — nosniff means a browser will not guess.
func contentTypeOf(name string) string {
	ext := path.Ext(name)
	//: the types a page cannot run without.
	if t, ok := pinnedType(ext); ok {
		//: pinned.
		return t
	}
	//: the host's table.
	if t := mime.TypeByExtension(ext); t != "" {
		//: from the table.
		return t
	}
	//: unknown.
	return filesOctet
}

// pinnedType returns the Content-Type of the extensions a page cannot run
// without, the same on every host — the static tree's table, copied here
// because it is unexported there: a later change should share one table.
func pinnedType(extension string) (contentType string, pinned bool) {
	//: the extension, case-insensitively.
	switch strings.ToLower(extension) {
	case ".html", ".htm":
		//: a page.
		return "text/html; charset=utf-8", true
	case ".js", ".mjs":
		//: a script.
		return "text/javascript; charset=utf-8", true
	case ".css":
		//: a stylesheet.
		return "text/css; charset=utf-8", true
	case ".json", ".map":
		//: data, and a source map.
		return "application/json", true
	case ".wasm":
		//: streamed compilation needs this type.
		return "application/wasm", true
	case ".svg":
		//: a vector image.
		return "image/svg+xml", true
	case ".webmanifest":
		//: a web application manifest.
		return "application/manifest+json", true
	default:
		//: the host's or nothing.
		return "", false
	}
}

// appendETag appends the file's weak validator, from what stat says of it:
// its size and modification time, in hexadecimal.
func appendETag(dst []byte, info fs.FileInfo) []byte {
	dst = append(dst, `W/"`...)
	dst = strconv.AppendInt(dst, info.Size(), 16)
	dst = append(dst, '-')
	dst = strconv.AppendInt(dst, info.ModTime().UnixNano(), 16)
	dst = append(dst, '"')
	//: weak: stat cannot tell two byte-for-byte different revisions in the same tick apart.
	return dst
}

// notModified reports whether the client's validators say it holds this
// revision: If-None-Match first, by the weak comparison RFC 9110 §13.1.2
// requires, and only without one If-Modified-Since, at the second a date holds.
func (j *fileJob) notModified(etag []byte, info fs.FileInfo) bool {
	//: If-None-Match decides alone when present (RFC 9110 §13.1.3).
	if len(j.ifNoneMatch) > 0 {
		//: any value, or one of the list.
		return bytes.Equal(j.ifNoneMatch, wildcardMatch) || etagListed(j.ifNoneMatch, etag)
	}
	//: no conditional at all.
	if len(j.ifModifiedSince) == 0 {
		//: not modified means something was asked.
		return false
	}
	t, err := time.Parse(httpDate, string(j.ifModifiedSince))
	//: a date not parsed is ignored: the whole file is the safe answer.
	if err != nil {
		//: as if absent.
		return false
	}
	//: at the second a date holds.
	return !info.ModTime().Truncate(time.Second).After(t)
}

// etagListed reports whether etag is in the comma-separated list, compared
// weakly: a W/ prefix on either side changes nothing.
func etagListed(list, etag []byte) bool {
	etag = bytes.TrimPrefix(etag, []byte(`W/`))
	//: every member of the list.
	for len(list) > 0 {
		var v []byte
		v, list, _ = bytes.Cut(list, []byte(","))
		v = bytes.TrimPrefix(bytes.TrimSpace(v), []byte(`W/`))
		//: one match is enough.
		if bytes.Equal(v, etag) {
			//: the same revision.
			return true
		}
	}
	//: none.
	return false
}

// parseRange reads one "bytes=first-last", "bytes=first-" or "bytes=-suffix"
// over a file of size bytes and returns the range and its status: 206, 200
// for another unit or several ranges, which a server may ignore (RFC 9110
// §14.2), or 416 for one malformed or past the end.
func parseRange(spec []byte, size int64) (off, n int64, status int) {
	after, ok := bytes.CutPrefix(spec, []byte("bytes="))
	//: another unit, or several ranges: served whole.
	if !ok || bytes.IndexByte(after, ',') >= 0 {
		//: the whole file.
		return 0, size, statusOK
	}
	first, last, dash := bytes.Cut(after, []byte("-"))
	first, last = bytes.TrimSpace(first), bytes.TrimSpace(last)
	//: no dash, or neither side.
	if !dash || (len(first) == 0 && len(last) == 0) {
		//: malformed.
		return 0, 0, statusRangeBad
	}
	//: "-suffix": the last n bytes.
	if len(first) == 0 {
		//: the end of the file.
		return suffixRange(last, size)
	}
	//: "first-" or "first-last".
	return fromRange(first, last, size)
}

// suffixRange is the last v bytes of a file of size bytes: 416 for a suffix of
// nothing, an empty file, or not a number.
func suffixRange(last []byte, size int64) (off, n int64, status int) {
	v, ok := parseOffset(last)
	//: a suffix of nothing, or not a number, or nothing to give.
	if !ok || v == 0 || size == 0 {
		//: unsatisfiable.
		return 0, 0, statusRangeBad
	}
	n = min(v, size)
	//: the end of the file.
	return size - n, n, statusPartial
}

// fromRange reads "first-" or "first-last" over a file of size bytes.
func fromRange(first, last []byte, size int64) (off, n int64, status int) {
	from, ok := parseOffset(first)
	//: not a number, or past the end.
	if !ok || from >= size {
		//: unsatisfiable.
		return 0, 0, statusRangeBad
	}
	to := size - 1
	//: an upper bound too.
	if len(last) > 0 {
		v, ok := parseOffset(last)
		//: not a number, or before the start.
		if !ok || v < from {
			//: malformed.
			return 0, 0, statusRangeBad
		}
		to = min(v, size-1)
	}
	//: the range, inclusive.
	return from, to - from + 1, statusPartial
}

// parseOffset reads a run of decimal digits, refusing one past int64.
func parseOffset(b []byte) (int64, bool) {
	//: nothing, or more digits than an int64 can hold.
	if len(b) == 0 || len(b) > 19 {
		//: refused.
		return 0, false
	}
	var v int64
	//: digit by digit; 19 digits cannot wrap past 2^63 once checked below.
	for _, c := range b {
		//: a digit only.
		if c < '0' || c > '9' {
			//: refused.
			return 0, false
		}
		v = v*10 + int64(c-'0')
	}
	//: read; a value that wrapped is negative.
	return v, v >= 0
}

// appendContentRange appends a 206's Content-Range field.
func appendContentRange(dst []byte, off, n, size int64) []byte {
	dst = append(dst, "Content-Range: bytes "...)
	dst = strconv.AppendInt(dst, off, 10)
	dst = append(dst, '-')
	dst = strconv.AppendInt(dst, off+n-1, 10)
	dst = append(dst, '/')
	dst = strconv.AppendInt(dst, size, 10)
	//: the line's end.
	return append(dst, "\r\n"...)
}

// cleanName turns the decoded path under the route into a name of the tree,
// rooted so no ".." survives, made relative as io/fs requires; a name io/fs
// could not hold (invalid UTF-8) is left for the file system to refuse.
func cleanName(p string) string {
	name := strings.TrimPrefix(path.Clean("/"+p), "/")
	//: the route's root.
	if name == "" {
		//: io/fs's name for it.
		return "."
	}
	//: a valid io/fs name.
	return name
}

// closeTreeFile closes an opened file the answer does not keep.
func closeTreeFile(f fs.File) {
	//: nothing the client can be told about a read-only close.
	swallowErr(f.Close())
}
