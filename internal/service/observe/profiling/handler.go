package profiling

import (
	"context"
	"net/http"
	"path"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"time"

	coreprofiling "github.com/kitsunium/sdk/internal/core/observe/profiling"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The pprof wire's endpoints, as net/http/pprof names them.
const (
	endpointProfile   string = "profile"
	endpointTrace     string = "trace"
	endpointGoroutine string = "goroutine"
	// secondsParam and debugParam are the query parameters pprof clients send.
	secondsParam string = "seconds"
	debugParam   string = "debug"
	// defaultProfileSeconds and defaultTraceSeconds are net/http/pprof's.
	defaultProfileSeconds int = 30
	defaultTraceSeconds   int = 1
	// maxLookupDebug is the highest debug a profile lookup takes; the
	// goroutine dump also takes maxGoroutineDebug, its stacks in full.
	maxLookupDebug    int = 1
	maxGoroutineDebug int = 2
	// The headers every response carries.
	headerContentType  string = "Content-Type"
	headerDisposition  string = "Content-Disposition"
	headerNoSniff      string = "X-Content-Type-Options"
	headerCacheControl string = "Cache-Control"
	contentBinary      string = "application/octet-stream"
	contentText        string = "text/plain; charset=utf-8"
	noSniff            string = "nosniff"
	noStore            string = "no-store"
)

// lookupProfiles are the runtime profiles served by name.
var lookupProfiles = [...]string{"heap", "allocs", "block", "mutex", "threadcreate", endpointGoroutine}

// HandlerConfig configures NewHandler.
type HandlerConfig struct {
	// Clock measures the profile and trace windows; nil is the wall clock.
	Clock clock.Waiter
}

// profileHandler serves the pprof wire from the engine's captures.
type profileHandler struct {
	// clk measures the windows.
	clk clock.Waiter
}

// NewHandler returns the pprof wire — what go tool pprof http://host/... reads
// — as a plain http.Handler the caller mounts where it chooses (under
// /debug/pprof/ of its own mux, behind its own authentication), with no
// init registration on http.DefaultServeMux, which importing net/http/pprof
// does. It routes on the last path element: profile?seconds= (CaptureCPU's
// window on cfg.Clock, 30 by default, the client's disconnect cancelling it),
// trace?seconds= (CaptureTrace's, 1 by default), heap, allocs, block, mutex,
// threadcreate and goroutine (debug= 0 or 1, the goroutine dump also 2), and
// an index at the mount point. cmdline (argv carries secrets) and symbol (the
// offline tool's job) are 404. Every response carries X-Content-Type-Options:
// nosniff and Cache-Control: no-store; a refusal is its sentinel's status with
// its Public text only — WindowInvalid 400, ProfilerBusy and TraceBusy 409,
// CaptureCanceled 503, CaptureFailed 500.
func NewHandler(cfg HandlerConfig) http.Handler {
	clk := cfg.Clock
	//: the wall clock by default.
	if clk == nil {
		clk = clock.System
	}
	//: no mux, no prefix of its own.
	return &profileHandler{clk: clk}
}

// ServeHTTP routes by the last path element, wherever the caller mounted it.
func (h *profileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(headerNoSniff, noSniff)
	w.Header().Set(headerCacheControl, noStore)
	name := path.Base(r.URL.Path)
	switch {
	//: the mount point itself.
	case strings.HasSuffix(r.URL.Path, "/"):
		serveIndex(w)
	//: a CPU profile over a window.
	case name == endpointProfile:
		h.serveWindow(w, r, name, defaultProfileSeconds, captureCPURaw)
	//: an execution trace over a window.
	case name == endpointTrace:
		h.serveWindow(w, r, name, defaultTraceSeconds, captureTrace)
	//: a runtime profile by name.
	case slices.Contains(lookupProfiles[:], name):
		serveLookup(w, r, name)
	//: cmdline (argv carries secrets) and symbol (the offline tool's) are not served.
	default:
		http.NotFound(w, r)
	}
}

// serveIndex lists what the handler serves.
func serveIndex(w http.ResponseWriter) {
	w.Header().Set(headerContentType, contentText)
	var index strings.Builder
	//: one name per line, the windowed ones first.
	for _, name := range append([]string{endpointProfile, endpointTrace}, lookupProfiles[:]...) {
		index.WriteString(name)
		index.WriteByte('\n')
	}
	//: a client gone mid-answer is told nothing more.
	if _, err := w.Write([]byte(index.String())); err != nil {
		return
	}
}

// serveWindow captures over ?seconds= on the handler's clock; the client's
// disconnect cancels the window.
func (h *profileHandler) serveWindow(
	w http.ResponseWriter, r *http.Request, name string, defaultSeconds int,
	capture func(context.Context, clock.Waiter, time.Duration) ([]byte, error),
) {
	seconds, err := intParam(r, secondsParam, defaultSeconds)
	//: not a number of seconds.
	if err != nil {
		refuse(w, errs.Wrap(coreprofiling.WindowInvalid, errs.WrapParams{}, errs.String(secondsParam, r.URL.Query().Get(secondsParam))))
		return
	}
	data, err := capture(r.Context(), h.clk, time.Duration(seconds)*time.Second)
	//: refused, cancelled or failed.
	if err != nil {
		refuse(w, err)
		return
	}
	attach(w, name)
	//: a client gone mid-answer is told nothing more.
	if _, werr := w.Write(data); werr != nil {
		return
	}
}

// serveLookup writes runtime/pprof's profile name at ?debug= (0 binary, 1
// text; the goroutine dump also 2).
func serveLookup(w http.ResponseWriter, r *http.Request, name string) {
	debug, err := intParam(r, debugParam, 0)
	ceiling := maxLookupDebug
	//: the goroutine dump in full.
	if name == endpointGoroutine {
		ceiling = maxGoroutineDebug
	}
	//: a debug level the runtime does not write is clamped, not refused.
	if err != nil || debug < 0 {
		debug = 0
	}
	debug = min(debug, ceiling)
	//: binary or text.
	if debug == 0 {
		attach(w, name)
	} else {
		w.Header().Set(headerContentType, contentText)
	}
	//: the runtime writes it.
	if werr := pprof.Lookup(name).WriteTo(w, debug); werr != nil {
		refuse(w, errs.Wrap(werr, errs.WrapParams{
			Code:    coreprofiling.CodeCaptureFailed,
			Reason:  "CAPTURE_FAILED",
			Public:  coreprofiling.CaptureFailed.Public(),
			Private: coreprofiling.CaptureFailed.Private(),
		}, errs.String("profile", name)))
	}
}

// attach marks a binary download, as net/http/pprof does.
func attach(w http.ResponseWriter, name string) {
	w.Header().Set(headerContentType, contentBinary)
	w.Header().Set(headerDisposition, `attachment; filename="`+name+`"`)
}

// refuse answers err's status with its Public text only.
func refuse(w http.ResponseWriter, err error) {
	//: the sentinel's status, never its private text.
	http.Error(w, errs.PublicOf(err), errs.HTTPStatusOf(err))
}

// intParam reads an integer query parameter, def when absent.
func intParam(r *http.Request, name string, def int) (int, error) {
	raw := r.URL.Query().Get(name)
	//: absent: the default.
	if raw == "" {
		return def, nil
	}
	//: the number, or why not.
	return strconv.Atoi(raw)
}
