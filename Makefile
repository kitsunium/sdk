.PHONY: help build test lint bench cover docs docs-dev serve release-dry-run docs-readme error-codes profile benchstat-install benchstat-diff sdk-bench sdk-bench-profile sdk-bench-compare

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
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "serve"  "kill / rebuild / re-serve docs on http://localhost:$(DIM)\$${PORT:-4321}$(RST)$(DIM)/$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "docs-dev" "$(DIM)hot-reloading docs dev server (astro dev, no full rebuild — fast iteration)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "cover"  "bazel coverage --combined_report=lcov //..."
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "release-dry-run"  "$(DIM)compute-bumps + cut-tags in dry-run (see ADR 0007)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "docs-readme"  "$(DIM)regenerate pkg/v1/{codec,crypto,errs,hash,sign,kdf,password,logger,logger/writer}/README.md from doc comments (see ADR 0008)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "profile"      "$(DIM)capture cpu+mem+block+mutex pprof for codec bench (WAVE=<slug>)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "benchstat-diff" "$(DIM)compare two captured waves with mannwhitney p-values (BEFORE / AFTER)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "sdk-bench"        "$(DIM)run every internal/kernel/*_bench_test.go → .bench.out (COUNT=N)$(RST)"
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
	gofumpt -l -w internal pkg third-party
	bazel build //...

# `test` is the race-on suite. Always runs //... so the AST audit in
# //internal/kernel/errs:errs_test (no fmt.Errorf / errors.New leaks)
# is part of every run — no separate audit target needed.
test:
	bazel test --config=race //...

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
	@drift=$$(gofumpt -l internal pkg third-party); if [ -n "$$drift" ]; then \
		echo "gofumpt drift in the following files (run 'make build' to fix):"; \
		echo "$$drift"; exit 1; \
	fi
	# Gate on the gating phases (1-7) only — phase 8 (tests) is advisory, matching
	# the MCP daemon's active set and the PostToolUse hook. `--phases=all` pulled in
	# style-only test rules (TEST-TABLE/TEST-CONTEXT) that block no CI lane.
	ktn-linter lint --skip-phases=tests ./...
	# Exemption invariant: a `//go:build !race` test is invisible to the race
	# suite, so the alloc lane is its only gate. Fail if one runs in no lane.
	bash scripts/pre-commit/check-alloc-lane-coverage.sh

# `bench` regenerates pkg/v1/codec/BENCH.md by running the full bench
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
# into the source tree at pkg/v1/codec/BENCH.md.
bench:
	bazel run //pkg/v1/codec:codec_bench_test -- \
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
#   - /benchmarks — render pkg/v1/codec/BENCH.md (the mean-baseline pivot)
# node_modules/ + dist/ are gitignored — the artefact is rebuilt from
# source every run. Idempotent npm install is fast after first run.
docs:
	cd docs/site && npm install --silent && npm run build
	@echo "→ docs/site/dist/ ready ($$(find docs/site/dist -name '*.html' | wc -l) pages). Serve with: make serve"

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
# any tag. compute-bumps.sh emits the list of pkg/<major> dirs that
# would receive a patch bump; cut-tags.sh shows the resulting tag for
# each. Explicit /bin/bash because the SDK shell is zsh and the
# release scripts use bash-only patterns (associative arrays,
# `< <(...)` process substitution). See ADR 0007 §Bump semantics.
release-dry-run:
	@/bin/bash scripts/release/compute-bumps.sh --dry-run > /tmp/sdk-release-majors.txt; \
	if [ ! -s /tmp/sdk-release-majors.txt ]; then \
		echo "no majors need bumping"; \
	else \
		echo "majors to bump:"; cat /tmp/sdk-release-majors.txt; echo; \
		/bin/bash scripts/release/cut-tags.sh --dry-run < /tmp/sdk-release-majors.txt; \
	fi

# `docs-readme` regenerates pkg/v1/<service>/README.md from each
# package's Go doc comment via the `gomarkdoc` binary (ADR 0008).
# The binary is installed by the devcontainer Go feature
# (.devcontainer/features/languages/go/install.sh) so it lives on
# $PATH without polluting pkg/go.mod with ~50 indirect deps.
docs-readme:
	@command -v gomarkdoc >/dev/null 2>&1 \
	  || { echo "✗ gomarkdoc not on PATH. Install: go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@v1.1.0 (or rebuild devcontainer)"; exit 1; }
	cd pkg/v1 && go generate ./codec ./crypto ./errs ./hash ./id ./sign ./kdf ./password ./logger ./logger/writer
	@echo "→ pkg/v1/{codec,crypto,errs,hash,id,sign,kdf,password,logger,logger/writer}/README.md regenerated"

# `error-codes` regenerates docs/error-codes.yaml — the human-readable mirror of
# the dotted-quad error-code registry (ADR 0005/0006), extracted from every
# errs.Code constant in the tree. The executable source of truth stays the AST
# audit (internal/kernel/errs:errs_test); this YAML is for humans. The
# check-error-codes-drift pre-commit guard fails the commit when it is stale.
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
# `pkg/v1/codec/main_test.go` toggles SetBlockProfileRate(1) +
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
	  ./v1/codec/... \
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
