#!/usr/bin/env bash
# scripts/release/proxy-rehearsal.sh — rehearse a release on a module proxy that
# is a directory (GOPROXY=file://…), before anything reaches proxy.golang.org,
# where every version is kept for good (ADR 0171 §2, §3).
#
# The question it answers is the one the split of ADR 0171 must not get wrong:
# a consumer pinned to a MONOLITHIC root — v0.19.7, which holds pkg/v1/* and
# internal/* — adds one family module. Two modules then provide the family's
# packages, and Go refuses with `ambiguous import`, unless the family module
# requires a root that no longer holds them, which minimum version selection
# then lifts the consumer to. The rehearsal builds that consumer and runs
# `go get` + `go build` against the file proxy, with no network and no
# checksum database.
#
# Modes:
#
#   --fixture (default)  factice modules shaped like the real ones: a
#                        monolithic root v0.19.7 holding pkg/v1/data, a root
#                        v0.20.0 holding neither pkg/ nor internal/ and
#                        retracting [v0.18.0, v0.19.7], and a family module
#                        pkg/v1/data v0.1.0 requiring that root. PASSES when the
#                        consumer builds — and, as its control, when the same
#                        family module NOT requiring the new root fails with
#                        `ambiguous import`: a rehearsal that cannot see the
#                        failure it exists for proves nothing.
#   --manifest=<file> --ref=<commit> [--offline]
#                        the real candidate release: every module the manifest
#                        names, its zip built from <ref> (the release commit
#                        cut-tags.sh made), its tombstones from their tags, the
#                        modules it keeps and the root floor from their last
#                        tags here, beside what $REHEARSAL_UPSTREAM (default
#                        https://proxy.golang.org) already serves of the old
#                        modules — the root v0.19.7, …/pkg v0.17.0 and v0.18.0,
#                        the three internal modules v0.17.0. Then, each on a
#                        consumer of its own:
#                          - every family of the manifest added to a go.mod
#                            pinned to the root v0.19.7: it builds, the root
#                            lifted to the manifest's root_floor, the tidied
#                            go.mod naming no monolithic root;
#                          - `go get -u ./...` from v0.19.7: the build ends on
#                            the internal module, no retired one selected;
#                          - a go.mod on …/pkg v0.17.0 adding a family (errs
#                            when cut) with one `go get`;
#                          - every vendor and connector module of the manifest
#                            added to a go.mod on v0.19.7 — its vendors from the
#                            upstream, so skipped (and said) with --offline or
#                            an upstream that does not answer;
#                          - every family the manifest keeps, at its last tag,
#                            built against the manifest's internal and families;
#                          - the control: the first family re-published without
#                            the floor fails with an ambiguous import.
#                        The consumers' GOPROXY is the file proxy, then the
#                        upstream (`,`: only what the file proxy does not hold),
#                        the checksum database off. Exit 3 when the manifest
#                        names no family module or no floor.
#
# Exit 0: rehearsed and green. 1: a consumer did not build (or the control
# built). 3: nothing to rehearse. 64: bad usage, or a go older than the go
# directive of the modules the manifest names (GOTOOLCHAIN=local is forced: a
# toolchain download is network). `KEEP=1` keeps the work directory.

set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"

MODE=fixture
MANIFEST=""
REF=""
UPSTREAM="${REHEARSAL_UPSTREAM:-https://proxy.golang.org}"
OFFLINE=0
for arg in "$@"; do
  case "$arg" in
    --fixture) MODE=fixture ;;
    --manifest=*) MODE=manifest; MANIFEST="${arg#--manifest=}" ;;
    --ref=*) REF="${arg#--ref=}" ;;
    --offline) OFFLINE=1 ;;
    -h | --help)
      sed -n '2,60p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "proxy-rehearsal: unknown argument $arg" >&2
      exit 64
      ;;
  esac
done

if ! command -v go >/dev/null 2>&1; then
  echo "proxy-rehearsal: go is not on PATH" >&2
  exit 64
fi


# go_new_enough — 0 when the go on PATH is at least the go directive of the
# repository's go.mod. The manifest's modules declare it, and every go command
# here runs with GOTOOLCHAIN=local — a toolchain download is network and would
# rehearse with a go nobody chose —, so an older go fails every consumer with
# "go.mod requires go >= …"; said once, up front.
go_new_enough() {
  local need have
  need="$(awk '$1 == "go" { print $2; exit }' "$here/../../go.mod")"
  have="$(GOTOOLCHAIN=local go env GOVERSION 2>/dev/null | sed 's/^go//; s/[^0-9.].*$//')"
  if [ -n "$need" ] && [ "$(printf '%s\n%s\n' "$need" "$have" | sort -t. -k1,1n -k2,2n -k3,3n | head -n1)" != "$need" ]; then
    echo "proxy-rehearsal: go $have is older than go $need, the go directive of the modules it rehearses — put go $need or newer on PATH (GOTOOLCHAIN=local is forced)" >&2
    return 1
  fi
}

# Checked before anything is built, for a manifest that has something to
# rehearse: one with no family is exit 3 whatever the go.
if [ "$MODE" = manifest ] && [ -r "$MANIFEST" ] &&
  jq -e '.modules[]? | select(.dir | startswith("pkg/v1/"))' "$MANIFEST" >/dev/null 2>&1; then
  go_new_enough || exit 64
fi

WORK="$(mktemp -d)"
if [ "${KEEP:-0}" != 1 ]; then
  trap 'chmod -R u+w "$WORK" 2>/dev/null; rm -rf "$WORK"' EXIT
else
  echo "proxy-rehearsal: work directory kept at $WORK" >&2
fi
PROXY="$WORK/proxy"
# The consumers' GOPROXY: the file proxy alone for the fixture, which must run
# with no network; in manifest mode the file proxy, then $UPSTREAM for what it
# does not hold — the vendor and connector modules' own dependencies —, unless
# --offline or the upstream does not answer (manifest() decides).
CONSUMER_PROXY="file://$PROXY"
mkdir -p "$PROXY" "$WORK/tool" "$WORK/gopath" "$WORK/gocache"

# The zip writer: Go's module zip format (module@version/ prefix, nested
# modules and dot-directories left out), with the standard library alone —
# golang.org/x/mod/zip is a dependency this repository does not take.
cat >"$WORK/tool/go.mod" <<'EOF'
module rehearsal/zipmod

go 1.21
EOF
cat >"$WORK/tool/main.go" <<'EOF'
package main

import (
	"archive/zip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// main writes the module zip of srcdir as module@version to out.
func main() {
	out, module, version, src := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	f, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	w := zip.NewWriter(f)
	prefix := module + "@" + version + "/"
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || exists(filepath.Join(p, "go.mod"))) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		dst, err := w.Create(prefix + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(dst, in)
		return err
	})
	if err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	if err := f.Close(); err != nil {
		panic(err)
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
EOF
(cd "$WORK/tool" && GOWORK=off GOFLAGS= GOTOOLCHAIN=local go build -o "$WORK/zipmod" .) || {
  echo "proxy-rehearsal: the zip writer did not build" >&2
  exit 1
}

# escape_module <path> — the proxy's case encoding (`!` + lower case).
escape_module() {
  sed -E 's/([A-Z])/!\L\1/g' <<<"$1"
}

# proxy_add <module> <version> <srcdir> — publish <srcdir> on the file proxy as
# <module>@<version>: its go.mod, an .info and the zip, and the version listed.
proxy_add() {
  local module="$1" version="$2" src="$3" d
  d="$PROXY/$(escape_module "$module")/@v"
  mkdir -p "$d"
  cp "$src/go.mod" "$d/$version.mod"
  printf '{"Version":"%s","Time":"2026-10-12T00:00:00Z"}\n' "$version" >"$d/$version.info"
  "$WORK/zipmod" "$d/$version.zip" "$module" "$version" "$src"
  echo "$version" >>"$d/list"
}

# proxy_fetch <module> <version> — copy a PUBLISHED version from $UPSTREAM.
proxy_fetch() {
  local module="$1" version="$2" d m ext
  m="$(escape_module "$module")"
  d="$PROXY/$m/@v"
  mkdir -p "$d"
  for ext in info mod zip; do
    # -L: proxy.golang.org answers a zip with a 302 to its storage; without
    # following it the zip is the redirect's empty body (measured).
    curl -fsSL --max-time 60 -o "$d/$version.$ext" "$UPSTREAM/$m/@v/$version.$ext" || {
      echo "proxy-rehearsal: could not fetch $module@$version.$ext from $UPSTREAM" >&2
      return 1
    }
  done
  echo "$version" >>"$d/list"
}

# consume <name> <require-root> <import> <get…> — a consumer module pinned to
# the root at <require-root>, importing <import>, running `go get <get…>` then
# `go build ./...` against the file proxy alone. Its output is left in
# $WORK/<name>.log; the exit status is the build's.
consume() {
  local name="$1" root="$2" imp="$3" c
  shift 3
  c="$WORK/$name"
  mkdir -p "$c"
  printf 'module example.com/%s\n\ngo 1.21\n\nrequire github.com/kitsunium/sdk %s\n' "$name" "$root" >"$c/go.mod"
  printf 'package main\n\nimport _ "%s"\n\nfunc main() {}\n' "$imp" >"$c/main.go"
  (
    cd "$c"
    export GOWORK=off GOFLAGS=-mod=mod GOTOOLCHAIN=local GOPROXY="$CONSUMER_PROXY" \
      GONOSUMDB='*' GONOSUMCHECK=1 GOSUMDB=off GOPATH="$WORK/gopath" GOMODCACHE="$WORK/gopath/pkg/mod" \
      GOCACHE="$WORK/gocache"
    go get "$@" && go mod tidy && go build ./...
  ) >"$WORK/$name.log" 2>&1
}

# upgrade_all <name> <root-version> <import> — a consumer pinned to the root at
# <root-version>, importing <import>, built there first, then
# `go get -u ./...`, `go mod tidy` and `go build`; $WORK/<name>.mods lists the
# build's modules and $WORK/<name>.pkgs each dependency package with its module.
upgrade_all() {
  local name="$1" root="$2" imp="$3" c
  c="$WORK/$name"
  mkdir -p "$c"
  printf 'module example.com/%s\n\ngo 1.21\n\nrequire github.com/kitsunium/sdk %s\n' "$name" "$root" >"$c/go.mod"
  printf 'package main\n\nimport _ "%s"\n\nfunc main() {}\n' "$imp" >"$c/main.go"
  (
    cd "$c"
    export GOWORK=off GOFLAGS=-mod=mod GOTOOLCHAIN=local GOPROXY="$CONSUMER_PROXY" \
      GONOSUMDB='*' GONOSUMCHECK=1 GOSUMDB=off GOPATH="$WORK/gopath" GOMODCACHE="$WORK/gopath/pkg/mod" \
      GOCACHE="$WORK/gocache"
    go mod tidy && go build ./... && go get -u ./... && go mod tidy && go build ./... &&
      go list -m all >"$WORK/$name.mods" &&
      go list -deps -f '{{if .Module}}{{.ImportPath}} {{.Module.Path}}{{end}}' ./... >"$WORK/$name.pkgs"
  ) >"$WORK/$name.log" 2>&1
}

# legacy_pkg <name> <import> <get…> — a consumer on …/pkg v0.17.0 and its
# three internal modules, importing <import>, built there first, then
# `go get <get…>`, `go mod tidy`, `go build`; $WORK/<name>.mods lists the
# build's modules.
legacy_pkg() {
  local name="$1" imp="$2" c
  shift 2
  c="$WORK/$name"
  mkdir -p "$c"
  printf 'module example.com/%s\n\ngo 1.21\n\nrequire github.com/kitsunium/sdk/pkg v0.17.0\n\nrequire (\n\tgithub.com/kitsunium/sdk/internal/core v0.17.0 // indirect\n\tgithub.com/kitsunium/sdk/internal/kernel v0.17.0 // indirect\n\tgithub.com/kitsunium/sdk/internal/service v0.17.0 // indirect\n)\n' "$name" >"$c/go.mod"
  printf 'package main\n\nimport _ "%s"\n\nfunc main() {}\n' "$imp" >"$c/main.go"
  (
    cd "$c"
    export GOWORK=off GOFLAGS=-mod=mod GOTOOLCHAIN=local GOPROXY="$CONSUMER_PROXY" \
      GONOSUMDB='*' GONOSUMCHECK=1 GOSUMDB=off GOPATH="$WORK/gopath" GOMODCACHE="$WORK/gopath/pkg/mod" \
      GOCACHE="$WORK/gocache"
    go build ./... && go get "$@" && go mod tidy && go build ./... && go list -m all >"$WORK/$name.mods"
  ) >"$WORK/$name.log" 2>&1
}

# vendor_consumers — for each third-party/ or framework/connectors/ module of
# the manifest, a consumer pinned to the root v0.19.7 importing one of its
# packages and `go get`ting it, which must build. Offline: skipped, said.
vendor_consumers() {
  local dir module version pkg n=0
  if [ "$CONSUMER_PROXY" = "file://$PROXY" ]; then
    echo "note: offline — the vendor and connector modules' consumers are not rehearsed (their vendors are on no file proxy); test-modules.bats covers their floor and pins"
    return 0
  fi
  while IFS=$'\t' read -r dir module version; do
    case "$dir" in third-party/* | framework/connectors/*) ;; *) continue ;; esac
    pkg="$(git -C "$here/../.." ls-tree -r --name-only "$REF" -- "$dir" | awk -F/ '/\.go$/ && !/_test\.go$/ { sub(/\/[^\/]*$/, ""); print; exit }')"
    [ -n "$pkg" ] || continue
    n=$((n + 1))
    if ! consume "vendor$n" v0.19.7 "github.com/kitsunium/sdk/$pkg" "$module@$version"; then
      echo "proxy-rehearsal: pinned to v0.19.7, adding $dir@$version did not build:" >&2
      sed 's/^/  /' "$WORK/vendor$n.log" >&2
      return 1
    fi
    echo "ok: pinned to v0.19.7, adding $dir@$version builds (its vendors through the module cache or $UPSTREAM)"
  done < <(jq -r '.modules[] | [.dir, .module, .version] | @tsv' "$MANIFEST")
}

# publish_kept — every module go.work names that the manifest does not, at its
# last tag here, and every tombstone tag here the manifest does not name,
# zipped from its tag onto the file proxy: what the manifest's modules require
# at their last tags, as the real proxy already serves it.
publish_kept() {
  local root dir tag version src floor
  root="$here/../.."
  # shellcheck source=lib/tag-format.sh
  . "$here/lib/tag-format.sh"
  while IFS= read -r dir; do
    [ -z "$dir" ] && continue
    jq -e --arg d "$dir" '.modules[] | select(.dir == $d)' "$MANIFEST" >/dev/null && continue
    tag="$(cd "$root" && latest_module_tag "$dir")" || return 1
    [ -n "$tag" ] || continue
    version="v$(version_from_tag "$tag")"
    src="$WORK/src/kept_$(tr '/.' '__' <<<"$dir")"
    mkdir -p "$src"
    if [ "$dir" = . ]; then
      git -C "$root" archive "$tag" | tar -x -C "$src"
      proxy_add github.com/kitsunium/sdk "$version" "$src"
    else
      git -C "$root" archive "$tag" "$dir" | tar -x -C "$src"
      proxy_add "github.com/kitsunium/sdk/$dir" "$version" "$src/$dir"
    fi
  done < <(cd "$root" && sed -n '/use (/,/)/p' go.work | awk '/^[[:space:]]*\./{ sub(/^\.\//, "", $1); print $1 }')
  # The root at the floor every family requires, when this release is not it.
  floor="$(jq -r '.root_floor // ""' "$MANIFEST")"
  if [ -n "$floor" ] && ! jq -e --arg v "$floor" '.modules[] | select(.dir == "." and .version == $v)' "$MANIFEST" >/dev/null &&
    git -C "$root" rev-parse -q --verify "refs/tags/$floor" >/dev/null; then
    src="$WORK/src/kept_root_floor"
    mkdir -p "$src"
    git -C "$root" archive "$floor" | tar -x -C "$src"
    proxy_add github.com/kitsunium/sdk "$floor" "$src"
  fi
  while IFS= read -r tag; do
    [ -z "$tag" ] && continue
    jq -e --arg t "$tag" '.tombstones[]? | select(. == $t)' "$MANIFEST" >/dev/null && continue
    dir="${tag%/v*}"
    src="$WORK/src/kepttomb_$(tr '/' '_' <<<"$dir")"
    mkdir -p "$src"
    git -C "$root" archive "$tag" "$dir" | tar -x -C "$src"
    proxy_add "github.com/kitsunium/sdk/$dir" "${tag##*/}" "$src/$dir"
  done < <(cd "$root" && git tag -l 'internal/*/v*' | while IFS= read -r t; do is_valid_tombstone_tag "$t" && echo "$t"; done)
}

# kept_families — for every pkg family go.work names that the manifest does
# not, at its last tag here: zipped from that tag onto the file proxy, then a
# consumer requiring it there, `go get` of the manifest's internal and family
# versions, and `go build` of every package of the family. Returns 1 on the
# first that does not build.
kept_families() {
  local root dir tag version n=0 c get
  root="$here/../.."
  # shellcheck source=lib/tag-format.sh
  . "$here/lib/tag-format.sh"
  get=()
  while IFS=$'\t' read -r dir module version; do
    case "$dir" in internal | pkg/v1/*) get+=("$module@$version") ;; esac
  done < <(jq -r '.modules[] | [.dir, .module, .version] | @tsv' "$MANIFEST")
  while IFS= read -r dir; do
    case "$dir" in pkg/v1/*) ;; *) continue ;; esac
    jq -e --arg d "$dir" '.modules[] | select(.dir == $d)' "$MANIFEST" >/dev/null && continue
    tag="$(cd "$root" && latest_module_tag "$dir")" || return 1
    if [ -z "$tag" ]; then
      echo "note: $dir is kept and has no tag yet — nothing to build at a last tag"
      continue
    fi
    version="v$(version_from_tag "$tag")"
    n=$((n + 1))
    c="$WORK/kept$n"
    mkdir -p "$c"
    printf 'module example.com/kept%s\n\ngo 1.21\n\nrequire github.com/kitsunium/sdk/%s %s\n' "$n" "$dir" "$version" >"$c/go.mod"
    if ! (
      cd "$c"
      export GOWORK=off GOFLAGS=-mod=mod GOTOOLCHAIN=local GOPROXY="$CONSUMER_PROXY" \
        GONOSUMDB='*' GONOSUMCHECK=1 GOSUMDB=off GOPATH="$WORK/gopath" GOMODCACHE="$WORK/gopath/pkg/mod" \
        GOCACHE="$WORK/gocache"
      go get "${get[@]}" && go build "github.com/kitsunium/sdk/$dir/..."
    ) >"$WORK/kept$n.log" 2>&1; then
      echo "proxy-rehearsal: $dir at its last tag $tag does not build against this release's internal and families:" >&2
      sed 's/^/  /' "$WORK/kept$n.log" >&2
      return 1
    fi
    echo "ok: $dir kept at $tag builds against this release's internal and families"
  done < <(cd "$root" && sed -n '/use (/,/)/p' go.work | awk '/\.\//{ sub(/^\.\//, "", $1); print $1 }')
  return 0
}

module_src() {
  mkdir -p "$1"
  printf '%s' "$2" >"$1/go.mod"
}

fixture() {
  local s="$WORK/src"
  # The monolithic root, as v0.19.7 is: its packages under pkg/v1 and internal/.
  module_src "$s/root-0.19.7" $'module github.com/kitsunium/sdk\n\ngo 1.21\n'
  mkdir -p "$s/root-0.19.7/pkg/v1/data" "$s/root-0.19.7/internal/kernel"
  echo 'package data' >"$s/root-0.19.7/pkg/v1/data/data.go"
  echo 'package kernel' >"$s/root-0.19.7/internal/kernel/kernel.go"
  echo 'package sdk' >"$s/root-0.19.7/doc.go"
  proxy_add github.com/kitsunium/sdk v0.19.7 "$s/root-0.19.7"
  # The root after the split: neither pkg/ nor internal/, the monolithic roots
  # retracted (ADR 0171 §3).
  module_src "$s/root-0.20.0" $'module github.com/kitsunium/sdk\n\ngo 1.21\n\n// The packages moved into a module per pkg family and the internal module (ADR 0171).\nretract [v0.18.0, v0.19.7]\n'
  echo 'package sdk' >"$s/root-0.20.0/doc.go"
  proxy_add github.com/kitsunium/sdk v0.20.0 "$s/root-0.20.0"
  # The family module, requiring the root that no longer holds its packages.
  module_src "$s/data-0.1.0" $'module github.com/kitsunium/sdk/pkg/v1/data\n\ngo 1.21\n\nrequire github.com/kitsunium/sdk v0.20.0\n'
  echo 'package data' >"$s/data-0.1.0/data.go"
  proxy_add github.com/kitsunium/sdk/pkg/v1/data v0.1.0 "$s/data-0.1.0"
  # The control: the same family module, NOT requiring the new root.
  module_src "$s/data-0.0.9" $'module github.com/kitsunium/sdk/pkg/v1/data\n\ngo 1.21\n'
  echo 'package data' >"$s/data-0.0.9/data.go"
  proxy_add github.com/kitsunium/sdk/pkg/v1/data v0.0.9 "$s/data-0.0.9"

  if consume pinned v0.19.7 github.com/kitsunium/sdk/pkg/v1/data github.com/kitsunium/sdk/pkg/v1/data@v0.1.0; then
    # Minimum version selection must have lifted the root past the monolith:
    # the build passing for another reason is no proof of the mechanism.
    case "$(cat "$WORK/pinned.log")" in
      *"upgraded github.com/kitsunium/sdk v0.19.7 => v0.20.0"*) ;;
      *)
        echo "proxy-rehearsal: the consumer built, but go get did not lift the root from v0.19.7 to v0.20.0:" >&2
        sed 's/^/  /' "$WORK/pinned.log" >&2
        return 1
        ;;
    esac
    echo "ok: a consumer pinned to the root v0.19.7 adds pkg/v1/data v0.1.0 and builds — the root lifted to v0.20.0, which holds no pkg/"
  else
    echo "proxy-rehearsal: the consumer pinned to v0.19.7 did not build with pkg/v1/data v0.1.0:" >&2
    sed 's/^/  /' "$WORK/pinned.log" >&2
    return 1
  fi
  if consume control v0.19.7 github.com/kitsunium/sdk/pkg/v1/data github.com/kitsunium/sdk/pkg/v1/data@v0.0.9; then
    echo "proxy-rehearsal: the control built — a family module that does not require the new root should meet an ambiguous import; the rehearsal cannot see the failure it exists for" >&2
    return 1
  fi
  case "$(cat "$WORK/control.log")" in
    *"ambiguous import"*) echo "ok: the control — the same module not requiring the new root — fails with an ambiguous import, as it must" ;;
    *)
      echo "proxy-rehearsal: the control failed, but not with an ambiguous import:" >&2
      sed 's/^/  /' "$WORK/control.log" >&2
      return 1
      ;;
  esac
}

manifest() {
  local families dir module version src pkg floor cache n=0
  if [ ! -r "$MANIFEST" ] || [ -z "$REF" ]; then
    echo "proxy-rehearsal: --manifest needs a readable file and --ref=<release commit>" >&2
    return 64
  fi
  families="$(jq -r '.modules[] | select(.dir | startswith("pkg/v1/")) | .dir' "$MANIFEST")"
  if [ -z "$families" ]; then
    echo "proxy-rehearsal: the manifest names no pkg family module — nothing of ADR 0171's shape to rehearse until the split lands" >&2
    return 3
  fi
  floor="$(jq -r '.root_floor // ""' "$MANIFEST")"
  if [ -z "$floor" ]; then
    echo "proxy-rehearsal: the manifest names no root_floor — cut-tags.sh writes the floor once a root holds the split (ADR 0171 §7)" >&2
    return 3
  fi
  proxy_fetch github.com/kitsunium/sdk v0.19.7
  # The upstream behind the file proxy, for the dependencies of the vendor and
  # connector modules the file proxy does not hold.
  if [ "$OFFLINE" -eq 0 ] && curl -fsSL --max-time 20 -o /dev/null "$UPSTREAM/github.com/kitsunium/sdk/@v/list" 2>/dev/null; then
    CONSUMER_PROXY="file://$PROXY,$UPSTREAM"
    # This machine's module cache first, when it has one: it is GOPROXY's own
    # layout, holds what the workspace already builds with, and spares the
    # upstream a request (a caching proxy in front of it has served a stale
    # redirect for a zip — measured).
    cache="$(GOTOOLCHAIN=local go env GOMODCACHE 2>/dev/null)/cache/download"
    if [ -d "$cache" ]; then CONSUMER_PROXY="file://$PROXY,file://$cache,$UPSTREAM"; fi
  fi
  while IFS=$'\t' read -r dir module version; do
    src="$WORK/src/$(tr '/' '_' <<<"$dir")"
    mkdir -p "$src"
    git -C "$here/../.." archive "$REF" "$([ "$dir" = . ] && echo . || echo "$dir")" | tar -x -C "$src"
    [ "$dir" = . ] || src="$src/$dir"
    proxy_add "$module" "$version" "$src"
  done < <(jq -r '.modules[] | [.dir, .module, .version] | @tsv' "$MANIFEST")
  # The tombstones, each zipped from its own tag — a child of the release
  # commit, its directory reduced to a go.mod.
  while IFS= read -r tag; do
    [ -z "$tag" ] && continue
    dir="${tag%/v*}"
    src="$WORK/src/tomb_$(tr '/' '_' <<<"$dir")"
    mkdir -p "$src"
    git -C "$here/../.." archive "$tag" "$dir" | tar -x -C "$src"
    proxy_add "github.com/kitsunium/sdk/$dir" "${tag##*/}" "$src/$dir"
  done < <(jq -r '.tombstones[]?' "$MANIFEST")
  # The modules this release does not cut, at their last tags here.
  publish_kept || return 1
  # What the proxy already serves of the modules ADR 0162 retired: the build a
  # consumer of a monolithic root resolves an internal package against.
  for dir in internal/kernel internal/core internal/service pkg; do
    proxy_fetch "github.com/kitsunium/sdk/$dir" v0.17.0
  done
  # …/pkg's tombstone, and the root it requires: what the I1 consumer is lifted
  # to.
  proxy_fetch github.com/kitsunium/sdk/pkg v0.18.0
  proxy_fetch github.com/kitsunium/sdk v0.18.0
  while IFS= read -r dir; do
    [ -z "$dir" ] && continue
    version="$(jq -r --arg d "$dir" '.modules[] | select(.dir == $d) | .version' "$MANIFEST")"
    pkg="$(git -C "$here/../.." ls-tree -r --name-only "$REF" -- "$dir" | awk -F/ '/\.go$/ && !/_test\.go$/ { sub(/\/[^\/]*$/, ""); print; exit }')"
    n=$((n + 1))
    if ! consume "family$n" v0.19.7 "github.com/kitsunium/sdk/$pkg" "github.com/kitsunium/sdk/$dir@$version"; then
      echo "proxy-rehearsal: pinned to v0.19.7, adding $dir@$version did not build:" >&2
      sed 's/^/  /' "$WORK/family$n.log" >&2
      return 1
    fi
    # The mechanism, not just the outcome: go get lifted the root past the
    # monolith to the floor the release wrote, and the tidied go.mod keeps no
    # monolithic root.
    case "$(cat "$WORK/family$n.log")" in
      *"upgraded github.com/kitsunium/sdk v0.19.7 => $floor"*) ;;
      *)
        echo "proxy-rehearsal: $dir@$version built, but go get did not lift the root from v0.19.7 to $floor:" >&2
        sed 's/^/  /' "$WORK/family$n.log" >&2
        return 1
        ;;
    esac
    if awk '$1 == "github.com/kitsunium/sdk" && $2 ~ /^v0\.(18|19)\./ { found = 1 } END { exit !found }' "$WORK/family$n/go.mod"; then
      echo "proxy-rehearsal: after go mod tidy, family$n still requires a monolithic root:" >&2
      sed 's/^/  /' "$WORK/family$n/go.mod" >&2
      return 1
    fi
    echo "ok: pinned to v0.19.7, adding $dir@$version builds — the root lifted to $floor, go mod tidy clean"
  done <<<"$families"
  # B1: a consumer of the monolithic root that upgrades everything. Before the
  # split's tombstones it resolved internal/kernel/errs to the longest path that
  # still provided it, …/internal/kernel@v0.17.0, and built silently against
  # the old internals; it must end on the internal module the release cut.
  if ! upgrade_all upgrade v0.19.7 github.com/kitsunium/sdk/pkg/v1/errs; then
    echo "proxy-rehearsal: pinned to v0.19.7, 'go get -u ./...' did not build:" >&2
    sed 's/^/  /' "$WORK/upgrade.log" >&2
    return 1
  fi
  if grep -E 'kitsunium/sdk/internal/(kernel|core|service) v0\.1[0-7]\.' "$WORK/upgrade.mods" >/dev/null; then
    echo "proxy-rehearsal: after 'go get -u ./...' from v0.19.7 the build still selects a retired internal module:" >&2
    sed 's/^/  /' "$WORK/upgrade.mods" >&2
    return 1
  fi
  if ! grep -E '^github\.com/kitsunium/sdk/internal/kernel/errs github\.com/kitsunium/sdk/internal$' "$WORK/upgrade.pkgs" >/dev/null; then
    echo "proxy-rehearsal: after 'go get -u ./...' internal/kernel/errs does not come from the internal module:" >&2
    sed 's/^/  /' "$WORK/upgrade.pkgs" >&2
    return 1
  fi
  echo "ok: pinned to v0.19.7, 'go get -u ./...' builds on the internal module — no retired internal module selected"

  # The vendor and connector modules the release cuts, each added to a
  # consumer pinned to the monolithic root: their own vendors come from the
  # upstream, so offline they are left to test-modules.bats (the floor and the
  # pins on their go.mods), and said so.
  vendor_consumers || return 1

  # I4: every family the release does NOT cut keeps its last tag, whose go.mod
  # pinned the internal module and the families of ITS release; a consumer that
  # takes this release's internal and families beside it must still build that
  # family. Each kept family is zipped from its own tag and built against the
  # manifest's versions.
  kept_families
  local kept_rc=$?
  [ "$kept_rc" -eq 0 ] || return "$kept_rc"

  # I1: a consumer still on …/pkg v0.17.0 — the module before ADR 0162, with
  # its three internal modules listed indirect — adds one family by naming it
  # alone. Two modules provided pkg/v1/errs there (`ambiguous import`) until
  # every family required …/pkg at its tombstone.
  # pkg/v1/errs when the release cuts it: the one package …/pkg v0.17.0 held
  # at the same path, so the ambiguity is real; else the first family.
  dir="$(head -n1 <<<"$families")"
  if awk '$0 == "pkg/v1/errs" { f = 1 } END { exit !f }' <<<"$families"; then dir=pkg/v1/errs; fi
  version="$(jq -r --arg d "$dir" '.modules[] | select(.dir == $d) | .version' "$MANIFEST")"
  pkg="$(git -C "$here/../.." ls-tree -r --name-only "$REF" -- "$dir" | awk -F/ '/\.go$/ && !/_test\.go$/ { sub(/\/[^\/]*$/, ""); print; exit }')"
  if ! legacy_pkg legacy "github.com/kitsunium/sdk/$pkg" "github.com/kitsunium/sdk/$dir@$version"; then
    echo "proxy-rehearsal: on …/pkg v0.17.0, 'go get …/$dir@$version' did not build:" >&2
    sed 's/^/  /' "$WORK/legacy.log" >&2
    return 1
  fi
  if grep -E 'kitsunium/sdk/(pkg|internal/(kernel|core|service)) v0\.(1[0-7]|[0-9])\.' "$WORK/legacy.mods" >/dev/null; then
    echo "proxy-rehearsal: after adding $dir the build still selects a module ADR 0162 retired, below its tombstone:" >&2
    sed 's/^/  /' "$WORK/legacy.mods" >&2
    return 1
  fi
  echo "ok: on …/pkg v0.17.0, 'go get …/$dir@$version' alone builds — …/pkg lifted to its tombstone, no retired internal module selected"

  # The control, on the real modules: the first family re-published without
  # the floor must meet the ambiguous import the floor exists to prevent.
  dir="$(head -n1 <<<"$families")"
  module="$(jq -r --arg d "$dir" '.modules[] | select(.dir == $d) | .module' "$MANIFEST")"
  pkg="$(git -C "$here/../.." ls-tree -r --name-only "$REF" -- "$dir" | awk -F/ '/\.go$/ && !/_test\.go$/ { sub(/\/[^\/]*$/, ""); print; exit }')"
  src="$WORK/src/control"
  mkdir -p "$src"
  git -C "$here/../.." archive "$REF" "$dir" | tar -x -C "$src"
  (cd "$src/$dir" && GOWORK=off GOFLAGS= GOTOOLCHAIN=local go mod edit -droprequire=github.com/kitsunium/sdk go.mod)
  proxy_add "$module" v0.0.1-control "$src/$dir"
  if consume control v0.19.7 "github.com/kitsunium/sdk/$pkg" "$module@v0.0.1-control"; then
    echo "proxy-rehearsal: the control built — $dir without the floor should meet an ambiguous import; the rehearsal cannot see the failure it exists for" >&2
    return 1
  fi
  case "$(cat "$WORK/control.log")" in
    *"ambiguous import"*) echo "ok: the control — $dir without the floor — fails with an ambiguous import, as it must" ;;
    *)
      echo "proxy-rehearsal: the control failed, but not with an ambiguous import:" >&2
      sed 's/^/  /' "$WORK/control.log" >&2
      return 1
      ;;
  esac
}

rc=0
case "$MODE" in
  fixture) fixture || rc=$? ;;
  manifest) manifest || rc=$? ;;
esac
exit "$rc"
