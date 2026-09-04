// Command sdkguard — the SDK freshness probe.
//
// A consumer pinned to an old SDK is the quiet cousin of the defects the rules
// catch: nothing fails, and the fix that shipped upstream simply never arrives.
// It matters more here than in most libraries because of how this SDK versions
// (ADR 0007): a patch release is cut whenever an `internal/*` package that
// `pkg` depends on changes, so patches carry fixes that never touch the public
// API. A consumer reading only the changelog of exported symbols sees nothing
// and concludes there is nothing to take.
//
// This is a WARNING and never an error by default. Being behind is not a
// violation — it is a fact the maintainer may already know and may have
// decided to live with. Teams that want it to gate say so with
// -version-check=error.
package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// sdkModule is the SDK's only consumer-facing module (ADR 0017: the bare
// `…/pkg`, because Go forbids a `/v1` module-path suffix).
const sdkModule string = "github.com/kitsunium/sdk/pkg"

// defaultProxy is what the go command uses when GOPROXY is unset.
const defaultProxy string = "https://proxy.golang.org"

// maxListBytes caps the proxy response. The SDK has tens of tags; a response
// larger than this is a misbehaving proxy, not a version list.
const maxListBytes int64 = 1 << 20

// probeTimeout bounds the network call. A linter that hangs on a firewalled
// runner is worse than one that skips the check: the first blocks a pipeline,
// the second only misses a nudge.
const probeTimeout time.Duration = 3 * time.Second

// versionCheckWarn reports an outdated SDK without changing the exit code.
const versionCheckWarn string = "warn"

// versionCheckOff skips the probe entirely — no network call is made.
const versionCheckOff string = "off"

// versionCheckError makes an outdated SDK fail the run, for teams that want
// freshness gated rather than suggested.
const versionCheckError string = "error"

// moduleRef is the SDK requirement found in a consumer's go.mod.
type moduleRef struct {
	// Version is the required version, e.g. "v0.1.24".
	Version string
	// Replaced reports whether a replace directive redirects the module. A
	// replaced module is someone working against a local checkout; telling them
	// to upgrade would be noise.
	Replaced bool
}

// probe fetches the version list from a module proxy. The proxy and client are
// fields rather than globals so a test can point it at an httptest server and
// never touch the network.
type probe struct {
	// proxy is the base URL of the module proxy.
	proxy string
	// client bounds the request; nil falls back to a timeout-bounded default.
	client *http.Client
}

// checkVersion returns the warning text for an outdated SDK, and whether the
// SDK was found to be outdated at all.
//
// Every failure path returns silently: no go.mod, no SDK requirement, a
// replace directive, GOPROXY=off, no network, a malformed response. A
// freshness nudge that breaks a build has failed at being a nudge.
func checkVersion(dir string, p probe) (string, bool) {
	ref, ok := sdkRequirement(dir)
	if !ok || ref.Replaced {
		return "", false
	}
	versions, err := p.versions(sdkModule)
	if err != nil {
		return "", false
	}
	newer := newerThan(ref.Version, versions)
	if len(newer) == 0 {
		return "", false
	}
	return versionNotice(ref.Version, newer), true
}

// sdkRequirement finds the SDK requirement by walking up from dir to the
// nearest go.mod.
//
// go.mod is parsed by hand rather than with golang.org/x/mod/modfile: this
// module is stdlib-only by a build constraint, not a preference (see
// tools/CLAUDE.md). The require/replace grammar needed here is a handful of
// line shapes, and a parse that fails simply yields no warning.
func sdkRequirement(dir string) (moduleRef, bool) {
	path, ok := findGoMod(dir)
	if !ok {
		return moduleRef{}, false
	}
	data, err := os.ReadFile(path) //nolint:gosec // path comes from walking the scan root, not from input
	if err != nil {
		return moduleRef{}, false
	}

	return parseGoMod(string(data))
}

// parseGoMod extracts the SDK requirement from go.mod's text.
//
// Both the single-line and the parenthesised block forms are handled, and they
// must be told apart: a naive reader that treats every line as a require turns
// the block line "github.com/kitsunium/sdk/pkg => ../sdk/pkg" into a
// requirement on the version "=>", which then compares older than every real
// tag and produces a confident, wrong "you are 25 releases behind".
func parseGoMod(text string) (moduleRef, bool) {
	var ref moduleRef
	section := ""
	for _, raw := range strings.Split(text, "\n") {
		// go.mod comments start with //; strip them so a "// indirect" marker
		// or a commented-out directive cannot be read as content.
		line := strings.TrimSpace(raw)
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}

		switch {
		// Opening a block sets the context for the lines that follow.
		case strings.HasPrefix(line, "require ("):
			section = "require"
		case strings.HasPrefix(line, "replace ("):
			section = "replace"
		// A lone ")" closes whichever block was open.
		case line == ")":
			section = ""
		// The single-line forms carry their own keyword, so they are read
		// directly and leave the surrounding context untouched.
		case strings.HasPrefix(line, "require "):
			readRequire(&ref, strings.TrimPrefix(line, "require "))
		case strings.HasPrefix(line, "replace "):
			readReplace(&ref, strings.TrimPrefix(line, "replace "))
		// Anything else belongs to the open block, if there is one.
		case section == "require":
			readRequire(&ref, line)
		case section == "replace":
			readReplace(&ref, line)
		}
	}
	return ref, ref.Version != ""
}

// readRequire records the SDK version from a "<path> <version>" entry.
func readRequire(ref *moduleRef, entry string) {
	fields := strings.Fields(entry)
	if len(fields) < 2 || fields[0] != sdkModule {
		return
	}
	// Guard the version's shape: it keeps a malformed line from being read as
	// a version, which is what would make the comparison silently absurd.
	if !strings.HasPrefix(fields[1], "v") {
		return
	}
	ref.Version = fields[1]
}

// readReplace records that the SDK is redirected, but only when it is the
// LEFT side of the arrow.
//
// The direction matters: "replace example.com/fork => github.com/kitsunium/sdk/pkg"
// mentions the SDK without replacing it, and reading that as a replacement
// would silently suppress the freshness warning for a consumer who is genuinely
// behind.
func readReplace(ref *moduleRef, entry string) {
	lhs, _, found := strings.Cut(entry, "=>")
	if !found {
		return
	}
	fields := strings.Fields(lhs)
	if len(fields) > 0 && fields[0] == sdkModule {
		ref.Replaced = true
	}
}

// findGoMod walks up from dir looking for a go.mod, so the tool works from a
// package subdirectory the way every other Go tool does.
func findGoMod(dir string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		candidate := filepath.Join(abs, "go.mod")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, true
		}
		parent := filepath.Dir(abs)
		// filepath.Dir is idempotent at the filesystem root; that is the stop.
		if parent == abs {
			return "", false
		}
		abs = parent
	}
}

// versions lists the module's tagged releases through the proxy.
func (p probe) versions(module string) ([]string, error) {
	base := p.proxy
	if base == "" {
		resolved, enabled := resolveProxy()
		if !enabled {
			return nil, errProxyDisabled
		}
		base = resolved
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: probeTimeout}
	}

	resp, err := client.Get(base + "/" + module + "/@v/list")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errProxyDisabled
	}

	// Bound the read: a proxy is a third party, and an unbounded body from one
	// would be a denial-of-service on the linter.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes))
	if err != nil {
		return nil, err
	}

	var out []string
	for _, line := range strings.Fields(string(body)) {
		// Only stable releases are candidates: suggesting a prerelease or a
		// pseudo-version as "the latest" would be wrong advice.
		if _, _, _, ok := parseSemver(line); ok {
			out = append(out, line)
		}
	}
	return out, nil
}

// resolveProxy reads GOPROXY the way the go command does, and reports whether
// a proxy is usable at all.
func resolveProxy() (string, bool) {
	raw := strings.TrimSpace(os.Getenv("GOPROXY"))
	if raw == "" {
		return defaultProxy, true
	}
	// GOPROXY is a fallback list separated by "," or "|".
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '|' }) {
		entry = strings.TrimSpace(entry)
		switch {
		// "off" forbids any module download; honour it rather than reaching out.
		case entry == "off":
			return "", false
		// "direct" means VCS access, which this probe deliberately does not do.
		case entry == "direct" || entry == "":
			continue
		case strings.HasPrefix(entry, "http://") || strings.HasPrefix(entry, "https://"):
			return strings.TrimSuffix(entry, "/"), true
		}
	}
	return "", false
}

// newerThan returns the released versions strictly newer than cur, oldest
// first.
func newerThan(cur string, versions []string) []string {
	var out []string
	for _, v := range versions {
		if semverLess(cur, v) {
			out = append(out, v)
		}
	}
	sortVersions(out)
	return out
}

// sortVersions orders versions ascending by semver. Insertion sort keeps the
// dependency list empty and the input is a handful of tags.
func sortVersions(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && semverLess(v[j], v[j-1]); j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// parseSemver splits a strict "vMAJOR.MINOR.PATCH" tag. Anything carrying a
// prerelease or build suffix is rejected, which is what keeps pseudo-versions
// out of the candidate set.
func parseSemver(v string) (major, minor, patch int, ok bool) {
	if !strings.HasPrefix(v, "v") {
		return 0, 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, 0, 0, false
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], true
}

// semverLess reports whether a sorts before b. An unparseable version sorts
// first, so a consumer on a pseudo-version is told about every real release.
func semverLess(a, b string) bool {
	amaj, amin, apatch, aok := parseSemver(a)
	bmaj, bmin, bpatch, bok := parseSemver(b)
	if !bok {
		return false
	}
	if !aok {
		return true
	}
	if amaj != bmaj {
		return amaj < bmaj
	}
	if amin != bmin {
		return amin < bmin
	}
	return apatch < bpatch
}

// versionNotice renders the warning. It names the gap's kind because that is
// what tells a reader how urgent this is, and it names the exact go get line
// because a nudge without a next step is just noise.
func versionNotice(cur string, newer []string) string {
	latest := newer[len(newer)-1]
	var b strings.Builder
	b.WriteString("warning: the SDK is " + strconv.Itoa(len(newer)) + " " +
		gapKind(cur, latest) + " behind — go.mod requires " + cur + ", latest is " + latest + ".\n")
	// The reason a patch matters here is specific to this SDK's release policy,
	// and it is the part a consumer cannot infer from a changelog of exported
	// symbols.
	b.WriteString("  A patch is cut whenever an internal package pkg depends on changes (ADR 0007),\n")
	b.WriteString("  so releases carry fixes that never alter the public API.\n")
	b.WriteString("  Update:  go get " + sdkModule + "@" + latest + "\n")
	b.WriteString("  Silence: -version-check=off")
	return b.String()
}

// gapKind names the largest version component that changed, pluralised for the
// count the caller prints before it.
func gapKind(cur, latest string) string {
	cmaj, cmin, _, cok := parseSemver(cur)
	lmaj, lmin, _, lok := parseSemver(latest)
	if !cok || !lok {
		return "releases"
	}
	switch {
	case lmaj != cmaj:
		return "releases, crossing a MAJOR version,"
	case lmin != cmin:
		return "releases, crossing a minor version,"
	default:
		return "patch releases"
	}
}
