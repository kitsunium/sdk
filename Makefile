.PHONY: help build test test-framework lint guard bench cover docs docs-check docs-dev serve release-dry-run docs-readme error-codes profile benchstat-install benchstat-diff sdk-bench sdk-bench-profile sdk-bench-compare ci-gates-check release-scripts-check pre-commit-check lint-check lint-ktn-check ci-scripts-check vuln-install vuln-check doclinks api api-check

# `make` with no args prints the help. No aliases — every target on its own.
.DEFAULT_GOAL := help

# ── Colors (auto-disable when stdout is not a TTY) ─────────────────────
ifeq ($(shell [ -t 1 ] && echo tty),tty)
  CYAN  := \033[36m
  GREEN := \033[32m
  DIM   := \033[2m
  BOLD  := \033[1m
  RST   := \033[0m
else
  CYAN  :=
  GREEN :=
  DIM   :=
  BOLD  :=
  RST   :=
endif

help: ## Print this help (default goal).
	@printf "$(BOLD)$(CYAN)━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━$(RST)\n"
	@printf "  $(BOLD)kitsunium/sdk$(RST) $(DIM)—$(RST) Makefile\n"
	@printf "  $(DIM)Source of truth: .bazelrc (race / pure / coverage / ci) + MODULE.bazel$(RST)\n"
	@printf "$(BOLD)$(CYAN)━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━$(RST)\n"
	@printf "\n"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "build"  "$(DIM)mod tidy → gazelle → gofumpt → bazel build$(RST)  $(DIM)(prep + compile)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "test"   "bazel test --config=race //... $(DIM)(every *_test target, race on)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "lint"   "$(DIM)mod tidy + gazelle diff + drift assert + gofumpt -l + ktn-linter$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "bench"  "Regenerate every BENCH.md $(DIM)(CPU/RAM/OS/Go/git SHA envelope)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "docs"   "Build the SDK documentation portal $(DIM)(→ docs/site/dist/, 15 pages)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "docs-check" "$(DIM)build the working tree's portal, hold its ⌘K and API sections to docs/api, check every link$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "serve"  "kill / rebuild / re-serve docs on http://localhost:$(DIM)\$${PORT:-4321}$(RST)$(DIM)/$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "docs-dev" "$(DIM)hot-reloading docs dev server (astro dev, no full rebuild — fast iteration)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "cover"  "bazel coverage --combined_report=lcov //..."
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "release-dry-run"  "$(DIM)compute-bumps + cut-tags in dry-run (see ADR 0007)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "docs-readme"  "$(DIM)regenerate every pkg/v1/*/README.md from its doc comment (see ADR 0008)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "api"          "$(DIM)write docs/api — every exported symbol, read from the code on the 12 cells — then error-codes and gazelle$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "profile"      "$(DIM)capture cpu+mem+block+mutex pprof for codec bench (WAVE=<slug>)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "benchstat-diff" "$(DIM)compare two captured waves with mannwhitney p-values (BEFORE / AFTER)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "sdk-bench"        "$(DIM)run every internal/kernel/**/*_bench_test.go → .bench.out (COUNT=N)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "sdk-bench-profile" "$(DIM)capture cpu+mem+block+mutex pprof per kernel package → profiles/<pkg>/$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "sdk-bench-compare" "$(DIM)benchstat .bench.main.out vs .bench.out (A/B vs main)$(RST)"

# ── Wrappers ───────────────────────────────────────────────────────────
# Every target shells to bazel (+ housekeeping tools). The .bazelrc named
# configs (race / pure / coverage / ci) are the real source of truth;
# wrappers stay minimal.

# `build` runs the full prep chain before compiling:
#   1. bazel mod tidy        → MODULE.bazel up to date
#   2. bazel run //:gazelle  → regenerate BUILD.bazel from imports
#   3. gofumpt -l -w         → format Go source in place
#   4. bazel build //...     → compile every target
# This is the heavy "make my workspace consistent" button.
build:
	bazel mod tidy
	bazel run //:gazelle
	gofumpt -l -w internal pkg third-party framework
	bazel build //...

# `test` is the race-on suite. Always runs //... so the errs AST audits in
# //internal/kernel/errs:errs_test — every errs.Define well-formed, no two codes
# equal, every PP range owned (rules 3 and 4) — are part of every run, with no
# separate audit target. The ban on fmt.Errorf / errors.New (rule 2) is not one
# of them: it is `guard`'s SDK002 pass, run by `make lint` and CI's lint gate.
test:
	bazel test --config=race //...

# `test-framework` runs the framework's suites under `go test -race`,
# GOWORK=off, as a product builds them: its packages in the SDK module
# (./framework/..., which stops at the connector modules nested under it — ADR
# 0162), then each connector module of the census, one at a time. It is the
# gate of //framework/internal/kit:kit_test, `manual` under Bazel because the suite
# reads its own sources and positions relative to the module root (rule 12,
# ADR 0147).
test-framework:
	@echo "→ framework (the SDK module)"
	GOWORK=off go test -race -count=1 ./framework/...
	@set -e; for m in $$(bash scripts/ci/go-modules.sh | grep '^framework/connectors/'); do \
	  echo "→ $$m"; (cd $$m && GOWORK=off go test -race -count=1 ./...); \
	done

# `test-alloc` runs the race-off allocation gates. Every target here carries at
# least one `//go:build !race` test file (testing.AllocsPerRun /
# testing.Benchmark report a spurious +1 under -race), so the race suite above
# never compiles them — this pass is their ONLY gate. The target list lives in
# tools/alloc-lane-targets.txt so the Makefile, CI and the coverage guard read
# the same source of truth; check-alloc-lane-coverage.sh fails the build if a
# `!race` test exists in a package absent from that list. Run this locally
# before touching a codec, kernel or logger hot path's allocation profile.
ALLOC_TARGETS = $(shell sed -e 's/#.*//' -e '/^[[:space:]]*$$/d' tools/alloc-lane-targets.txt)

test-alloc:
	bazel test --config=alloc $(ALLOC_TARGETS)

# `lint` is the read-only counterpart of `build`: same checks, but it
# REFUSES to write — it asserts the tree is already consistent.
lint:
	bazel mod tidy
	bazel run //:gazelle -- -mode=diff
	@git diff --exit-code MODULE.bazel '**/BUILD.bazel'
	# gofumpt + sdkguard, then ktn-linter. Two targets, not one, because CI can
	# run the first pair unconditionally and the second only where a credential
	# for the private linter release exists. See the comment on `lint-check`.
	$(MAKE) --no-print-directory lint-check
	$(MAKE) --no-print-directory lint-ktn-check
	# Exemption invariant: a `//go:build !race` test is invisible to the race
	# suite, so the alloc lane is its only gate. Fail if one runs in no lane.
	bash scripts/pre-commit/check-alloc-lane-coverage.sh
	# The errs AST audits can only judge files that reach them as runfiles of
	# //:audit_sources. A package declaring codes but missing from that list is
	# audited by nothing AND passes — ADR 0020 records this gap having already
	# hidden ~10 emitters once. Mechanical, so it cannot reopen by forgetting.
	bash scripts/pre-commit/check-audit-coverage.sh
	# The root CLAUDE.md is the first thing anybody reads, and parallel union
	# merges silently left it describing a tree that no longer existed. Both
	# invariants key on what is ON DISK, never on a maintained number.
	bash scripts/pre-commit/check-domain-docs.sh
	# ADR 0160, mechanically: no service package declares an error code, every
	# service domain has a core at the same path, and every code the core
	# declares belongs to an engine at that path or is the core's own range.
	bash scripts/pre-commit/check-core-symmetry.sh
	# The platforms table genindex judges and writes docs/api on is the
	# cross-build matrix CI compiles: one table, written twice, held equal.
	bash scripts/pre-commit/check-platforms.sh
	# Package documentation, BENCH.md presence and the error-code YAML mirror.
	# Only the in-repo pre-commit hook ran these until it was removed (ADR 0153);
	# CI runs them now, as the same three direct steps.
	bash scripts/pre-commit/check-pkg-docs.sh
	bash scripts/pre-commit/check-bench-md.sh
	bash scripts/pre-commit/check-error-codes-drift.sh
	# Gazelle gives every internal/ package //:__subpackages__ visibility, which
	# admits the whole repository, so the layer direction is asserted on the
	# build graph instead of assumed from visibility (ADR 0068).
	bash scripts/check-layer-deps.sh

# `lint-check` and `lint-ktn-check` are the parts of `lint` a CI runner can
# execute on its own: a Go toolchain and two pinned binaries, no Bazel, no
# gazelle. `lint-check` also runs `doclinks` (ADR 0138) and `api-check`, which
# need nothing but the toolchain either — api-check also the workspace's
# modules, which the go command fetches when the module cache lacks them.
#
# They exist as named targets because of #236. Five of the eight checks `lint`
# performed then were already invoked by bazel-ci.yml as direct `bash …` steps
# (gazelle drift, alloc-lane, audit-coverage, domain-docs, layer-deps); the
# three here — gofumpt, sdkguard and ktn-linter — had no server-side control
# point at all, and `ktn-linter` in particular ran nowhere except a developer's
# pre-commit hook. Running the whole of `lint` in CI would have repeated the
# five that were already there and added a second gazelle pass; running these
# under a name is what `scripts/ci-gates-check.sh` can assert, because it only
# sees the Makefile↔CI link through a target name.
#
# The split is not cosmetic. `gofumpt` is a public Go module and `sdkguard` is
# in this repository, so `lint-check` needs nothing but the toolchain. The
# ktn-linter release is in a PRIVATE repository, and a workflow token is scoped
# to the repository that issues it — measured on this lane, not assumed:
#
#   gh release download v1.11.2 --repo kodflow/ktn-linter  ->  release not found
#
# with `secrets.GITHUB_TOKEN`, and the same command with a credentialed account
# downloads the asset. So the two halves have different preconditions, and a
# single target would have made the half that CAN run depend on the half that
# cannot.
#
# `lint` delegates to both instead of repeating either recipe, so the set CI
# enforces and the set `make lint` enforces locally cannot drift apart by
# editing one.
lint-check:
	@drift=$$(gofumpt -l internal pkg third-party framework); if [ -n "$$drift" ]; then \
		echo "gofumpt drift in the following files (run 'make build' to fix):"; \
		echo "$$drift"; exit 1; \
	fi
	# The SDK is bound by the invariants it imposes on consumers, and by its own
	# rule 2 (SDK002) besides. Running the guard here is what keeps ADR 0033 from
	# being a tool nobody executes, and rule 2 from being a sentence.
	$(MAKE) --no-print-directory guard
	# Every same-package doc link resolves (ADR 0138).
	$(MAKE) --no-print-directory doclinks
	# docs/api is what the code exports, byte for byte, on every cell; the
	# code's surface is the pins' markers, and every generated file still the
	# bytes of its design file (ADR 0163).
	$(MAKE) --no-print-directory api-check

# Gate on the gating phases (1-7) only — phase 8 (tests) is advisory, matching
# the MCP daemon's active set and the PostToolUse hook. `--phases=all` pulled in
# style-only test rules (TEST-TABLE/TEST-CONTEXT) that block no CI lane.
lint-ktn-check:
	ktn-linter lint --skip-phases=tests ./...

# `doclinks` fails on every same-package doc link in the repository that names
# no symbol its package declares — go/doc renders one as literal bracketed text
# on pkg.go.dev, in `go doc` and in the generated READMEs, and nothing else
# notices (#241). The dominant cause is structural, not a typo: a pkg/v1 facade
# ALIASES its types and go/doc collects methods and fields from the package's own
# declarations, so a member of an aliased type is written `[Type].Member`
# (ADR 0138). tools/genindex already walks packages through go/doc for the docs
# site, so the check is a mode of it: stdlib-only, GOWORK=off, no network.
doclinks:
	cd tools/genindex && GOWORK=off go run . -platforms $(CURDIR)/scripts/ci/platforms.sh -check-doclinks $(CURDIR)

# `api` writes docs/api: one JSON document per module of go.work — the SDK
# module and every vendor module — holding every exported symbol, internal
# packages included, with its go: id, kind, signature as its file spells it and
# canonically, owner, doc text, cells, file, codes, layer and family
# (docs/api/schema.json). tools/genindex reads the CODE: the go command lists
# each cell of scripts/ci/platforms.sh, go/types checks every package from
# source with function bodies ignored, and the records that differ between
# cells say on which they hold. Run it after a doc edit — a doc edit needs
# nothing else — and after any change to the exported surface, which starts in
# design/ and `kit gen` (ADR 0163): the flow is the design, kit gen, the code,
# then `make api`. Deterministic: the same tree writes the same bytes on every
# machine. It needs the workspace's modules in the module cache, so its first
# run may download them. It then writes docs/error-codes.yaml from the
# documents it wrote (`error-codes`), and runs gazelle, which gives the pin
# files kit gen writes — api_gen*_test.go, external tests — their test
# targets.
api:
	cd tools/genindex && GOWORK=off go run . -write-api -repo-root $(CURDIR) -platforms $(CURDIR)/scripts/ci/platforms.sh
	$(MAKE) --no-print-directory error-codes
	bazel run //:gazelle

# `api-check` holds the code to docs/api and to its design (ADR 0163), with no
# kit and no YAML read: it writes nothing and fails on
#   - any byte docs/api regenerated in memory differs from what is committed,
#     naming each record added, changed or removed — a doc edit without
#     `make api` included;
#   - -markers: on every cell, the code's exported (id, kind, canonical
#     signature) set differs from the `// go:<id> <kind> <canonical>` markers
#     of the api_gen*_test.go pins kit gen writes from design/ — a symbol
#     added in the code alone, a function turned variable, a constraint
#     widened, a struct tag, a receiver;
#   - -digests: a generated file (a pin, or a design_gen.go port) whose header
#     sha256 is no longer the bytes of the design file it names, or whose body
#     was edited by hand — a design edit without `kit gen`.
# What the pins hold themselves — a signature, a value, a type — the compiler
# catches: `go vet` on every cell (cross-build) and the Bazel test targets. It
# runs in `lint-check`, so CI's required job runs it.
api-check:
	cd tools/genindex && GOWORK=off go run . -check-api -markers -digests -repo-root $(CURDIR) -platforms $(CURDIR)/scripts/ci/platforms.sh

# `guard` runs tools/sdkguard over the SDK's own tree, in two passes.
#
# sdkguard is the consumer-facing enforcement of the SDK's ADRs (0033). It is a
# stdlib-only module OUTSIDE go.work, so it is invoked with GOWORK=off from its
# own directory with absolute path arguments — the same treatment genindex gets.
#
# The first pass runs every invariant over every tree the SDK writes Go in.
#
# The second is rule 2 of CLAUDE.md made a gate: SDK002 alone — no fmt.Errorf,
# no errors.New — over the SDK's production code: internal/, pkg/, third-party/
# and framework/, _test.go files left out as sdkguard leaves them by default (a
# fixture mints throwaway errors). The rule stated an AST audit that never
# existed, and 22 calls had passed CI. For a consumer SDK002 stays a convention
# (ADR 0033 §4: the SDK offers its error model and obliges nobody); the SDK
# holds itself to it. tools/ and e2e/ are outside rule 2: stdlib-only modules,
# they cannot import the errs package it sends a caller to. errors.Is, As,
# AsType, Join and Unwrap stay allowed, and so does errors.ErrUnsupported: none
# of them writes a message of its own — Join groups errors that keep their
# codes, which errs.HasCode walks — and a stdlib protocol may ask for the last.
#
# The SDK grants itself no exemption: a `//sdkguard:allow SDK002 <reason>`
# directive, honoured for a consumer, fails this target, and the scan fails
# closed when grep cannot read the tree. An exemption the SDK ever needs is
# granted by editing this recipe — where a reviewer sees it — never by a
# comment in a diff.
#
# SDK005 (the legacy log package) does not run on the SDK: the tree still calls
# log.Printf, and adopting that rule is its own change. Consumers choose their
# own level; see tools/sdkguard/CLAUDE.md.
# -version-check=off: the freshness probe reaches a module proxy, and `make
# lint` must not depend on network egress. The probe is for consumers, and the
# SDK is not a consumer of itself.
guard:
	cd tools/sdkguard && GOWORK=off go run . -level=invariant -version-check=off \
	  $(CURDIR)/internal/... $(CURDIR)/pkg/... $(CURDIR)/framework/... $(CURDIR)/tools/...
	cd tools/sdkguard && GOWORK=off go run . -rules=SDK002 -version-check=off \
	  $(CURDIR)/internal/... $(CURDIR)/pkg/... $(CURDIR)/third-party/... $(CURDIR)/framework/...
	@status=0; grep -rniE --include='*.go' 'sdkguard:allow[[:space:]]+sdk002' \
	  internal pkg third-party framework || status=$$?; \
	case $$status in \
	  0) echo "guard: rule 2 has no exemption — remove the //sdkguard:allow SDK002 directive(s) above"; exit 1 ;; \
	  1) ;; \
	  *) echo "guard: could not scan internal/ pkg/ third-party/ framework/ for SDK002 exemptions"; exit 1 ;; \
	esac

# The two gates below are shell-only: no Bazel, no Go, seconds to run. They are
# invoked by name from bazel-ci.yml, which is what makes them gates rather than
# advice — and ci-gates-check is the thing that asserts that.
#
# `release-scripts-check` runs scripts/release/*.bats. That suite guards the two
# scripts that decide which tag every merge gets, and from the commit that
# introduced it until this one, NOTHING executed it — no make target, no CI
# lane, no hook (ADR 0085 Deferred, ADR 0086).
ci-gates-check:
	bash scripts/ci-gates-check.sh

release-scripts-check:
	bash scripts/release/release-scripts-test.sh

# `ci-scripts-check` runs scripts/ci/*.bats: the module census every
# module-looping lane reads (scripts/ci/go-modules.sh, ADR 0137) and the
# govulncheck gate built on it (scripts/ci/vuln-check.sh, ADR 0136). The census
# exists because three hand-written module lists had drifted apart and none of
# them named tools/genindex or tools/sdkguard (#242); its suite is what keeps a
# fourth list from coming back.
ci-scripts-check:
	bash scripts/ci-scripts-test.sh

# `vuln-check` runs govulncheck in source mode over every module of the census,
# one module at a time, and fails on a vulnerable symbol any of them REACHES —
# not on one it merely imports (ADR 0136, #210). The standard library is in the
# scan; a finding there is settled by moving the go line and MODULE.bazel's
# go_sdk together. It needs the vulnerability database at vuln.go.dev, so unlike
# `make lint` it needs the network — which is why it is its own target and not
# part of `lint`.
#
# The scanner is pinned here and nowhere else. `vuln-install` installs exactly
# this build (a module version is verified against the checksum database, so
# the pin is the integrity check), and `vuln-check` refuses any other: a lane
# that followed `latest` would change its verdict with no commit moving.
GOVULNCHECK_VERSION := v1.8.0

vuln-install:
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

vuln-check:
	GOVULNCHECK_VERSION=$(GOVULNCHECK_VERSION) bash scripts/ci/vuln-check.sh

# `pre-commit-check` runs scripts/pre-commit/*.bats against the commit gates in
# scripts/pre-commit/. ADR 0088 recorded its own sweep as INCOMPLETE: two of
# those gates pipe into an early-exiting reader the same way the former commit-msg
# hook did, and they fail in opposite directions — check-ktn-phases-1-7.sh reports a
# clean linter run as a failed one, and check-audit-coverage.sh lets an
# uncovered package through, which is the gap that guard exists to close.
pre-commit-check:
	bash scripts/pre-commit-test.sh

# `bench` regenerates pkg/v1/data/codec/BENCH.md by running the full bench
# matrix programmatically (testing.Benchmark per row, no text-format
# parsing) under the `benchmark` build tag. Stamps a reproducibility
# envelope (CPU, RAM, OS, Go toolchain, git SHA, timestamp) at the top
# so cross-machine deltas can be evaluated honestly. Default per-row
# wall-clock is 1s (~8 min for the full 348-cell matrix): allocs/op and
# B/op are deterministic regardless of benchtime — only ns/op precision
# scales — so 1s is plenty for a snapshot pivot ranked by ns/op. Override
# with BENCH_TIME=10s for a publishable canonical run (~84 min).
#
# `-test.bench=^$$` is load-bearing: the BUILD.bazel target args bake in
# `-test.bench=.`, which would otherwise re-run all ten top-level Benchmark*
# funcs (~116 min) on top of the generator and blow the timeout. The
# generator already runs the full matrix internally, so we mask the bench
# selector down to "match nothing".
#
# `bazel run` (not `bazel test`) is the entry point so BUILD_WORKSPACE_DIRECTORY
# is set + the sandbox is lifted; that lets the test write BENCH.md back
# into the source tree at pkg/v1/data/codec/BENCH.md.
bench:
	bazel run //pkg/v1/data/codec:codec_bench_test -- \
		-test.run=TestGenerateBenchMD \
		-test.bench=^$$ \
		-test.timeout=2h \
		-test.benchtime=$${BENCH_TIME:-1s} \
		-test.v

cover:
	bazel coverage --combined_report=lcov //...
	@echo "LCOV report: $$(bazel info output_path)/_coverage/_coverage_report.dat"

# `docs` builds the full kitsunium/sdk documentation portal at
# docs/site/dist/. The Astro site pulls live content from the workspace
# at build time:
#   - / (home) — handwritten in src/pages/index.astro
#   - /getting-started, /philosophy, /architecture — handwritten
#   - /packages/{logger,codec,errs} — render pkg/v1/<pkg>/README.md
#   - /adr/ + /adr/<slug> — render every docs/adr/*.md
#   - /benchmarks — render pkg/v1/data/codec/BENCH.md (the mean-baseline pivot)
# node_modules/ + dist/ are gitignored — the artefact is rebuilt from
# source every run. Idempotent npm install is fast after first run.
docs:
	cd docs/site && npm install --silent && npm run build
	@echo "→ docs/site/dist/ ready ($$(find docs/site/dist -name '*.html' | wc -l) pages). Serve with: make serve"

# `docs-check` is the docs portal's gate, run by CI's `docs-site` job. It
# builds the working tree's release alone (DOCS_RELEASES=local: no release
# snapshot, no network, about 30 s) and checks what it built against the code:
# `npm test` (scripts/lib), then `npm run check` — the ⌘K index and every API
# section equal docs/api, counted apart from the code that wrote them
# (scripts/check-api-counts.mjs), and every link and ⌘K entry resolves under
# the deploy base, fragments included (scripts/check-links.mjs). The deploy
# (docs-deploy.yml) runs the same check over every release it builds.
docs-check:
	cd docs/site && npm ci --silent && npm test && DOCS_RELEASES=local npm run build && npm run check

# `serve` is the kill → rebuild → re-serve loop for the docs portal.
# Useful while iterating on src/pages/*.astro or src/styles/global.css:
# one command replaces "Ctrl+C, make docs, npx serve dist".
#
# 1. Kill any lingering `serve dist -l <PORT>` process — both ours and
#    background ones spawned by previous sessions. `|| true` swallows
#    the "no process matched" exit-1 from pkill so make doesn't abort
#    on a clean state.
# 2. Wait for the port to actually free (pkill returns before the
#    socket drops; we loop on `ss -tln`).
# 3. `make docs` — rebuilds dist/ from source.
# 4. Start a fresh static server. The PORT env var lets you switch
#    from the default 4321 (`PORT=5173 make serve`) without editing
#    the Makefile.
# NOTE: serve builds with DOCS_BASE=/ so the site is ROOT-served. The default
# build bakes base=/<repo> (for the GitHub Pages project page), under which the
# root index redirects to /<repo>/… — which `serve dist` can't resolve locally
# (files live at dist/, not dist/<repo>/), so the page comes up blank. Forcing
# base=/ for the local preview makes http://localhost:<port>/ work.
serve:
	@DOCS_BASE=/ $(MAKE) --no-print-directory docs
	@port=$${PORT:-4321}; \
	pkill -f "serve.*dist.*-l $$port" 2>/dev/null || true; \
	for i in 1 2 3 4 5; do \
		ss -tln 2>/dev/null | grep -q ":$$port " || break; \
		sleep 0.2; \
	done; \
	echo "→ serving docs/site/dist on http://localhost:$$port/ (Ctrl+C to stop)"; \
	cd docs/site && npx --yes serve dist -l $$port --no-clipboard

# `docs-dev` is the fast iteration loop. `serve` does a full PRODUCTION build
# (~135s: 40s types + 95s render + pagefind) on every restart; `docs-dev` runs
# the prebuild once, then `astro dev` with hot-module reload — edits to
# src/pages/*.astro, styles, ADRs, or package docs reflect live with NO rebuild.
# Use this while iterating; use `make serve` only to preview the real built site.
# DOCS_BASE=/ so the dev server is root-served (default base=/<repo> would put
# the app under /<repo>/ and leave / blank). Dev server: http://localhost:4321/.
docs-dev:
	cd docs/site && npm install --silent && DOCS_BASE=/ npm run dev

# `release-dry-run` previews the auto-bump pipeline without pushing
# any tag. compute-bumps.sh emits the token `sdk` when a release is
# due; cut-tags.sh shows the release it would cut — the SDK module's
# vX.Y.Z and the tag of each vendor module that changed since its own
# (ADR 0162). Explicit /bin/bash because the SDK shell is zsh and the
# release scripts use bash-only patterns (associative arrays,
# `< <(...)` process substitution). See ADR 0007 §Bump semantics.
#
# The exit code is captured and propagated. It used to be dropped by the `;`
# after the redirection, and make runs recipes under /bin/sh with no `-e`
# (this Makefile sets neither SHELL nor .SHELLFLAGS), so a compute-bumps that
# REFUSED to answer printed "no release is due" and exited 0 — the same
# two lines as a genuine no-op. Measured against a compute-bumps stub exiting 1:
# its two stderr lines were shown and then contradicted by the verdict below
# them. That would have re-swallowed, on the path a maintainer actually runs
# before a release, the voice #227 gave the script.
# `--explain` sends the verdict and the reason for it to stderr, which is this
# terminal. Without it the recipe printed "no release is due" and nothing
# about WHY — the local twin of #226, where a release run that published nothing
# left no recoverable reason either.
#
# The size is read from the `release:*` labels of the range's merged pull
# requests (ADR 0135), so the dry run asks GitHub through an authenticated `gh`
# — a lookup that fails is refused, never previewed as a patch. `BUMP=minor`
# (or patch, major) states the size instead and asks nothing, exactly as the
# SDK Release dispatch input does.
release-dry-run:
	@rc=0; /bin/bash scripts/release/compute-bumps.sh --dry-run --explain > /tmp/sdk-release-majors.txt || rc=$$?; \
	if [ "$$rc" -ne 0 ]; then \
		echo "release-dry-run: compute-bumps.sh exited $$rc; refusing to print a verdict for a computation that did not run" >&2; \
		exit "$$rc"; \
	fi; \
	if [ ! -s /tmp/sdk-release-majors.txt ]; then \
		echo "no release is due"; \
	else \
		echo "release token:"; cat /tmp/sdk-release-majors.txt; echo; \
		/bin/bash scripts/release/cut-tags.sh --dry-run $(if $(BUMP),--bump=$(BUMP),) < /tmp/sdk-release-majors.txt; \
	fi

# `docs-readme` regenerates pkg/v1/<service>/README.md from each
# package's Go doc comment via the `gomarkdoc` binary (ADR 0008).
# The binary is installed with `go install
# github.com/princjef/gomarkdoc/cmd/gomarkdoc@v1.1.0` so it lives on
# $PATH without adding ~50 indirect deps to the SDK module's go.mod,
# which requires nothing (ADR 0156).
#
# The package list is NOT enumerated here. It was, and it went stale: the
# enumeration named 15 packages while 27 carried a //go:generate directive,
# so client, server and ten others could never be regenerated at all. Letting
# `go generate ./...` find the directives makes the set self-maintaining — a
# new pkg/v1 package is covered the moment it declares one.
docs-readme:
	@command -v gomarkdoc >/dev/null 2>&1 \
	  || { echo "✗ gomarkdoc not on PATH. Install: go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@v1.1.0"; exit 1; }
	cd pkg/v1 && go generate ./...
	cd framework && go generate ./...
	@echo "→ every pkg/v1 and framework package declaring //go:generate gomarkdoc regenerated"

# `error-codes` writes docs/error-codes.yaml — the human-readable list of the
# dotted-quad error codes (ADR 0005/0006) — from docs/api: every errs.Code
# constant a package declares under a name starting with Code, re-exports and
# the errs masks left out (tools/genindex -write-error-codes, through
# scripts/gen-error-codes.sh). docs/api is the code's (`make api-check`), so
# the file is too; `make api` runs this target after writing docs/api. The
# executable source of truth stays the AST audit
# (internal/kernel/errs:errs_test). The check-error-codes-drift guard fails CI
# and `make lint` when the file is not what this writes.
error-codes:
	bash scripts/gen-error-codes.sh

# `profile WAVE=<slug>` captures CPU + memory + block + mutex pprof
# alongside a bench.txt summary for the named wave. Output lives under
# .bench/profiles/<WAVE>/ (gitignored — see .gitignore). Commit messages
# cite the bench.txt summary inline. Usage:
#
#   make profile WAVE=baseline
#   make profile WAVE=post-wave-1 COUNT=15 BENCHTIME=10s
#
# COUNT defaults to 10 (benchstat needs ≥10 samples for mannwhitney);
# BENCHTIME defaults to 5s (firms-up nanos on the small bench cells).
# `pkg/v1/data/codec/main_test.go` toggles SetBlockProfileRate(1) +
# SetMutexProfileFraction(1) when the corresponding -*profile flag is set.
WAVE ?= current
BEFORE ?= baseline
AFTER ?= $(WAVE)
profile:
	@mkdir -p .bench/profiles/$(WAVE)
	cd pkg && GOWORK=off go test -run='^$$' -bench=. -benchmem \
	  -count=$${COUNT:-10} -benchtime=$${BENCHTIME:-5s} \
	  -cpu=1,2,4,8,16 \
	  -cpuprofile=$(CURDIR)/.bench/profiles/$(WAVE)/cpu.out \
	  -memprofile=$(CURDIR)/.bench/profiles/$(WAVE)/mem.out \
	  -blockprofile=$(CURDIR)/.bench/profiles/$(WAVE)/block.out \
	  -mutexprofile=$(CURDIR)/.bench/profiles/$(WAVE)/mutex.out \
	  ./v1/data/codec/... \
	  | tee $(CURDIR)/.bench/profiles/$(WAVE)/bench.txt
	@echo "→ .bench/profiles/$(WAVE)/ {cpu,mem,block,mutex}.out + bench.txt"

# `benchstat-install` installs the analysis CLI when missing. Pinned
# to whatever golang.org/x/perf publishes on @latest; the CLI is
# backwards-stable on its flag surface so this is safe.
benchstat-install:
	@command -v benchstat >/dev/null 2>&1 \
	  || GOTOOLCHAIN=auto go install golang.org/x/perf/cmd/benchstat@latest

# `benchstat-diff` compares two wave directories. Mann-Whitney with
# 95% confidence per the benchstat docs; the p<0.05 cells become the
# ship-gate signal for Phase B agent commits.
benchstat-diff: benchstat-install
	@test -f .bench/profiles/$(BEFORE)/bench.txt || { echo "✗ .bench/profiles/$(BEFORE)/bench.txt missing — run: make profile WAVE=$(BEFORE)"; exit 1; }
	@test -f .bench/profiles/$(AFTER)/bench.txt  || { echo "✗ .bench/profiles/$(AFTER)/bench.txt missing — run: make profile WAVE=$(AFTER)"; exit 1; }
	benchstat -confidence=0.95 -delta-test=mannwhitney \
	  .bench/profiles/$(BEFORE)/bench.txt \
	  .bench/profiles/$(AFTER)/bench.txt

# ── Kernel benchmarks (tracker #16 / tooling #21) ───────────────────────
# `sdk-bench` runs every kernel *_bench_test.go via go test (NOT bazel) so the
# output stays benchstat-friendly, writing .bench.out for A/B comparison.
# A kernel package with no bench file is not a failure — go test just reports
# "no tests to run" and exits 0. Override the sample count with COUNT=N.
# NOTE: the result is redirected (not piped through tee) so make observes the
# go-test exit code directly — a failed build/bench aborts here instead of
# being masked by tee's success and leaving a stale/partial .bench.out behind.
sdk-bench:
	@echo "::group::kernel benches"
	@cd internal/kernel && GOWORK=off go test -run=^$$ -bench=. -benchmem \
	    -count=$${COUNT:-10} ./... > $(CURDIR)/.bench.out
	@echo "::endgroup::"
	@cat $(CURDIR)/.bench.out
	@echo "Output: .bench.out (feed to benchstat for A/B comparison)"

# `sdk-bench-profile` captures cpu+mem+block+mutex pprof for every kernel
# package, one package at a time. Each package writes into its own
# profiles/<pkg>/ directory: go test reuses fixed profile filenames, so a
# single `./...` run would have every package overwrite the previous one's
# profiles. Per-package directories keep a complete set. Takes several minutes;
# the block/mutex profiles need the benches that exercise sync primitives
# (ring, async) to register meaningful samples.
sdk-bench-profile:
	@mkdir -p $(CURDIR)/profiles
	@cd internal/kernel && for pkg in $$(GOWORK=off go list ./...); do \
	    name=$$(echo "$$pkg" | sed 's#.*/##'); \
	    out=$(CURDIR)/profiles/$$name; mkdir -p "$$out"; \
	    echo "profiling $$pkg → profiles/$$name/"; \
	    GOWORK=off go test -run=^$$ -bench=. -benchmem \
	        -cpuprofile="$$out/cpu.prof" \
	        -memprofile="$$out/mem.prof" \
	        -blockprofile="$$out/block.prof" \
	        -mutexprofile="$$out/mutex.prof" \
	        "$$pkg" || exit $$?; \
	done
	@echo "Profiles saved per package under profiles/<pkg>/ — e.g. 'go tool pprof -http=:8080 profiles/ring/cpu.prof'"

# `sdk-bench-compare` runs benchstat on the recorded baseline vs the current
# tree. Record the baseline first:
#   git checkout main && make sdk-bench && mv .bench.out .bench.main.out
sdk-bench-compare:
	@command -v benchstat >/dev/null 2>&1 || { \
	    echo "benchstat not found. Install: make benchstat-install"; \
	    exit 1; }
	@test -f .bench.main.out || { \
	    echo "Missing .bench.main.out — record the baseline first:"; \
	    echo "  git checkout main && make sdk-bench && mv .bench.out .bench.main.out"; \
	    exit 1; }
	@test -f .bench.out || { \
	    echo "Missing .bench.out — run make sdk-bench on the branch you want to compare."; \
	    exit 1; }
	@benchstat .bench.main.out .bench.out
