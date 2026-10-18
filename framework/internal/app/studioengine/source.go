package studioengine

import (
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
)

// Limits of the source endpoint.
const (
	sourceContext  int = 12
	sourceMaxLines int = 400
)

// sourceLanguages are the files the source endpoint serves, by extension.
var sourceLanguages = map[string]string{
	".go": "go", ".js": "javascript", ".mjs": "javascript", ".ts": "typescript",
	".html": "xml", ".css": "css", ".json": "json",
}

// serveSource answers GET /_app/api/source?file=&module=&line=&end=: lines
// of a source file around a range. It serves only files the graph itself
// points at — never a path the request makes up —, as the host reads them:
// a regular file within its root, never a link (StudioHost.Source). So no
// request reaches .env, .git or the data directory.
func (s studio) serveSource(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	file, module := q.Get("file"), q.Get("module")
	notFound := func() {
		s.host.Reply(w, r, nil, s.host.NotFound("no such source in this product's graph"))
	}
	lang, ok := sourceLanguages[path.Ext(file)]
	if file == "" || !ok || !filepath.IsLocal(filepath.FromSlash(file)) {
		notFound()
		return
	}
	raw, ok := s.host.Source(r.Context(), module, file)
	if !ok {
		notFound()
		return
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	line := atoiOr(q.Get("line"), 1)
	end := atoiOr(q.Get("end"), line)
	line = min(max(line, 1), len(lines))
	end = min(max(end, line), len(lines), line+sourceMaxLines)
	first := max(1, line-sourceContext)
	last := min(len(lines), end+sourceContext)
	answer(s, w, r, model.Snippet{
		File: file, StartLine: first, Focus: line, FocusEnd: end, Lines: lines[first-1 : last], Language: lang,
	}, nil)
}

// atoiOr is text as a decimal integer, or fallback when it is not one: a
// query parameter the Studio may leave out.
func atoiOr(text string, fallback int) int {
	n, err := strconv.Atoi(text)
	if err != nil {
		return fallback
	}
	return n
}
