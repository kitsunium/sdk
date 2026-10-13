#!/usr/bin/env bats
# BATS tests for check-published.sh — after the push, every version a release
# names is served by the module proxy and downloadable by the go command —
# against a FILE proxy in GOPROXY's layout, the checksum database off, so no
# case needs the network.

setup() {
  CP="$BATS_TEST_DIRNAME/check-published.sh"
  PROXY="$BATS_TEST_TMPDIR/proxy"
  M="$BATS_TEST_TMPDIR/release-manifest.json"
  export CHECK_PUBLISHED_ATTEMPTS=2 CHECK_PUBLISHED_WAIT=0
  if ! command -v go >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1 || ! command -v python3 >/dev/null 2>&1; then
    skip "go, jq or python3 absent"
  fi
}

# publish <module> <version> [broken] — the module on the file proxy: its
# .info, .mod and a zip in the module zip format (module@version/ prefix);
# `broken` writes a zip that is no zip.
publish() {
  local module="$1" version="$2" d
  d="$PROXY/$module/@v"
  mkdir -p "$d"
  printf '{"Version":"%s","Time":"2026-10-13T00:00:00Z"}\n' "$version" >"$d/$version.info"
  printf 'module %s\n\ngo 1.21\n' "$module" >"$d/$version.mod"
  if [ "${3:-}" = broken ]; then
    echo "not a zip" >"$d/$version.zip"
  else
    python3 -I - "$d/$version.zip" "$module@$version" <<'PY'
import sys, zipfile
out, prefix = sys.argv[1], sys.argv[2]
mod = prefix.split("@")[0]
with zipfile.ZipFile(out, "w") as z:
    z.writestr(prefix + "/go.mod", "module %s\n\ngo 1.21\n" % mod)
PY
  fi
  echo "$version" >>"$d/list"
}

manifest() {
  jq -n --argjson modules "$1" --argjson tombs "$2" '{schema: 1, release: "v0.20.0", modules: $modules, tombstones: $tombs}' >"$M"
}

@test "every module and tombstone the manifest names, served and downloaded, passes" {
  publish github.com/kitsunium/sdk/pkg/v1/data v0.1.0
  publish github.com/kitsunium/sdk/internal/kernel v0.18.0
  manifest '[{"module":"github.com/kitsunium/sdk/pkg/v1/data","version":"v0.1.0","tag":"pkg/v1/data/v0.1.0"}]' '["internal/kernel/v0.18.0"]'
  run "$CP" --manifest="$M" --proxy="file://$PROXY" --no-sumdb
  [ "$status" -eq 0 ]
  [[ "$output" == *"ok github.com/kitsunium/sdk/pkg/v1/data@v0.1.0 — served and downloaded"* ]]
  [[ "$output" == *"ok github.com/kitsunium/sdk/internal/kernel@v0.18.0 — served and downloaded"* ]]
}

@test "a version the proxy does not serve fails, after the bounded retries, and is named" {
  publish github.com/kitsunium/sdk/pkg/v1/data v0.1.0
  manifest '[{"module":"github.com/kitsunium/sdk/pkg/v1/data","version":"v0.1.0"},{"module":"github.com/kitsunium/sdk/pkg/v1/net","version":"v0.1.0"}]' '[]'
  summary="$BATS_TEST_TMPDIR/summary.md"
  run env GITHUB_STEP_SUMMARY="$summary" "$CP" --manifest="$M" --proxy="file://$PROXY" --no-sumdb
  [ "$status" -eq 1 ]
  [[ "$output" == *"github.com/kitsunium/sdk/pkg/v1/net@v0.1.0 — the proxy does not serve"*"after 2 attempts"* ]]
  [[ "$(cat "$summary")" == *'| `github.com/kitsunium/sdk/pkg/v1/net` | `v0.1.0` | not served |'* ]]
  [[ "$(cat "$summary")" == *'| `github.com/kitsunium/sdk/pkg/v1/data` | `v0.1.0` | ok |'* ]]
}

@test "a version served but not downloadable fails, with the go command's words" {
  publish github.com/kitsunium/sdk/pkg/v1/data v0.1.0 broken
  manifest '[{"module":"github.com/kitsunium/sdk/pkg/v1/data","version":"v0.1.0"}]' '[]'
  run "$CP" --manifest="$M" --proxy="file://$PROXY" --no-sumdb
  [ "$status" -eq 1 ]
  [[ "$output" == *"pkg/v1/data@v0.1.0 — go mod download failed"* ]]
}

@test "a manifest naming nothing is no pass" {
  manifest '[]' '[]'
  run "$CP" --manifest="$M" --proxy="file://$PROXY" --no-sumdb
  [ "$status" -eq 1 ]
  [[ "$output" == *"nothing was checked"* ]]
}
