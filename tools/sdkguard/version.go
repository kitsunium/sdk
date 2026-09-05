// Package main — the SDK freshness probe.
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
	"errors"
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

// sectionNone means no block is currently open.
const sectionNone gomodSection = 0

// sectionRequire means the reader is inside a require block.
const sectionRequire gomodSection = 1

// sectionReplace means the reader is inside a replace block.
const sectionReplace gomodSection = 2

// requireFields is the smallest number of fields a require entry carries.
const requireFields int = 2

// requirePath indexes the module path in a require entry.
const requirePath int = 0

// requireVersion indexes the version in a require entry.
const requireVersion int = 1

// semverParts is the number of components in a strict release tag.
const semverParts int = 3

// semverMajor indexes the major component of a parsed tag.
const semverMajor int = 0

// semverMinor indexes the minor component of a parsed tag.
const semverMinor int = 1

// semverPatch indexes the patch component of a parsed tag.
const semverPatch int = 2

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

// gomodSection names the go.mod block a line belongs to.
//
// A named type rather than a string: "" would be a sentinel meaning "no block
// open", and a sentinel is exactly what a reader has to memorise. sectionNone
// says it.
type gomodSection int

// moduleRef is the SDK requirement found in a consumer's go.mod.
type moduleRef struct {
	// Version is the required version, e.g. "v0.1.24".
	Version string
	// replaceVersions holds the LEFT-SIDE version of every replace directive
	// naming the SDK, with the empty string standing for an unversioned one.
	//
	// The distinction is load-bearing. `replace <mod> => ../mod` redirects every
	// version; `replace <mod> v0.1.9 => ../mod` redirects only v0.1.9 and leaves
	// every other version resolving normally. Treating the second as if it were
	// the first silences the freshness check for a build that is not, in fact,
	// using a local checkout — and inside a workspace it silences it for every
	// sibling module too.
	replaceVersions []string
}

// Replaced reports whether a replace directive actually redirects the version
// this module requires.
//
// A replaced module is someone working against a local checkout, and telling
// them to upgrade would be noise. A module pinned by a directive for some OTHER
// version is not that, and still wants the nudge.
func (r moduleRef) Replaced() bool {
	//: the require line may be read after the replace, so the comparison
	//: happens here rather than while parsing.
	for _, at := range r.replaceVersions {
		//: an unversioned directive redirects every version there is.
		if at == "" || at == r.Version {
			//: this build genuinely resolves the SDK locally.
			return true
		}
	}
	//: every directive named a version this module does not require.
	return false
}

// checkVersion returns the warning text for an outdated SDK, and whether the
// SDK was found to be outdated at all.
//
// Every failure path returns silently: no go.mod, no SDK requirement, a
// replace directive, GOPROXY=off, no network, a malformed response. A
// freshness nudge that breaks a build has failed at being a nudge.
func checkVersion(dir string, p probe) (string, bool) {
	ref, ok := sdkRequirement(dir)
	//: a workspace root has no go.mod of its own, so the documented
	//: `sdkguard ./...` form from there would scan every module and warn about
	//: none of them.
	if !ok {
		//: fall back to the modules go.work lists.
		ref, ok = workspaceRequirement(dir)
	}
	//: no requirement, or one the build replaces locally — say nothing either
	//: way: a replace means someone is working against a checkout.
	if !ok || ref.Replaced() {
		//: silence, which is the documented behaviour of every failure path.
		return "", false
	}
	versions, err := p.versions(sdkModule)
	//: no proxy, no network, a bad response — none of them is worth a warning.
	if err != nil {
		//: a nudge that breaks a build has failed at being a nudge.
		return "", false
	}
	newer := newerThan(ref.Version, versions)
	//: up to date, or ahead of the proxy on an unreleased tag.
	if len(newer) == 0 {
		//: nothing to advise.
		return "", false
	}
	//: behind, with the gap counted so the notice can name its size.
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
	//: no go.mod above this directory; the caller may still try go.work.
	if !ok {
		//: nothing to read.
		return moduleRef{}, false
	}
	data, err := os.ReadFile(path) //nolint:gosec // path comes from walking the scan root, not from input
	//: an unreadable go.mod is not this tool's problem to report.
	if err != nil {
		//: silence, like every other degraded path.
		return moduleRef{}, false
	}

	//: the grammar lives in parseGoMod; this function only located the file.
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
	section := sectionNone
	//: SplitSeq walks the lines without materialising the whole slice.
	for raw := range strings.SplitSeq(text, "\n") {
		//: a blank or comment-only line carries nothing to read.
		line := stripComment(raw)
		//: a blank or comment-only line carries nothing to read.
		if line == "" {
			//: nothing on this line survives the comment strip.
			continue
		}
		//: one line at a time, carrying the open block across iterations.
		section = readGoModLine(&ref, section, line)
	}
	//: a requirement was found only when a version was actually recorded.
	return ref, ref.Version != ""
}

// stripComment trims a go.mod line down to its content.
//
// go.mod comments start with //, so a "// indirect" marker or a commented-out
// directive must not be read as content — the latter would otherwise register
// a replace that the build does not apply.
func stripComment(raw string) string {
	line := strings.TrimSpace(raw)
	//: Cut says "before, and whether there was a marker" in one call, which
	//: is exactly the question; Index made the caller re-derive it.
	before, _, found := strings.Cut(line, "//")
	//: a marker anywhere on the line ends its content there.
	if found {
		//: keep only what precedes the marker, re-trimmed.
		return strings.TrimSpace(before)
	}
	//: no comment on this line.
	return line
}

// readGoModLine folds one line into ref and returns the block still open.
//
// Split out of parseGoMod so each function states one thing: this one knows
// the grammar, the caller knows the iteration.
func readGoModLine(ref *moduleRef, section gomodSection, line string) gomodSection {
	//: block openers first, then the single-line forms, then block content.
	switch {
	//: opening a block sets the context for the lines that follow.
	case strings.HasPrefix(line, "require ("):
		//: every following line is a requirement until ")".
		return sectionRequire
	//: the replace block, read the same way.
	case strings.HasPrefix(line, "replace ("):
		//: every following line is a replacement until ")".
		return sectionReplace
	//: a lone ")" closes whichever block was open.
	case line == ")":
		//: back to the top level, where a keyword is required again.
		return sectionNone
	//: the single-line forms carry their own keyword, so they are read
	//: directly and leave the surrounding context untouched.
	case strings.HasPrefix(line, "require "):
		readRequire(ref, strings.TrimPrefix(line, "require "))
	//: the single-line replace, likewise.
	case strings.HasPrefix(line, "replace "):
		readReplace(ref, strings.TrimPrefix(line, "replace "))
	//: anything else belongs to the open block, if there is one.
	case section == sectionRequire:
		readRequire(ref, line)
	//: inside a replace block, every line is a replacement.
	case section == sectionReplace:
		readReplace(ref, line)
	}
	//: the block context is unchanged by a content line.
	return section
}

// readRequire records the SDK version from a "<path> <version>" entry.
func readRequire(ref *moduleRef, entry string) {
	fields := strings.Fields(entry)
	//: three conditions, one effect — the entry is not an SDK requirement this
	//: probe can compare, whether it is too short, names another module, or
	//: carries something that is not a version. Reading a malformed line as a
	//: version is what would make the comparison silently absurd.
	if len(fields) < requireFields ||
		fields[requirePath] != sdkModule ||
		!strings.HasPrefix(fields[requireVersion], "v") {
		//: nothing to record.
		return
	}
	//: the version this build requires, ready to compare against the roster.
	ref.Version = fields[requireVersion]
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
	//: an entry with no arrow is not a replacement.
	if !found {
		//: nothing to record.
		return
	}
	fields := strings.Fields(lhs)
	//: only the LEFT side names what is being replaced; the SDK appearing on
	//: the right means some other module was redirected TO it.
	if len(fields) == 0 || fields[0] != sdkModule {
		//: this directive names another module.
		return
	}
	var at string
	//: a second field on the left is the version the directive is scoped to;
	//: its absence means it applies to every version, which is what the empty
	//: string records.
	if len(fields) > 1 {
		//: scoped to exactly this version.
		at = fields[1]
	}
	ref.replaceVersions = append(ref.replaceVersions, at)
}

// workspaceRequirement resolves the SDK requirement through a go.work file.
//
// It reports the OLDEST requirement across the workspace's modules: that is
// the one a reader must act on, and warning about the newest would let a stale
// module hide behind an up-to-date sibling.
func workspaceRequirement(dir string) (moduleRef, bool) {
	work, found := findUp(dir, "go.work")
	//: no workspace either; there is nothing left to resolve through.
	if !found {
		//: the caller already tried go.mod.
		return moduleRef{}, false
	}
	data, err := os.ReadFile(work) //nolint:gosec // path comes from walking the scan root, not from input
	//: an unreadable go.work is not this tool's problem to report.
	if err != nil {
		//: silence, like every other degraded path.
		return moduleRef{}, false
	}

	root := filepath.Dir(work)
	var oldest moduleRef
	//: use paths are relative to the workspace root, not the scan root.
	for _, rel := range workspaceUses(string(data)) {
		ref, ok := sdkRequirement(filepath.Join(root, rel))
		//: a module that does not depend on the SDK has nothing to say.
		if !ok {
			//: skip it and keep looking.
			continue
		}
		//: a replaced module anywhere means someone is working locally; say
		//: nothing rather than nag about a version the build does not use.
		//: Replaced() weighs the directive's own version, so a pin for some
		//: other version does not silence the sibling that is genuinely behind.
		if ref.Replaced() {
			//: one local checkout silences the whole workspace.
			return moduleRef{}, false
		}
		//: the OLDEST wins: warning about the newest would let a stale module
		//: hide behind an up-to-date sibling.
		if oldest.Version == "" || semverLess(ref.Version, oldest.Version) {
			oldest = ref
		}
	}
	//: a requirement was found only when some module named one.
	return oldest, oldest.Version != ""
}

// workspaceUses lists the directories a go.work file declares, handling both
// the single-line and the parenthesised block forms.
func workspaceUses(text string) []string {
	var dirs []string
	inBlock := false
	//: SplitSeq walks the lines without materialising the whole slice.
	for raw := range strings.SplitSeq(text, "\n") {
		line := stripComment(raw)
		//: go.work has the same two shapes go.mod does: block, or single line.
		switch {
		//: a blank or comment-only line carries nothing.
		case line == "":
		//: opening the block; every following line is a directory.
		case strings.HasPrefix(line, "use ("):
			inBlock = true
		//: closing it; a keyword is required again below.
		case line == ")":
			inBlock = false
		//: the single-line form carries its own keyword.
		case strings.HasPrefix(line, "use "):
			dirs = append(dirs, strings.Trim(strings.TrimPrefix(line, "use "), `"`))
		//: anything else inside the block is a directory.
		case inBlock:
			dirs = append(dirs, strings.Trim(line, `"`))
		}
	}
	//: the module directories this workspace declares.
	return dirs
}

// findGoMod walks up from dir looking for a go.mod, so the tool works from a
// package subdirectory the way every other Go tool does.
func findGoMod(dir string) (string, bool) {
	//: the walk itself is shared with the go.work lookup.
	return findUp(dir, "go.mod")
}

// findUp walks up from dir looking for name.
func findUp(dir, name string) (string, bool) {
	abs, err := filepath.Abs(dir)
	//: an unresolvable path cannot be walked up from.
	if err != nil {
		//: nothing found, and nothing to complain about.
		return "", false
	}
	//: climb until the file turns up or the filesystem root is reached.
	for {
		candidate := filepath.Join(abs, name)
		//: the first hit wins, which is the nearest enclosing module.
		if _, statErr := os.Stat(candidate); statErr == nil {
			//: found it.
			return candidate, true
		}
		parent := filepath.Dir(abs)
		//: filepath.Dir is idempotent at the filesystem root; that is the stop.
		if parent == abs {
			//: walked the whole way up without finding it.
			return "", false
		}
		abs = parent
	}
}

// versions lists the module's tagged releases, walking the GOPROXY fallback
// list until one proxy answers.
//
// Trying only the first entry made the documented fallback support a fiction:
// a configuration whose primary proxy is unreachable would silently lose the
// freshness check rather than fall through to its backup, which is the entire
// reason the list exists.
func (p probe) versions(module string) (releases []string, err error) {
	bases := []string{p.proxy}
	//: an explicit proxy is a test stub; otherwise read the environment.
	if p.proxy == "" {
		resolved, enabled := resolveProxies()
		//: GOPROXY=off, or a list naming nothing callable.
		if !enabled {
			//: no request is made at all.
			return nil, errProxyDisabled
		}
		bases = resolved
	}
	client := p.client
	//: a nil client means production; the timeout is what keeps a firewalled
	//: runner from hanging the whole lint.
	if client == nil {
		client = &http.Client{Timeout: probeTimeout}
	}

	lastErr := errProxyDisabled
	//: walk the list in order, exactly as the go command would.
	for _, base := range bases {
		out, fetchErr := fetchVersions(client, base, module)
		//: the first proxy that answers settles it.
		if fetchErr == nil {
			//: no need to try the backups.
			return out, nil
		}
		lastErr = fetchErr
	}
	//: every entry failed; report the last cause rather than a bare nil.
	return nil, lastErr
}

// fetchVersions asks one proxy for a module's version list.
func fetchVersions(client *http.Client, base, module string) (releases []string, err error) {
	resp, err := client.Get(base + "/" + module + "/@v/list")
	//: a transport failure is the caller's cue to try the next proxy.
	if err != nil {
		//: forward it so the fallback loop can record the last cause.
		return nil, err
	}
	//: close in a named helper so the error is joined rather than dropped —
	//: a leaked body on a reused transport is a connection that never returns.
	defer func() {
		//: join keeps a close failure visible without masking a read failure.
		err = errors.Join(err, resp.Body.Close())
	}()
	//: anything but 200 means this proxy cannot answer for the module.
	if resp.StatusCode != http.StatusOK {
		//: treat it as unavailable so the next entry gets a turn.
		return nil, errProxyDisabled
	}

	//: bound the read: a proxy is a third party, and an unbounded body from one
	//: would be a denial-of-service on the linter.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes))
	//: a truncated or failed read leaves nothing worth parsing.
	if err != nil {
		//: the deferred close joins its own error onto this one.
		return nil, err
	}

	//: only stable releases are candidates: suggesting a prerelease or a
	//: pseudo-version as "the latest" would be wrong advice.
	for line := range strings.FieldsSeq(string(body)) {
		//: keep the tag only when it parses as a strict vX.Y.Z release.
		if _, _, _, ok := parseSemver(line); ok {
			//: a candidate the caller can compare against the requirement.
			releases = append(releases, line)
		}
	}
	//: the named return carries any close error joined by the defer above.
	return releases, err
}

// resolveProxies reads GOPROXY the way the go command does and returns every
// usable proxy in order, reporting whether any exists at all.
func resolveProxies() ([]string, bool) {
	raw := strings.TrimSpace(os.Getenv("GOPROXY"))
	//: unset means the go command's own default, not "no proxy".
	if raw == "" {
		//: the same endpoint a plain `go get` would reach.
		return []string{defaultProxy}, true
	}
	var out []string
	//: GOPROXY is a fallback list separated by "," or "|".
	for _, entry := range strings.FieldsFunc(raw, isProxySeparator) {
		url, usable, stop := classifyProxy(entry)
		//: "off" forbids any download; entries after it are unreachable by
		//: the go command too, so stop rather than reaching out.
		if stop {
			//: no proxy is permitted at all.
			return nil, false
		}
		//: skip the entries that name no HTTP endpoint.
		if !usable {
			//: "direct" and empty entries carry nothing this probe can call.
			continue
		}
		out = append(out, url)
	}
	//: enabled only when at least one endpoint survived.
	return out, len(out) > 0
}

// isProxySeparator reports whether r separates two GOPROXY entries.
func isProxySeparator(r rune) bool {
	//: the go command accepts both spellings of the fallback list.
	return r == ',' || r == '|'
}

// classifyProxy reads one GOPROXY entry, reporting the URL it names, whether
// that URL is usable, and whether it forbids proxying outright.
func classifyProxy(entry string) (url string, usable, stop bool) {
	entry = strings.TrimSpace(entry)
	//: two keywords and a scheme are the only spellings that mean anything.
	switch {
	//: "off" forbids any module download.
	case entry == "off":
		//: signal the caller to stop scanning the list entirely.
		return "", false, true
	//: "direct" means VCS access, which this probe deliberately does not do.
	case entry == "direct" || entry == "":
		//: nothing callable here, but the list continues.
		return "", false, false
	//: an http(s) entry is the only kind this probe can dial.
	case strings.HasPrefix(entry, "http://") || strings.HasPrefix(entry, "https://"):
		//: a usable endpoint; the trailing slash would double up on join.
		return strings.TrimSuffix(entry, "/"), true, false
	//: an entry naming neither a scheme nor a keyword is not addressable.
	default:
		//: skip it the way the go command skips what it cannot dial.
		return "", false, false
	}
}

// newerThan returns the released versions strictly newer than cur, oldest
// first.
func newerThan(cur string, versions []string) []string {
	var newer []string
	//: keep only the tags that sort strictly after the requirement.
	for _, candidate := range versions {
		//: strictly newer, so an exact match is not "behind".
		if semverLess(cur, candidate) {
			newer = append(newer, candidate)
		}
	}
	sortVersions(newer)
	//: oldest first, so the caller can count the gap and name the newest.
	return newer
}

// sortVersions orders versions ascending by semver. Insertion sort keeps the
// dependency list empty and the input is a handful of tags.
func sortVersions(v []string) {
	//: insertion sort keeps the dependency list empty; the input is a handful
	//: of tags, so the quadratic worst case never bites.
	for i := 1; i < len(v); i++ {
		//: slide the element left until it sits in order.
		for j := i; j > 0 && semverLess(v[j], v[j-1]); j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// parseSemver splits a strict "vMAJOR.MINOR.PATCH" tag. Anything carrying a
// prerelease or build suffix is rejected, which is what keeps pseudo-versions
// out of the candidate set.
func parseSemver(v string) (major, minor, patch int, ok bool) {
	//: a release tag always carries the v prefix; anything else is not one.
	if !strings.HasPrefix(v, "v") {
		//: reject rather than guess — a misread version yields wrong advice.
		return 0, 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	//: exactly three components, so a prerelease or pseudo-version is refused.
	if len(parts) != semverParts {
		//: not a strict release tag.
		return 0, 0, 0, false
	}
	var nums [semverParts]int
	//: parse each component; any non-numeric part disqualifies the whole tag.
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		//: a non-numeric or negative component disqualifies the whole tag.
		if err != nil || n < 0 {
			//: not a strict release; the caller must not treat it as one.
			return 0, 0, 0, false
		}
		nums[i] = n
	}
	//: hand back the three components in declaration order.
	return nums[semverMajor], nums[semverMinor], nums[semverPatch], true
}

// semverLess reports whether a sorts before b. An unparseable version sorts
// first, so a consumer on a pseudo-version is told about every real release.
func semverLess(a, b string) bool {
	amaj, amin, apatch, aok := parseSemver(a)
	bmaj, bmin, bpatch, bok := parseSemver(b)
	//: nothing sorts before an unparseable target, or every pseudo-version
	//: would look like an upgrade.
	if !bok {
		//: b is not a release, so a cannot precede it.
		return false
	}
	//: an unparseable a sorts first, so a consumer on a pseudo-version is told
	//: about every real release.
	if !aok {
		//: a is not a release, so every real tag is newer.
		return true
	}
	//: major, then minor, then patch — the usual precedence.
	if amaj != bmaj {
		//: a differing major settles it.
		return amaj < bmaj
	}
	//: same major, so the minor decides.
	if amin != bmin {
		//: a differing minor settles it.
		return amin < bmin
	}
	//: same major and minor; only the patch is left.
	return apatch < bpatch
}

// versionNotice renders the warning. It names the gap's kind because that is
// what tells a reader how urgent this is, and it names the exact go get line
// because a nudge without a next step is just noise.
func versionNotice(cur string, newer []string) string {
	//: newerThan sorted ascending, so the last entry is the newest release.
	latest := newer[len(newer)-1]
	var b strings.Builder
	b.WriteString("warning: the SDK is " + strconv.Itoa(len(newer)) + " " +
		gapKind(cur, latest) + " behind — go.mod requires " + cur + ", latest is " + latest + ".\n")
	//: the reason a patch matters here is specific to this SDK's release
	//: policy, and it is the part a consumer cannot infer from a changelog of
	//: exported symbols.
	b.WriteString("  A patch is cut whenever an internal package pkg depends on changes (ADR 0007),\n")
	b.WriteString("  so releases carry fixes that never alter the public API.\n")
	b.WriteString("  Update:  go get " + sdkModule + "@" + latest + "\n")
	b.WriteString("  Silence: -version-check=off")
	//: one block of text, printed verbatim by the caller.
	return b.String()
}

// gapKind names the largest version component that changed, pluralised for the
// count the caller prints before it.
func gapKind(cur, latest string) string {
	cmaj, cmin, _, cok := parseSemver(cur)
	lmaj, lmin, _, lok := parseSemver(latest)
	//: without two parseable tags the gap has no nameable kind.
	if !cok || !lok {
		//: the neutral word, which still reads correctly in the sentence.
		return "releases"
	}
	//: name the largest component that moved; that is what conveys urgency.
	switch {
	//: a major gap is the one a reader must plan for.
	case lmaj != cmaj:
		//: say MAJOR loudly; it is the only gap that can break a build.
		return "releases, crossing a MAJOR version,"
	//: a minor gap means new API, not just fixes.
	case lmin != cmin:
		//: worth naming, but not worth alarming about.
		return "releases, crossing a minor version,"
	//: patches only — the case this whole probe exists for.
	default:
		//: the wording that makes ADR 0007's policy legible in one line.
		return "patch releases"
	}
}
