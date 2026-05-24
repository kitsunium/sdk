.PHONY: help build test lint bench cover docs serve release-dry-run docs-readme profile benchstat-install benchstat-diff

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
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "cover"  "bazel coverage --combined_report=lcov //..."
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "release-dry-run"  "$(DIM)compute-bumps + cut-tags in dry-run (see ADR 0007)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "docs-readme"  "$(DIM)regenerate pkg/v1/{codec,errs,logger}/README.md from doc comments (see ADR 0008)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "profile"      "$(DIM)capture cpu+mem+block+mutex pprof for codec bench (WAVE=<slug>)$(RST)"
	@printf "  $(GREEN)%-7s$(RST)  %s\n" "benchstat-diff" "$(DIM)compare two captured waves with mannwhitney p-values (BEFORE / AFTER)$(RST)"

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
	gofumpt -l -w internal pkg
	bazel build //...

# `test` is the race-on suite. Always runs //... so the AST audit in
# //internal/kernel/errs:errs_test (no fmt.Errorf / errors.New leaks)
# is part of every run — no separate audit target needed.
test:
	bazel test --config=race //...

# `test-alloc` runs the per-codec allocation-budget regression gate. These
# tests carry `//go:build !race` (testing.AllocsPerRun reports +1 under
# -race), so they are invisible to the race suite above and need this
# race-off pass. CI runs it as a dedicated step; run it locally before
# touching a codec's allocation profile.
test-alloc:
	bazel test --config=alloc //internal/service/codec/...

# `lint` is the read-only counterpart of `build`: same checks, but it
# REFUSES to write — it asserts the tree is already consistent.
lint:
	bazel mod tidy
	bazel run //:gazelle -- -mode=diff
	@git diff --exit-code MODULE.bazel '**/BUILD.bazel'
	@drift=$$(gofumpt -l internal pkg); if [ -n "$$drift" ]; then \
		echo "gofumpt drift in the following files (run 'make build' to fix):"; \
		echo "$$drift"; exit 1; \
	fi
	ktn-linter lint --phases=all ./...

# `bench` regenerates pkg/v1/codec/BENCH.md by running the full bench
# matrix programmatically (testing.Benchmark per row, no text-format
# parsing) under the `benchmark` build tag. Stamps a reproducibility
# envelope (CPU, RAM, OS, Go toolchain, git SHA, timestamp) at the top
# so cross-machine deltas can be evaluated honestly. Default per-row
# wall-clock is 10s — enough to firm up numbers on the 5-operation × 3-size
# x 18-codec matrix; override with e.g. BENCH_TIME=1s for a smoke regen.
#
# `bazel run` (not `bazel test`) is the entry point so BUILD_WORKSPACE_DIRECTORY
# is set + the sandbox is lifted; that lets the test write BENCH.md back
# into the source tree at pkg/v1/codec/BENCH.md.
bench:
	bazel run //pkg/v1/codec:codec_bench_test -- \
		-test.run=TestGenerateBenchMD \
		-test.timeout=2h \
		-test.benchtime=$${BENCH_TIME:-10s} \
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
serve: docs
	@port=$${PORT:-4321}; \
	pkill -f "serve.*dist.*-l $$port" 2>/dev/null || true; \
	for i in 1 2 3 4 5; do \
		ss -tln 2>/dev/null | grep -q ":$$port " || break; \
		sleep 0.2; \
	done; \
	echo "→ serving docs/site/dist on http://localhost:$$port/ (Ctrl+C to stop)"; \
	cd docs/site && npx --yes serve dist -l $$port --no-clipboard

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
# $PATH without polluting pkg/v1/go.mod with ~50 indirect deps.
docs-readme:
	@command -v gomarkdoc >/dev/null 2>&1 \
	  || { echo "✗ gomarkdoc not on PATH. Install: go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@v1.1.0 (or rebuild devcontainer)"; exit 1; }
	cd pkg/v1 && go generate ./codec ./errs ./logger
	@echo "→ pkg/v1/{codec,errs,logger}/README.md regenerated"

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
	cd pkg/v1 && GOWORK=off go test -run='^$$' -bench=. -benchmem \
	  -count=$${COUNT:-10} -benchtime=$${BENCHTIME:-5s} \
	  -cpu=1,2,4,8 \
	  -cpuprofile=$(CURDIR)/.bench/profiles/$(WAVE)/cpu.out \
	  -memprofile=$(CURDIR)/.bench/profiles/$(WAVE)/mem.out \
	  -blockprofile=$(CURDIR)/.bench/profiles/$(WAVE)/block.out \
	  -mutexprofile=$(CURDIR)/.bench/profiles/$(WAVE)/mutex.out \
	  ./codec/... \
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
